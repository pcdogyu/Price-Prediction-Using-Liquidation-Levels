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

func TestOpenWithOptionsDatabaseRoutesGammaWrites(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "liquidation.db")
	optionsPath := filepath.Join(dir, "options.db")
	st, err := OpenWithMarketAndAlertsAndOptionsDatabase(mainPath, "", "", optionsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.SaveGamma(ctx, domain.GammaView{Symbol: "BTCUSDT", State: "ok"}); err != nil {
		t.Fatal(err)
	}
	value := 0.25
	if err := st.SaveOptionGamma(ctx, domain.OptionGammaPoint{Symbol: "BTCUSDT", Underlying: "BTC", Source: "deribit", Time: time.UnixMilli(1_700_000_000_000).UTC(), Gamma: &value, Contracts: 80, State: "ok"}); err != nil {
		t.Fatal(err)
	}

	mainDB, err := sql.Open("sqlite", mainPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()
	optionsDB, err := sql.Open("sqlite", optionsPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer optionsDB.Close()
	for _, table := range []string{"gamma_latest", "deribit_gamma_history"} {
		var mainCount, optionsCount int
		if err := mainDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&mainCount); err != nil {
			t.Fatal(err)
		}
		if err := optionsDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&optionsCount); err != nil {
			t.Fatal(err)
		}
		if mainCount != 0 || optionsCount != 1 {
			t.Fatalf("table %s main=%d options=%d", table, mainCount, optionsCount)
		}
	}
	var authTables int
	if err := mainDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_users'").Scan(&authTables); err != nil {
		t.Fatal(err)
	}
	if authTables != 1 {
		t.Fatal("unified database schema must remain available")
	}
}
