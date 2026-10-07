package app

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
)

const archiveBaseURL = "https://data.binance.vision/data/futures/um/daily/aggTrades"

func (s *Service) volumeArchiveLoop(ctx context.Context) {
	// Current-session continuity has priority over historical reconstruction.
	// Waiting here also prevents archive writes from delaying the initial REST
	// catch-up and WebSocket hand-off.
	select {
	case <-ctx.Done():
		return
	case <-s.volumeLiveReady:
	}
	s.importVolumeArchives(ctx)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.importVolumeArchives(ctx)
		}
	}
}

func (s *Service) importVolumeArchives(ctx context.Context) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	start, before := today.AddDate(0, 0, -31), today.AddDate(0, 0, -1)
	allReady := true
	for _, symbol := range s.cfg.Symbols {
		for day := start; day.Before(before); day = day.AddDate(0, 0, 1) {
			if ctx.Err() != nil {
				return
			}
			imported, err := s.store.ArchiveImported(ctx, symbol, day)
			if err != nil {
				allReady = false
				continue
			}
			if imported {
				continue
			}
			s.log.Info("volume archive import started", "symbol", symbol, "day", day.Format("2006-01-02"))
			if err = s.importVolumeArchiveDay(ctx, symbol, day); err != nil {
				allReady = false
				_ = s.store.MarkArchiveImport(ctx, symbol, day, "failed", err.Error())
				s.log.Warn("volume archive import failed", "symbol", symbol, "day", day.Format("2006-01-02"), "error", err)
				continue
			}
			_ = s.store.MarkArchiveImport(ctx, symbol, day, volumeprofile.ArchiveStatus, "")
			s.log.Info("volume archive import complete", "symbol", symbol, "day", day.Format("2006-01-02"))
		}
		count, err := s.store.CompleteArchiveDays(ctx, symbol, start, before)
		if err != nil || count < 30 {
			allReady = false
		}
	}
	s.mu.Lock()
	becameReady := allReady && !s.volumeHistoryReady
	s.volumeHistoryReady = allReady
	s.mu.Unlock()
	if becameReady {
		s.log.Info("thirty day volume profile history ready")
		s.train(ctx)
	}
}

func (s *Service) importVolumeArchiveDay(ctx context.Context, symbol string, day time.Time) error {
	name := fmt.Sprintf("%s-aggTrades-%s.zip", symbol, day.Format("2006-01-02"))
	url := archiveBaseURL + "/" + symbol + "/" + name
	temporary, err := os.CreateTemp(filepath.Dir(s.cfg.DatabasePath), "aggtrades-*.zip")
	if err != nil {
		return err
	}
	path := temporary.Name()
	temporary.Close()
	defer os.Remove(path)
	if err = downloadFile(ctx, url, path); err != nil {
		return err
	}
	want, err := downloadChecksum(ctx, url+".CHECKSUM")
	if err != nil {
		return err
	}
	got, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("archive checksum mismatch: want %s got %s", want, got)
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) != 1 {
		return fmt.Errorf("expected one CSV in archive, got %d", len(reader.File))
	}
	file, err := reader.File[0].Open()
	if err != nil {
		return err
	}
	defer file.Close()
	csvReader := csv.NewReader(bufio.NewReaderSize(file, 1<<20))
	csvReader.ReuseRecord = true
	var snapshots []domain.VolumeProfileSnapshot
	accumulator := newArchiveAccumulator(symbol, day, func(_ context.Context, snapshot domain.VolumeProfileSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	})
	for {
		record, readErr := csvReader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		if len(record) < 6 || strings.EqualFold(record[0], "agg_trade_id") {
			continue
		}
		id, idErr := strconv.ParseInt(record[0], 10, 64)
		price, priceErr := strconv.ParseFloat(record[1], 64)
		quantity, quantityErr := strconv.ParseFloat(record[2], 64)
		timestamp, timeErr := strconv.ParseInt(record[5], 10, 64)
		if idErr != nil || priceErr != nil || quantityErr != nil || timeErr != nil {
			return fmt.Errorf("invalid aggregate trade CSV row")
		}
		if timestamp > 1e15 {
			timestamp /= 1000
		}
		if err = accumulator.Add(ctx, domain.AggregateTrade{ID: id, Symbol: symbol, Time: time.UnixMilli(timestamp).UTC(), Price: price, Quantity: quantity, PriceText: record[1]}); err != nil {
			return err
		}
	}
	if err = accumulator.Finish(ctx, day.Add(24*time.Hour)); err != nil {
		return err
	}
	return s.store.SaveVolumeProfileSnapshots(ctx, snapshots)
}

type archiveAccumulator struct {
	symbol  string
	day     time.Time
	session volumeprofile.Session
	levels  map[float64]float64
	next    time.Time
	save    func(context.Context, domain.VolumeProfileSnapshot) error
}

func newArchiveAccumulator(symbol string, day time.Time, save func(context.Context, domain.VolumeProfileSnapshot) error) *archiveAccumulator {
	return &archiveAccumulator{symbol: symbol, day: day, levels: make(map[float64]float64), save: save}
}

func (a *archiveAccumulator) Add(ctx context.Context, trade domain.AggregateTrade) error {
	session, err := volumeprofile.SessionAt(trade.Time)
	if err != nil {
		return err
	}
	if a.session.Start.IsZero() || !a.session.Start.Equal(session.Start) {
		if !a.session.Start.IsZero() {
			if err = a.flushUntil(ctx, a.session.End, false); err != nil {
				return err
			}
		}
		a.session = session
		a.levels = make(map[float64]float64)
		a.next = session.Start.Add(5 * time.Minute)
	}
	if err = a.flushUntil(ctx, trade.Time, true); err != nil {
		return err
	}
	a.levels[trade.Price] += trade.Price * trade.Quantity
	return nil
}

func (a *archiveAccumulator) flushUntil(ctx context.Context, until time.Time, inclusive bool) error {
	for !a.next.IsZero() && (a.next.Before(until) || inclusive && a.next.Equal(until)) {
		levels := make([]volumeprofile.Level, 0, len(a.levels))
		for price, volume := range a.levels {
			levels = append(levels, volumeprofile.Level{Price: price, VolumeUSD: volume})
		}
		profile := volumeprofile.Build(a.symbol, a.session, levels, a.next, a.next, true)
		if profile.VAL != nil && profile.VAH != nil {
			if err := a.save(ctx, domain.VolumeProfileSnapshot{Symbol: a.symbol, Time: a.next, SessionStart: a.session.Start, VAL: *profile.VAL, VAH: *profile.VAH, TotalVolumeUSD: profile.TotalVolumeUSD, Complete: true}); err != nil {
				return err
			}
		}
		a.next = a.next.Add(5 * time.Minute)
	}
	return nil
}

func (a *archiveAccumulator) Finish(ctx context.Context, dayEnd time.Time) error {
	if a.session.Start.IsZero() {
		return fmt.Errorf("archive contained no aggregate trades")
	}
	if dayEnd.After(a.session.End) {
		dayEnd = a.session.End
	}
	return a.flushUntil(ctx, dayEnd, false)
}

func downloadFile(ctx context.Context, url, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: %s", url, response.Status)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, response.Body)
	return err
}

func downloadChecksum(ctx context.Context, url string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := (&http.Client{Timeout: time.Minute}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return "", fmt.Errorf("GET %s: %s", url, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("invalid checksum response")
	}
	return fields[0], nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
