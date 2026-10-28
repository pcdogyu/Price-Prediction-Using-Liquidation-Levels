package server

import (
	"context"
	"sync"
	"time"
)

const binancePendingOrdersCacheTTL = 15 * time.Second

type binancePendingOrderCacheItem struct {
	fetchedAt time.Time
	value     pendingOrdersResponse
	loading   chan struct{}
}

var binancePendingOrderCache = struct {
	sync.Mutex
	items map[string]binancePendingOrderCacheItem
}{items: make(map[string]binancePendingOrderCacheItem)}

// cachedBinancePendingOrders coalesces the two upstream order requests and
// the per-order market metadata requests generated while building the view.
// The key includes the credential fingerprint through binanceUserStreamID, so
// tenants cannot observe one another's account data.
func cachedBinancePendingOrders(ctx context.Context, key string, now time.Time, force bool, fetch func() (pendingOrdersResponse, error)) (pendingOrdersResponse, error) {
	for {
		binancePendingOrderCache.Lock()
		item := binancePendingOrderCache.items[key]
		if !force && item.loading == nil && !item.fetchedAt.IsZero() && now.Sub(item.fetchedAt) < binancePendingOrdersCacheTTL {
			value := clonePendingOrdersResponse(item.value)
			binancePendingOrderCache.Unlock()
			return value, nil
		}
		if item.loading != nil {
			loading := item.loading
			binancePendingOrderCache.Unlock()
			select {
			case <-ctx.Done():
				return pendingOrdersResponse{}, ctx.Err()
			case <-loading:
				force = false
				now = time.Now().UTC()
				continue
			}
		}
		item.loading = make(chan struct{})
		binancePendingOrderCache.items[key] = item
		binancePendingOrderCache.Unlock()

		value, err := fetch()
		binancePendingOrderCache.Lock()
		item = binancePendingOrderCache.items[key]
		if err == nil {
			item.fetchedAt = time.Now().UTC()
			item.value = clonePendingOrdersResponse(value)
		} else if !item.fetchedAt.IsZero() {
			// Keep the last usable snapshot during a temporary upstream error.
			err = nil
			value = clonePendingOrdersResponse(item.value)
		}
		close(item.loading)
		item.loading = nil
		binancePendingOrderCache.items[key] = item
		binancePendingOrderCache.Unlock()
		return clonePendingOrdersResponse(value), err
	}
}

func clonePendingOrdersResponse(value pendingOrdersResponse) pendingOrdersResponse {
	value.Orders = append([]pendingOrderView(nil), value.Orders...)
	value.AlgoOrders = append([]pendingOrderView(nil), value.AlgoOrders...)
	value.RefreshedAt = value.RefreshedAt.UTC()
	return value
}
