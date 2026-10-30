package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func (s *Store) SaveOptionGamma(ctx context.Context, p domain.OptionGammaPoint) error {
	if (p.Symbol != "BTCUSDT" && p.Symbol != "ETHUSDT") || p.Source != "deribit" || p.Time.IsZero() || p.Gamma == nil || math.IsNaN(*p.Gamma) || math.IsInf(*p.Gamma, 0) || math.Abs(*p.Gamma) > 1 || p.Contracts < 1 || (p.State != "ok" && p.State != "partial") {
		return fmt.Errorf("invalid Deribit Gamma point")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO deribit_gamma_history(symbol,ts,payload) VALUES(?,?,?) ON CONFLICT(symbol,ts) DO UPDATE SET payload=excluded.payload`, p.Symbol, p.Time.UnixMilli(), data)
	return err
}

func (s *Store) LatestOptionGamma(ctx context.Context, symbol string) (domain.OptionGammaPoint, error) {
	var point domain.OptionGammaPoint
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM deribit_gamma_history WHERE symbol=? ORDER BY ts DESC LIMIT 1`, symbol).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &point)
	}
	return point, err
}

func (s *Store) OptionGammaHistory(ctx context.Context, symbol string, from, to time.Time) ([]domain.OptionGammaPoint, error) {
	fromMillis, toMillis := from.UnixMilli(), to.UnixMilli()
	denseCutoff := to.Add(-time.Duration(domain.OptionGammaDenseHours) * time.Hour).UnixMilli()
	if denseCutoff < fromMillis {
		denseCutoff = fromMillis
	}
	bucketMillis := int64(domain.OptionGammaArchiveMinutes) * int64(time.Minute/time.Millisecond)
	// Old data is reduced at read time as well as during daily maintenance. Only
	// exact UTC :00/:30 samples are part of the archive tier; this keeps the API
	// contract deterministic between cleanup runs and avoids redundant points.
	rows, err := s.db.QueryContext(ctx, `
SELECT payload,ts FROM deribit_gamma_history
WHERE symbol=? AND ts>=? AND ts<?
UNION ALL
SELECT payload,ts FROM deribit_gamma_history
WHERE symbol=? AND ts>=? AND ts<? AND ts % ? = 0
ORDER BY ts`, symbol, denseCutoff, toMillis, symbol, fromMillis, denseCutoff, bucketMillis)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []domain.OptionGammaPoint{}
	for rows.Next() {
		var data []byte
		var point domain.OptionGammaPoint
		var ts int64
		if err = rows.Scan(&data, &ts); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &point); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (s *Store) OptionGammaAvailableFrom(ctx context.Context, since time.Time) (*time.Time, error) {
	var ts *int64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM deribit_gamma_history WHERE ts>=?`, since.UnixMilli()).Scan(&ts); err != nil {
		return nil, err
	}
	if ts == nil {
		return nil, nil
	}
	at := time.UnixMilli(*ts).UTC()
	return &at, nil
}

type OptionGammaCleanupReport struct {
	Expired     int64
	Downsampled int64
}

// CleanupOptionGamma applies the two storage tiers in bounded transactions:
// two-minute source data for 24 hours, then only exact wall-clock :00/:30
// samples through day 3. The time slots are UTC, which has the same minute
// boundaries as Beijing time.
func (s *Store) CleanupOptionGamma(ctx context.Context, now time.Time) (OptionGammaCleanupReport, error) {
	report := OptionGammaCleanupReport{}
	cutoff := now.UTC().Add(-time.Duration(domain.OptionGammaMaxHours) * time.Hour).UnixMilli()
	var err error
	if report.Expired, err = s.pruneBatches(ctx, "deribit_gamma_history", "ts", cutoff); err != nil {
		return report, err
	}

	denseCutoff := now.UTC().Add(-time.Duration(domain.OptionGammaDenseHours) * time.Hour).UnixMilli()
	archiveRemoved, err := s.downsampleOptionGamma(ctx, cutoff, denseCutoff, domain.OptionGammaArchiveMinutes)
	report.Downsampled += archiveRemoved
	if err != nil {
		return report, err
	}
	denseRemoved, err := s.downsampleOptionGamma(ctx, denseCutoff, now.UTC().Add(time.Minute).UnixMilli(), domain.OptionGammaRefreshSeconds/60)
	report.Downsampled += denseRemoved
	return report, err
}

func (s *Store) downsampleOptionGamma(ctx context.Context, from, to int64, bucketMinutes int) (int64, error) {
	bucketMillis := int64(bucketMinutes) * int64(time.Minute/time.Millisecond)
	if bucketMinutes == domain.OptionGammaArchiveMinutes {
		return s.deleteOptionGammaOutsideSlots(ctx, from, to, bucketMillis)
	}
	halfBucket := bucketMillis / 2
	query := `DELETE FROM deribit_gamma_history WHERE rowid IN (
  SELECT rowid FROM (
    SELECT rowid,ROW_NUMBER() OVER (
      PARTITION BY symbol,((ts+?)/?)
      ORDER BY ABS(ts-(((ts+?)/?)*?)),ts DESC
    ) AS keep_rank
    FROM deribit_gamma_history
    WHERE ts>=? AND ts<?
  ) WHERE keep_rank>1 LIMIT ?
)`
	var total int64
	for {
		result, execErr := s.db.ExecContext(ctx, query, halfBucket, bucketMillis, halfBucket, bucketMillis, bucketMillis, from, to, pruneBatchSize)
		if execErr != nil {
			return total, execErr
		}
		count, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return total, rowsErr
		}
		total += count
		if count < pruneBatchSize {
			return total, nil
		}
	}
}

func (s *Store) deleteOptionGammaOutsideSlots(ctx context.Context, from, to, bucketMillis int64) (int64, error) {
	query := `DELETE FROM deribit_gamma_history WHERE rowid IN (
  SELECT rowid FROM deribit_gamma_history
  WHERE ts>=? AND ts<? AND ts % ? <> 0
  LIMIT ?
)`
	var total int64
	for {
		result, err := s.db.ExecContext(ctx, query, from, to, bucketMillis, pruneBatchSize)
		if err != nil {
			return total, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += count
		if count < pruneBatchSize {
			return total, nil
		}
	}
}

func (s *Store) PruneOptionGamma(ctx context.Context, now time.Time) error {
	_, err := s.CleanupOptionGamma(ctx, now)
	return err
}
