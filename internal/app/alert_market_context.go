package app

import (
	"context"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// liquidationMarketContext builds the 1m market window used by the radar. The
// liquidation event price is overlaid on the current minute so a just-arrived
// cascade is evaluated against the newest price even when the minute candle is
// still being written by the regular market poller.
func (s *Service) liquidationMarketContext(ctx context.Context, event domain.LiquidationEvent, cfg alert.Config) alert.MarketContext {
	period := cfg.VWAPPeriodMinutes
	if period <= 0 {
		period = 240
	}
	at := event.EventTime.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	from := at.Add(-time.Duration(period+2) * time.Minute)
	to := at.Add(time.Minute)
	rows, err := s.store.CandlesRange(ctx, strings.ToUpper(strings.TrimSpace(event.Symbol)), from, to)
	if err != nil || len(rows) == 0 {
		return alert.MarketContext{Price: event.Price}
	}

	price := event.Price
	if price <= 0 {
		price = rows[len(rows)-1].Close
	}
	bucket := at.Truncate(time.Minute)
	last := len(rows) - 1
	if rows[last].Time.Equal(bucket) {
		rows[last].Close = price
		if rows[last].High < price {
			rows[last].High = price
		}
		if rows[last].Low <= 0 || rows[last].Low > price {
			rows[last].Low = price
		}
	} else if rows[last].Time.Before(bucket) {
		rows = append(rows, domain.Candle{Exchange: "binance", Symbol: event.Symbol, Time: bucket, Open: price, High: price, Low: price, Close: price})
	}
	if len(rows) > period {
		rows = rows[len(rows)-period:]
	}
	if len(rows) < period {
		return alert.MarketContext{Price: price, Candles: rows}
	}
	current := rows[len(rows)-1]
	return alert.MarketContext{Ready: true, Price: price, Candles: rows, CurrentCandle: current}
}
