package binancerest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSchedulerReservesWeightForExecution(t *testing.T) {
	s := New(8, 8, 0)
	background := Request{Host: "fapi.binance.com", Path: "/fapi/v1/klines", Class: ClassBackground, Priority: PriorityBackground}
	for i := 0; i < 6; i++ {
		release, err := s.Acquire(context.Background(), background)
		if err != nil {
			t.Fatalf("background acquire %d: %v", i, err)
		}
		release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, background); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected background budget exhaustion, got %v", err)
	}
	release, err := s.Acquire(context.Background(), Request{Host: "demo-fapi.binance.com", Path: "/fapi/v1/order", Class: ClassExecution, Priority: PriorityExecution})
	if err != nil {
		t.Fatalf("execution request should use reserved capacity: %v", err)
	}
	release()
}

func TestSchedulerLimitsConcurrencyAndHonorsCancellation(t *testing.T) {
	s := New(1200, 2, 0)
	request := Request{Host: "fapi.binance.com", Path: "/fapi/v1/account", Class: ClassAccount, Priority: PriorityAccount}
	first, err := s.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Acquire(context.Background(), request)
	if err != nil {
		first()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation while all slots are busy, got %v", err)
	}
	first()
	second()
	if got := s.Snapshot().Blocked; got == 0 {
		t.Fatal("expected a blocked request to be counted")
	}
}

func TestSchedulerPausesAcrossBinanceHostsAndParsesBanDeadline(t *testing.T) {
	s := New(1200, 4, 0)
	until, ok := ParsePauseUntil("Binance code -1003: Way too many requests; IP banned until 1793205212975")
	if !ok || until.IsZero() {
		t.Fatal("expected ban deadline to parse")
	}
	future := time.Now().UTC().Add(time.Hour)
	now := future.Add(-time.Minute)
	s.PauseUntil(future)
	for _, host := range []string{"fapi.binance.com", "demo-fapi.binance.com", "eapi.binance.com"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := s.Acquire(ctx, Request{Host: host, Path: "/fapi/v1/ping"})
		cancel()
		var pause *PauseError
		if !errors.As(err, &pause) {
			t.Fatalf("expected shared pause for %s, got %v", host, err)
		}
	}
	if paused, ok := s.PausedUntil(now); !ok || !paused.Equal(future) {
		t.Fatalf("unexpected pause state: %v %v", paused, ok)
	}
}

func TestSchedulerRecordsResponseWeightAndStatus(t *testing.T) {
	s := New(1200, 4, 0)
	request := Request{Host: "fapi.binance.com", Path: "/fapi/v1/exchangeInfo", Class: ClassBackground, Priority: PriorityBackground}
	release, err := s.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s.Complete(request, 200, 37, nil)
	snapshot := s.Snapshot()
	if len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].LastResponseWeight != 37 || snapshot.Endpoints[0].LastStatus != 200 {
		t.Fatalf("unexpected endpoint stats: %+v", snapshot.Endpoints)
	}
}
