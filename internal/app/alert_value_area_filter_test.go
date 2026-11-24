package app

import (
	"testing"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alert"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumeprofile"
)

func TestApplyReplayValueAreaRequiresFreshCompleteAsOfSnapshot(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	session, err := volumeprofile.SessionAt(at)
	if err != nil {
		t.Fatal(err)
	}
	valid := domain.VolumeProfileSnapshot{
		Symbol: "BTCUSDT", Time: at.Add(-5 * time.Minute), SessionStart: session.Start,
		VAL: 99, VAH: 101, Complete: true,
	}
	tests := []struct {
		name      string
		snapshot  domain.VolumeProfileSnapshot
		wantReady bool
	}{
		{name: "fresh as-of snapshot", snapshot: valid, wantReady: true},
		{name: "future snapshot", snapshot: func() domain.VolumeProfileSnapshot { row := valid; row.Time = at.Add(time.Second); return row }()},
		{name: "stale snapshot", snapshot: func() domain.VolumeProfileSnapshot {
			row := valid
			row.Time = at.Add(-valueAreaProfileMaxAge - time.Second)
			return row
		}()},
		{name: "incomplete snapshot", snapshot: func() domain.VolumeProfileSnapshot { row := valid; row.Complete = false; return row }()},
		{name: "different session", snapshot: func() domain.VolumeProfileSnapshot {
			row := valid
			row.SessionStart = row.SessionStart.Add(-24 * time.Hour)
			return row
		}()},
		{name: "invalid bounds", snapshot: func() domain.VolumeProfileSnapshot { row := valid; row.VAL, row.VAH = 102, 101; return row }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyReplayValueArea(alert.MarketContext{Ready: true}, []domain.VolumeProfileSnapshot{tc.snapshot}, at)
			if got.ValueAreaReady != tc.wantReady {
				t.Fatalf("ValueAreaReady=%v, want %v: %+v", got.ValueAreaReady, tc.wantReady, got)
			}
			if tc.wantReady && (got.ValueAreaVAL != 99 || got.ValueAreaVAH != 101) {
				t.Fatalf("applied levels = %.2f/%.2f, want 99/101", got.ValueAreaVAL, got.ValueAreaVAH)
			}
		})
	}
	if got := applyReplayValueArea(alert.MarketContext{Ready: true}, nil, at); got.ValueAreaReady {
		t.Fatalf("missing snapshot unexpectedly passed: %+v", got)
	}
}

func TestVolumeProfileFreshAtUsesSourceTimestamp(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		data time.Time
		want bool
	}{
		{name: "recent", data: at.Add(-time.Minute), want: true},
		{name: "stale", data: at.Add(-valueAreaProfileMaxAge - time.Second)},
		{name: "missing", data: time.Time{}},
		{name: "future skew", data: at.Add(6 * time.Second)},
		{name: "small clock skew", data: at.Add(3 * time.Second), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := volumeProfileFreshAt(domain.VolumeProfile{DataThrough: tc.data}, at)
			if got != tc.want {
				t.Fatalf("fresh=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestValueAreaBlockedSignalStaysInHistoryWithoutAutomaticMessages(t *testing.T) {
	rtm := &alertRuntime{}
	signal := domain.TradeSignal{
		Symbol: "BTCUSDT", Kind: domain.SignalSteady, Side: "long", FilterApplied: true,
		OpenAllowed: false, CloseAllowed: true, FilterTags: []string{"value_area"},
	}
	status, message := rtm.submitSignal(signal, true)
	if status != "filtered" || message == "" {
		t.Fatalf("blocked signal notification status = %q, %q", status, message)
	}
	// This must return before touching the webhook dispatcher; the signal remains
	// persisted for history, while neither the entry webhook nor chart notification
	// is queued for a rejected opening.
	rtm.submitSignalWebhook(signal, true)
}
