package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type DepthUpdate struct {
	First     int64      `json:"U"`
	Last      int64      `json:"u"`
	Previous  int64      `json:"pu"`
	EventTime int64      `json:"E"`
	Bids      [][]string `json:"b"`
	Asks      [][]string `json:"a"`
}
type LocalBook struct {
	Symbol     string
	Tick       float64
	Sequence   int64
	Ready      bool
	bids, asks map[int64]float64
}

func NewLocalBook(symbol string, tick float64, sequence int64, bids, asks [][]string) *LocalBook {
	b := &LocalBook{Symbol: symbol, Tick: tick, Sequence: sequence, bids: map[int64]float64{}, asks: map[int64]float64{}}
	b.apply(b.bids, bids)
	b.apply(b.asks, asks)
	return b
}
func (b *LocalBook) apply(levels map[int64]float64, rows [][]string) {
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		p, q := f(r[0]), f(r[1])
		if p <= 0 || q < 0 {
			continue
		}
		key := int64(math.Round(p / b.Tick))
		if q == 0 {
			delete(levels, key)
		} else {
			levels[key] = q
		}
	}
}
func (b *LocalBook) Update(u DepthUpdate) (bool, error) {
	if u.Last < b.Sequence || (b.Ready && u.Last == b.Sequence) {
		return false, nil
	}
	if !b.Ready {
		if u.First > b.Sequence {
			return false, errors.New("depth snapshot has no overlapping first update")
		}
	} else if u.Previous != b.Sequence {
		return false, errors.New("depth sequence gap")
	}
	b.apply(b.bids, u.Bids)
	b.apply(b.asks, u.Asks)
	b.Sequence = u.Last
	b.Ready = true
	return true, nil
}
func (b *LocalBook) Snapshot(at time.Time) domain.BookSnapshot {
	out := domain.BookSnapshot{Symbol: b.Symbol, Time: at, Sequence: b.Sequence, BucketWidth: b.Tick * 10, Bids: []domain.DepthLevel{}, Asks: []domain.DepthLevel{}}
	group := func(levels map[int64]float64, bid bool) []domain.DepthLevel {
		bins := map[int64]domain.DepthLevel{}
		for p, q := range levels {
			key := p / 10
			if !bid {
				key = (p + 9) / 10
			}
			lv := bins[key]
			lv.Price = float64(key*10) * b.Tick
			lv.Quantity += q
			lv.NotionalUSD += float64(p) * b.Tick * q
			bins[key] = lv
		}
		keys := make([]int64, 0, len(bins))
		for k := range bins {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if bid {
				return keys[i] > keys[j]
			}
			return keys[i] < keys[j]
		})
		if len(keys) > 60 {
			keys = keys[:60]
		}
		r := make([]domain.DepthLevel, 0, len(keys))
		for _, k := range keys {
			r = append(r, bins[k])
		}
		return r
	}
	out.Bids = group(b.bids, true)
	out.Asks = group(b.asks, false)
	for p := range b.bids {
		price := float64(p) * b.Tick
		if price > out.BestBid {
			out.BestBid = price
		}
	}
	for p := range b.asks {
		price := float64(p) * b.Tick
		if out.BestAsk == 0 || price < out.BestAsk {
			out.BestAsk = price
		}
	}
	return out
}
func StartDepthStreams(ctx context.Context, symbols []string, sink func(context.Context, domain.BookSnapshot) error, h *HealthRegistry, log *slog.Logger) {
	for _, sym := range symbols {
		symbol := sym
		go reconnect(ctx, "binance_depth_"+symbol, 1, h, log, func(ctx context.Context) error { return runDepth(ctx, symbol, sink, h) })
	}
}
func runDepth(ctx context.Context, symbol string, sink func(context.Context, domain.BookSnapshot) error, h *HealthRegistry) error {
	cc, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := dial(cc, "wss://fstream.binance.com/public/ws/"+strings.ToLower(symbol)+"@depth@100ms")
	if err != nil {
		return err
	}
	defer c.Close()
	type frame struct {
		data []byte
		err  error
	}
	frames := make(chan frame, 8192)
	go func() {
		for {
			_, data, e := c.ReadMessage()
			select {
			case frames <- frame{data, e}:
			case <-cc.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	client := newHTTP()
	var meta struct {
		Symbols []struct {
			Symbol  string `json:"symbol"`
			Filters []struct {
				Type string `json:"filterType"`
				Tick string `json:"tickSize"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err = client.get(cc, "https://fapi.binance.com/fapi/v1/exchangeInfo", &meta); err != nil {
		return err
	}
	tick := 0.
	for _, s := range meta.Symbols {
		if s.Symbol == symbol {
			for _, filter := range s.Filters {
				if filter.Type == "PRICE_FILTER" {
					tick = f(filter.Tick)
				}
			}
		}
	}
	if tick <= 0 {
		return fmt.Errorf("no price tick for %s", symbol)
	}
	var snap struct {
		Last int64      `json:"lastUpdateId"`
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err = client.get(cc, "https://fapi.binance.com/fapi/v1/depth?symbol="+symbol+"&limit=1000", &snap); err != nil {
		return err
	}
	book := NewLocalBook(symbol, tick, snap.Last, snap.Bids, snap.Asks)
	lastEmit := time.Time{}
	deadline := time.NewTimer(23*time.Hour + 50*time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("proactive depth reconnect")
		case frame := <-frames:
			if frame.err != nil {
				return frame.err
			}
			var u DepthUpdate
			if err = json.Unmarshal(frame.data, &u); err != nil {
				return err
			}
			if u.Last <= 0 {
				continue
			}
			applied, e := book.Update(u)
			if e != nil {
				return e
			}
			if !applied {
				continue
			}
			h.Touch("binance_depth_"+symbol, 1)
			at := time.Now().UTC()
			if at.Sub(lastEmit) >= 250*time.Millisecond {
				if err = sink(ctx, book.Snapshot(at)); err != nil {
					return err
				}
				lastEmit = at
			}
		}
	}
}
