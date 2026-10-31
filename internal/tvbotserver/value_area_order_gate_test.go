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
	cases := []struct {
		name   string
		minute int
		want   string
	}{
		{name: "before window", minute: 19*60 + 59, want: "value_area_time_window"},
		{name: "hour boundary", minute: 20 * 60, want: "value_area_minute_block"},
		{name: "hour boundary end", minute: 20*60 + 5, want: "value_area_minute_block"},
		{name: "first allowed minute", minute: 20*60 + 6},
		{name: "half hour start", minute: 20*60 + 25, want: "value_area_minute_block"},
		{name: "half hour end", minute: 20*60 + 35, want: "value_area_minute_block"},
		{name: "after half hour", minute: 20*60 + 36},
		{name: "last allowed minute", minute: 23*60 + 54},
		{name: "next hour boundary", minute: 23*60 + 55, want: "value_area_minute_block"},
		{name: "midnight", minute: 24 * 60, want: "value_area_time_window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, time.October, 31, 0, 0, 0, 0, location).Add(time.Duration(tc.minute) * time.Minute)
			signal := tvbottrading.Signal{
				SentAt:         at.UTC().Format(time.RFC3339Nano),
				PositionEffect: tvbottrading.PositionEffectOpen,
				Condition:      "value_area_signal",
			}
			if got := valueAreaOpenTimeRejection(signal); got != tc.want {
				t.Fatalf("rejection=%q want=%q local=%s", got, tc.want, at.Format("15:04"))
			}
		})
	}
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
	sentAt := time.Date(2026, time.October, 31, 20, 5, 0, 0, local)
	srv.Now = func() time.Time { return sentAt.Add(time.Minute).UTC() }
	srv.valueAreaOpenConflictChecker = func(context.Context, tvbottrading.Signal, tvbotconfig.Config) (bool, error) {
		return false, nil
	}
	signal := valueAreaWebhookSignal(t, srv, sentAt, "value_area_signal")
	rr := postValueAreaTVOrder(t, srv, signal)
	if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"reason":"value_area_minute_block"`) {
		t.Fatalf("blocked response status=%d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeTVOrderSignalResponse(t, rr.Body.Bytes())
	record, ok := srv.Orders.Get(resp.SignalID)
	if !ok || record.Status != tvbotstorage.StatusIgnored || record.ErrorCode != "value_area_minute_block" {
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
	allowedAt := time.Date(2026, time.October, 31, 20, 6, 0, 0, local)
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

	manualAt := time.Date(2026, time.October, 31, 20, 5, 0, 0, local)
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
