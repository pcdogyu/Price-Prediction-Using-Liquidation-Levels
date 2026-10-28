package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedBinancePendingOrdersCoalescesConcurrentFetches(t *testing.T) {
	key := "test-pending-cache-" + time.Now().Format("150405.000000000")
	var calls atomic.Int32
	fetch := func() (pendingOrdersResponse, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return pendingOrdersResponse{OK: true, TotalCount: 1, RefreshedAt: time.Now().UTC(), Orders: []pendingOrderView{{OrderGroup: "normal"}}}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := cachedBinancePendingOrders(context.Background(), key, time.Now().UTC(), false, fetch)
			if err != nil || value.TotalCount != 1 || len(value.Orders) != 1 {
				t.Errorf("cached response=%+v err=%v", value, err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch call count=%d, want 1", got)
	}
	if _, err := cachedBinancePendingOrders(context.Background(), key, time.Now().UTC(), false, fetch); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fresh cache fetch call count=%d, want 1", got)
	}
	if _, err := cachedBinancePendingOrders(context.Background(), key, time.Now().UTC(), true, fetch); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("forced fetch call count=%d, want 2", got)
	}
}
