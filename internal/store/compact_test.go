package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

// fillExpiredGamma writes padded samples past the rolling window so the daily
// retention has real pages to free, which is what compaction measures against.
func fillExpiredGamma(t *testing.T, s *Store, at time.Time, count int) {
	t.Helper()
	payload := `{"symbol":"BTCUSDT","underlying":"BTC","source":"deribit","gamma":0.5,"pad":"` + strings.Repeat("x", 2000) + `"}`
	for i := 0; i < count; i++ {
		ts := at.Add(-time.Duration(i) * time.Second).UnixMilli()
		if _, err := s.db.ExecContext(context.Background(), `INSERT INTO deribit_gamma_history(symbol,ts,payload) VALUES(?,?,?)`, "BTCUSDT", ts, payload); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompactReclaimsFreedPagesOnlyAboveThreshold(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "compact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	fillExpiredGamma(t, st, now.Add(-4*24*time.Hour), 900)
	if err = st.PruneOptionGamma(ctx, now); err != nil {
		t.Fatal(err)
	}
	pageSize, pageCount, freePages, err := st.pageStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if freePages*pageSize < 64<<10 {
		t.Fatalf("fixture freed too little space: %d bytes", freePages*pageSize)
	}
	// Below the threshold the maintenance window must not rewrite the database.
	report, err := st.Compact(ctx, 1<<30)
	if err != nil || report.Compacted || report.FreeBytes <= 0 {
		t.Fatalf("%+v err=%v", report, err)
	}
	report, err = st.Compact(ctx, 1)
	if err != nil || !report.Compacted {
		t.Fatalf("%+v err=%v", report, err)
	}
	if report.BytesAfter >= report.BytesBefore {
		t.Fatalf("compaction did not shrink the file: %+v", report)
	}
	_, pageCountAfter, freePagesAfter, err := st.pageStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.BytesBefore != pageCount*pageSize || report.BytesAfter != pageCountAfter*pageSize || freePagesAfter != 0 {
		t.Fatalf("reported sizes do not match the page statistics: %+v pages=%d/%d free=%d", report, pageCount, pageCountAfter, freePagesAfter)
	}
	// The database stays usable after the rewrite.
	value := 0.25
	if err = st.SaveOptionGamma(ctx, domain.OptionGammaPoint{Symbol: "BTCUSDT", Underlying: "BTC", Source: "deribit", Time: now.Add(-time.Minute), Gamma: &value, Contracts: 80, SelectedContracts: 80, EligibleContracts: 100, State: "ok"}); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LatestOptionGamma(ctx, "BTCUSDT")
	if err != nil || latest.Gamma == nil || *latest.Gamma != 0.25 {
		t.Fatalf("%+v err=%v", latest, err)
	}
}