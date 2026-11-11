package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	_ "modernc.org/sqlite"
)

func TestOpenWithMarketDatabaseRoutesMarketWrites(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	marketPath := filepath.Join(dir, "market.db")
	st, err := OpenWithMarketDatabase(mainPath, marketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.UnixMilli(1_700_000_000_000).UTC()
	if err := st.UpsertCandles(context.Background(), []domain.Candle{{Exchange: "binance", Symbol: "BTCUSDT", Time: now, Open: 100, High: 101, Low: 99, Close: 100.5}}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertLiquidation(context.Background(), domain.LiquidationEvent{ID: "liq-1", Exchange: "binance", Symbol: "BTCUSDT", PositionSide: "long", EventTime: now, ReceivedAt: now, Price: 100, Quantity: 1, NotionalUSD: 100, Coverage: "test"}); err != nil {
		t.Fatal(err)
	}

	mainDB, err := sql.Open("sqlite", mainPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	marketDB, err := sql.Open("sqlite", marketPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer marketDB.Close()

	for _, table := range []string{"candles", "liquidations"} {
		var mainCount, marketCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := marketDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&marketCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || marketCount != 1 {
			t.Fatalf("table %s main=%d market=%d", table, mainCount, marketCount)
		}
	}
}
