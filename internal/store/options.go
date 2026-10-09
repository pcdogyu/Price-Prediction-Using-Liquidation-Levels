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
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM deribit_gamma_history WHERE symbol=? AND ts>=? AND ts<? ORDER BY ts`, symbol, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []domain.OptionGammaPoint{}
	for rows.Next() {
		var data []byte
		var point domain.OptionGammaPoint
		if err = rows.Scan(&data); err != nil {
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

func (s *Store) PruneOptionGamma(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-time.Duration(domain.OptionGammaMaxHours) * time.Hour).UnixMilli()
	for {
		// Batch deletes allow other collectors to use the SQLite writer.
		result, err := s.db.ExecContext(ctx, `DELETE FROM deribit_gamma_history WHERE rowid IN (SELECT rowid FROM deribit_gamma_history WHERE ts<? LIMIT 2000)`, cutoff)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count < 2000 {
			return nil
		}
	}
}
