package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

func TestDashboardRoutesAndValidation(t *testing.T) {
	srv, st := testService(t, config.Config{BasePath: "/liquidation/"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 302 || w.Header().Get("Location") != "/liquidation/bubbles" {
		t.Fatal(w.Code, w.Header())
	}
	for _, path := range []string{"/bubbles", "/liquidations", "/hedge-wall", "/market-info"} {
		w = httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/liquidation/market-info"`) || !strings.Contains(w.Body.String(), `content="/liquidation/"`) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/liquidations?minimum=NaN", "/api/v1/liquidations?minimum=-1", "/api/v1/liquidations?side=buy", "/api/v1/liquidations?field=bad", "/api/v1/liquidations?cursor=bad", "/api/v1/hedge-wall?symbol=ETHUSDT&half_life=0", "/api/v1/hedge-wall/history?symbol=ETHUSDT&kind=bad", "/api/v1/market-info?symbol=ETHUSDT&range=100d"} {
		w = httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	at := time.Now().UTC().Add(-time.Second)
	e := domain.LiquidationEvent{ID: "small-sol", Exchange: "binance", Symbol: "SOLUSDT", PositionSide: "short", EventTime: at, ReceivedAt: at, Quantity: 1, Price: 1, NotionalUSD: 1, Coverage: "sampled"}
	if err := st.InsertLiquidation(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/liquidations", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "small-sol") {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Optional real-browser check uses an isolated SQLite fixture and never starts
// exchange collectors or reads production credentials.
func TestDashboardBrowser(t *testing.T) {
	script := os.Getenv("DASHBOARD_UI_SCRIPT")
	if script == "" {
		t.Skip("DASHBOARD_UI_SCRIPT is not set")
	}
	srv, st := testService(t, config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer st.Close()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Minute)
	price, oi, r, zero := 2500., 1000000., 2., 0.
	for _, symbol := range []string{"ETHUSDT", "BTCUSDT"} {
		cs := []domain.Candle{}
		for i := 0; i < 1500; i++ {
			ts := at.Add(-time.Duration(1500-i) * time.Minute)
			cs = append(cs, domain.Candle{Exchange: "binance", Symbol: symbol, Time: ts, Open: price, High: price + 1, Low: price - 1, Close: price, VolumeUSD: 1000, TakerBuyUSD: 600})
			if i%5 == 0 {
				if err := st.SaveMarketMetric(ctx, domain.MarketMetric{Symbol: symbol, Time: ts, MarkPrice: &price, OIUSD: &oi, TopPositionRatio: &r, AccountRatio: &r, FundingRate: &zero}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := st.UpsertCandles(ctx, cs); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveMarketMetric(ctx, domain.MarketMetric{Symbol: symbol, Time: at, MarkPrice: &price, OIUSD: &oi, TopPositionRatio: &r, AccountRatio: &r, FundingRate: &zero}); err != nil {
			t.Fatal(err)
		}
		b := domain.BookSnapshot{Symbol: symbol, Time: at, BucketWidth: .1, BestBid: 2499.9, BestAsk: 2500.1, Bids: []domain.DepthLevel{{Price: 2499.9, Quantity: 200, NotionalUSD: 499980}, {Price: 2499.8, Quantity: 250, NotionalUSD: 624950}}, Asks: []domain.DepthLevel{{Price: 2500.1, Quantity: 180, NotionalUSD: 450018}, {Price: 2500.2, Quantity: 300, NotionalUSD: 750060}}}
		if err := st.SaveBookSnapshot(ctx, b); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveWallEvent(ctx, domain.WallEvent{ID: symbol, Symbol: symbol, Side: "bid", Price: 2499.9, QualifiedAt: at, StartedAt: at.Add(-time.Minute), LastSeen: at, PeakUSD: 500000, DurationMS: 60000}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 65; i++ {
		err := st.InsertLiquidation(ctx, domain.LiquidationEvent{ID: time.Unix(int64(i), 0).String(), Exchange: "binance", Symbol: "SOLUSDT", PositionSide: "long", EventTime: at.Add(-time.Duration(i) * time.Second), ReceivedAt: at, Price: 100, Quantity: 2, NotionalUSD: 200, Coverage: "sampled"})
		if err != nil {
			t.Fatal(err)
		}
	}
	web := httptest.NewServer(srv.http.Handler)
	defer web.Close()
	command := exec.Command("python", script, web.URL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser check: %v\n%s", err, output)
	}
	t.Log(string(output))
}
