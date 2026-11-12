package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

func TestValueAreaOpenTimeRejectionUsesChinaOpeningTime(t *testing.T) {
	location, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		t.Fatal(err)
	}
	fullDayStart, fullDayEnd := 0, 24*60
	customStart, customEnd := 9*60, 17*60
	cases := []struct {
		name       string
		minute     int
		start, end *int
		want       string
	}{
		{name: "legacy before window", minute: 19*60 + 59, want: "value_area_time_window"},
		{name: "legacy opening boundary", minute: 20 * 60},
		{name: "configured opening boundary", minute: 20 * 60, start: ptr(20 * 60), end: ptr(24 * 60)},
		{name: "configured five minutes after boundary", minute: 20*60 + 5, start: ptr(20 * 60), end: ptr(24 * 60)},
		{name: "configured half hour", minute: 20*60 + 30, start: ptr(20 * 60), end: ptr(24 * 60)},
		{name: "configured last hour boundary", minute: 23*60 + 55, start: ptr(20 * 60), end: ptr(24 * 60)},
		{name: "full day midnight", minute: 0, start: &fullDayStart, end: &fullDayEnd},
		{name: "full day midday", minute: 12 * 60, start: &fullDayStart, end: &fullDayEnd},
		{name: "full day last minute", minute: 23*60 + 59, start: &fullDayStart, end: &fullDayEnd},
		{name: "custom window before start", minute: 8*60 + 59, start: &customStart, end: &customEnd, want: "value_area_time_window"},
		{name: "custom window end exclusive", minute: 17 * 60, start: &customStart, end: &customEnd, want: "value_area_time_window"},
		{name: "partial window", minute: 12 * 60, start: &fullDayStart, want: "value_area_time_window"},
		{name: "invalid window", minute: 12 * 60, start: ptr(24 * 60), end: &fullDayEnd, want: "value_area_time_window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, time.October, 31, 0, 0, 0, 0, location).Add(time.Duration(tc.minute) * time.Minute)
			signal := tvbottrading.Signal{
				SentAt:               at.UTC().Format(time.RFC3339Nano),
				PositionEffect:       tvbottrading.PositionEffectOpen,
				Condition:            "value_area_signal",
				ValueAreaStartMinute: tc.start,
				ValueAreaEndMinute:   tc.end,
			}
			if got := valueAreaOpenTimeRejection(signal); got != tc.want {
				t.Fatalf("rejection=%q want=%q local=%s", got, tc.want, at.Format("15:04"))
			}
		})
	}
}

func ptr(value int) *int {
	return &value
}

func TestValueAreaOpenGuardOnlyTargetsAutomaticSignal(t *testing.T) {
	for _, signal := range []tvbottrading.Signal{
		{PositionEffect: tvbottrading.PositionEffectOpen, Condition: "value_area_signal"},
		{PositionEffect: tvbottrading.PositionEffectOpen, Condition: "value_area_manual"},
		{PositionEffect: tvbottrading.PositionEffectClose, Condition: "value_area_close"},
	} {
		got := isAutomaticValueAreaOpenSignal(signal)
		want := signal.Condition == "value_area_signal" && signal.PositionEffect == tvbottrading.PositionEffectOpen
		if got != want {
			t.Fatalf("signal=%#v automatic=%v want=%v", signal, got, want)
		}
	}
}

func TestValueAreaOpenConflictCheckerCanBeInjected(t *testing.T) {
	srv := &Server{}
	srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
		return true, nil
	}
	conflict, err := srv.valueAreaOpenConflict(context.Background(), tvbottrading.Signal{}, tvbotconfig.Config{})
	if err != nil || !conflict {
		t.Fatalf("conflict=%v err=%v", conflict, err)
	}
}

func TestTVOrderValueAreaGateUsesSentAtAndSkipsExecutorWhenBlocked(t *testing.T) {
	srv := newTestServer(t)
	local, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := time.Date(2026, time.October, 31, 19, 59, 0, 0, local)
	srv.Now = func() time.Time { return sentAt.Add(time.Minute).UTC() }
	srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
		return false, nil
	}
	signal := valueAreaWebhookSignal(t, srv, sentAt, "value_area_signal")
	rr := postValueAreaTVOrder(t, srv, signal)
	if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"reason":"value_area_time_window"`) {
		t.Fatalf("blocked response status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	record, ok := srv.Orders.Get(resp.SignalID)
	if !ok || record.Status != tvbotstorage.StatusIgnored || record.ErrorCode != "value_area_time_window" {
		t.Fatalf("blocked record=%#v found=%v", record, ok)
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		t.Fatalf("blocked signal reached executor: %#v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTVOrderValueAreaGateAllowsOpeningAndLeavesManualOpeningUnrestricted(t *testing.T) {
	local, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		t.Fatal(err)
	}
	allowedAt := time.Date(2026, time.October, 31, 20, 5, 0, 0, local)
	srv := newTestServer(t)
	srv.Now = func() time.Time { return allowedAt.Add(time.Second).UTC() }
	srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
		return false, nil
	}
	automatic := valueAreaWebhookSignal(t, srv, allowedAt, "value_area_signal")
	allowed := postValueAreaTVOrder(t, srv, automatic)
	if allowed.Code != http.StatusAccepted || !strings.Contains(allowed.Body.String(), `"status":"accepted"`) {
		t.Fatalf("allowed response status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Condition != "value_area_signal" || got.PositionEffect != tvbottrading.PositionEffectOpen {
			t.Fatalf("unexpected automatic signal: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed automatic signal did not reach executor")
	}

	manualAt := time.Date(2026, time.October, 31, 20, 6, 0, 0, local)
	srv.Now = func() time.Time { return manualAt.Add(time.Second).UTC() }
	manual := valueAreaWebhookSignal(t, srv, manualAt, "value_area_manual")
	manualResponse := postValueAreaTVOrder(t, srv, manual)
	if manualResponse.Code != http.StatusAccepted || !strings.Contains(manualResponse.Body.String(), `"status":"accepted"`) {
		t.Fatalf("manual response status=%d body=%s", manualResponse.Code, manualResponse.Body.String())
	}
	select {
	case got := <-srv.Executor.(fakeExecutor).calls:
		if got.Condition != "value_area_manual" {
			t.Fatalf("unexpected manual signal: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("manual signal did not reach executor")
	}
}

func TestTVOrderValueAreaGateUsesConfiguredFullDayWindow(t *testing.T) {
	local, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 31, 0, 0, 0, 0, local)
	srv := newTestServer(t)
	srv.Now = func() time.Time { return at.Add(time.Second).UTC() }
	srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
		return false, nil
	}
	signal := valueAreaWebhookSignal(t, srv, at, "value_area_signal")
	start, end := 0, 24*60
	signal.ValueAreaStartMinute = &start
	signal.ValueAreaEndMinute = &end
	rr := postValueAreaTVOrder(t, srv, signal)
	if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"status":"accepted"`) {
		t.Fatalf("full-day configured response status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTVOrderValueAreaGateConflictsAndFailsClosed(t *testing.T) {
	local, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 31, 20, 6, 0, 0, local)

	t.Run("conflict", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Now = func() time.Time { return at.Add(time.Second).UTC() }
		srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
			return true, nil
		}
		rr := postValueAreaTVOrder(t, srv, valueAreaWebhookSignal(t, srv, at, "value_area_signal"))
		if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"reason":"value_area_conflicting_strategy"`) {
			t.Fatalf("conflict response status=%d body=%s", rr.Code, rr.Body.String())
		}
		select {
		case got := <-srv.Executor.(fakeExecutor).calls:
			t.Fatalf("conflicting signal reached executor: %#v", got)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("check error", func(t *testing.T) {
		srv := newTestServer(t)
		srv.Now = func() time.Time { return at.Add(time.Second).UTC() }
		srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
			return false, errors.New("exchange state unavailable")
		}
		rr := postValueAreaTVOrder(t, srv, valueAreaWebhookSignal(t, srv, at, "value_area_signal"))
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"error":"value_area_conflict_check_failed"`) {
			t.Fatalf("check error response status=%d body=%s", rr.Code, rr.Body.String())
		}
		if records := srv.Orders.List(10); len(records) != 1 || records[0].Status != tvbotstorage.StatusRejected || records[0].ErrorCode != "value_area_conflict_check_failed" {
			t.Fatalf("check error records=%#v", records)
		}
	})
}

func TestTVOrderValueAreaGateSerializesConflictCheckAndAcceptance(t *testing.T) {
	local, err := time.LoadLocation(valueAreaOrderTimezone)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 31, 20, 6, 0, 0, local)
	srv := newTestServer(t)
	srv.Now = func() time.Time { return at.Add(2 * time.Second).UTC() }
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var checks atomic.Int32
	srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
		if checks.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return false, nil
	}
	first := valueAreaWebhookSignal(t, srv, at, "value_area_signal")
	second := valueAreaWebhookSignal(t, srv, at.Add(time.Second), "value_area_signal")
	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() { responses <- postValueAreaTVOrder(t, srv, first) }()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first conflict check did not start")
	}
	go func() { responses <- postValueAreaTVOrder(t, srv, second) }()
	select {
	case <-secondEntered:
		t.Fatal("second conflict check bypassed the account/symbol lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	for range 2 {
		select {
		case rr := <-responses:
			if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"status":"accepted"`) {
				t.Fatalf("serialized response status=%d body=%s", rr.Code, rr.Body.String())
			}
		case <-time.After(time.Second):
			t.Fatal("serialized request did not finish")
		}
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second conflict check did not start after first acceptance")
	}
}

func TestValueAreaLocalOpeningConflictUsesLiveStateAndFreshness(t *testing.T) {
	now := time.Date(2026, time.November, 6, 1, 30, 0, 0, time.UTC)
	other := func(updatedAt time.Time) tvbotstorage.OrderRecord {
		return tvbotstorage.OrderRecord{
			AcceptedAt: updatedAt,
			UpdatedAt:  updatedAt,
			RawJSON:    `{"condition":"aggressive_liquidation_signal","source":"liquidation_alert"}`,
		}
	}
	valueArea := func(updatedAt time.Time) tvbotstorage.OrderRecord {
		return tvbotstorage.OrderRecord{
			AcceptedAt: updatedAt,
			UpdatedAt:  updatedAt,
			RawJSON:    `{"condition":"value_area_signal","source":"value_area_signal"}`,
		}
	}
	cases := []struct {
		name    string
		records []tvbotstorage.OrderRecord
		state   valueAreaExchangeState
		want    bool
	}{
		{name: "stale other strategy with empty exchange", records: []tvbotstorage.OrderRecord{other(now.Add(-16 * time.Minute))}},
		{name: "fresh other strategy with empty exchange", records: []tvbotstorage.OrderRecord{other(now.Add(-5 * time.Minute))}, want: true},
		{name: "stale local record with live exchange state", records: []tvbotstorage.OrderRecord{other(now.Add(-16 * time.Minute))}, state: valueAreaExchangeState{OpenPosition: true}, want: true},
		{name: "pending entry order remains a conflict", records: []tvbotstorage.OrderRecord{valueArea(now.Add(-5 * time.Minute))}, state: valueAreaExchangeState{OpenEntryOrder: true}, want: true},
		{name: "live exchange state with no local record", state: valueAreaExchangeState{OpenPosition: true}, want: true},
		{name: "stale value area record with empty exchange", records: []tvbotstorage.OrderRecord{valueArea(now.Add(-16 * time.Minute))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valueAreaLocalOpeningConflict(tc.records, tc.state, now); got != tc.want {
				t.Fatalf("conflict=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestValueAreaImmediateReverseRequiresOppositeLivePosition(t *testing.T) {
	now := time.Date(2026, time.November, 6, 1, 30, 0, 0, time.UTC)
	signal := tvbottrading.Signal{
		Condition:      "value_area_signal",
		PositionEffect: tvbottrading.PositionEffectOpen,
		PositionSide:   tvbottrading.PositionSideLong,
	}
	cases := []struct {
		name  string
		state valueAreaExchangeState
		want  bool
	}{
		{name: "opposite short position", state: valueAreaExchangeState{OpenPosition: true, OpenPositionSides: []tvbottrading.Side{tvbottrading.ActionShort}}},
		{name: "same long position", state: valueAreaExchangeState{OpenPosition: true, OpenPositionSides: []tvbottrading.Side{tvbottrading.ActionLong}}, want: true},
		{name: "unknown position direction", state: valueAreaExchangeState{OpenPosition: true}, want: true},
		{name: "opposite position with pending entry", state: valueAreaExchangeState{OpenPosition: true, OpenEntryOrder: true, OpenPositionSides: []tvbottrading.Side{tvbottrading.ActionShort}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valueAreaLocalOpeningConflictForSignal(nil, tc.state, now, signal); got != tc.want {
				t.Fatalf("conflict=%v want=%v", got, tc.want)
			}
		})
	}

	signal.Condition = "value_area_manual"
	if got := valueAreaLocalOpeningConflictForSignal(nil, valueAreaExchangeState{OpenPosition: true, OpenPositionSides: []tvbottrading.Side{tvbottrading.ActionShort}}, now, signal); got {
		t.Fatal("manual value-area reverse should bypass the conflict guard")
	}
}

func TestValueAreaOrderRecordSourceClassification(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "value area", raw: `{"condition":"value_area_signal"}`, want: "value_area"},
		{name: "manual", raw: `{"condition":"value_area_manual"}`, want: "manual"},
		{name: "manual source", raw: `{"source":"manual"}`, want: "manual"},
		{name: "alert", raw: `{"source":"liquidation_alert"}`, want: "alert"},
		{name: "aggressive", raw: `{"condition":"aggressive_liquidation_signal"}`, want: "aggressive"},
		{name: "steady", raw: `{"condition":"steady_liquidation_confirmation"}`, want: "steady"},
		{name: "unknown", raw: `{"condition":"unclassified"}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderRecordSignalKind(tvbotstorage.OrderRecord{RawJSON: tc.raw}); got != tc.want {
				t.Fatalf("kind=%q want=%q", got, tc.want)
			}
		})
	}
}

func valueAreaWebhookSignal(t *testing.T, srv *Server, local time.Time, condition string) tvbottrading.Signal {
	t.Helper()
	signal := validSignal(t, srv)
	signal.SentAt = local.UTC().Format(time.RFC3339Nano)
	signal.Condition = condition
	signal.PositionEffect = tvbottrading.PositionEffectOpen
	signal.OrderIntent = "open"
	signal.Normalize()
	signal.Token = srv.Token.Generate(signal.CanonicalWebhookTokenPayload())
	return signal
}

func postValueAreaTVOrder(t *testing.T, srv *Server, signal tvbottrading.Signal) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/tvorder", strings.NewReader(string(body))))
	return rr
}
