package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestExchangeOrdersUpsertDeduplicatesAndKeepsTenantDimensions(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	base := ExchangeOrder{
		Exchange:      "Binance",
		APIID:         "account-a",
		TradeEnv:      "LIVE",
		RemoteOrderID: "1001",
		ClientOrderID: "client-1",
		InstID:        "BTCUSDT",
		Status:        "NEW",
		Side:          "BUY",
		OriginalQty:   "1",
		ExecutedQty:   "0",
		CreatedAt:     created,
		RawJSON:       `{"status":"NEW"}`,
	}
	if got, err := store.UpsertExchangeOrders([]ExchangeOrder{base}, created); err != nil || got.Upserted != 1 {
		t.Fatalf("first upsert stats=%#v err=%v", got, err)
	}
	updated := base
	updated.Status = "FILLED"
	updated.ExecutedQty = "1"
	updated.RawJSON = `{"status":"FILLED"}`
	if got, err := store.UpsertExchangeOrders([]ExchangeOrder{updated}, created.Add(time.Minute)); err != nil || got.Upserted != 1 {
		t.Fatalf("second upsert stats=%#v err=%v", got, err)
	}

	otherAccounts := []ExchangeOrder{
		{Exchange: "okx", APIID: "account-a", TradeEnv: "live", RemoteOrderID: "1001", InstID: "BTC-USDT-SWAP", Status: "filled", CreatedAt: created},
		{Exchange: "binance", APIID: "account-b", TradeEnv: "live", RemoteOrderID: "1001", InstID: "BTCUSDT", Status: "filled", CreatedAt: created},
	}
	if got, err := store.UpsertExchangeOrders(otherAccounts, created); err != nil || got.Upserted != 2 {
		t.Fatalf("isolated upsert stats=%#v err=%v", got, err)
	}

	page, err := store.ListExchangeOrders(ExchangeOrderFilter{Exchange: "binance", APIID: "account-a", TradeEnv: "live", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Orders) != 1 || page.Orders[0].Status != "FILLED" || page.Orders[0].ExecutedQty != "1" {
		t.Fatalf("dedupe result=%#v", page)
	}

	page, err = store.ListExchangeOrders(ExchangeOrderFilter{Query: "client-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Orders[0].RemoteOrderID != "1001" {
		t.Fatalf("query result=%#v", page)
	}
	instIDs, err := store.ListExchangeOrderInstIDs("BINANCE", "account-a", "LIVE")
	if err != nil {
		t.Fatal(err)
	}
	if len(instIDs) != 1 || instIDs[0] != "BTCUSDT" {
		t.Fatalf("account instrument result=%#v", instIDs)
	}
}

func TestExchangeOrdersListPaginationAndSyncState(t *testing.T) {
	store, err := NewSQLiteOrderStore(filepath.Join(t.TempDir(), "tvbot.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	orders := make([]ExchangeOrder, 0, 3)
	for i, status := range []string{"FILLED", "CANCELED", "FILLED"} {
		orders = append(orders, ExchangeOrder{
			Exchange:      "gate",
			APIID:         "gate-a",
			TradeEnv:      "demo",
			RemoteOrderID: string(rune('1' + i)),
			InstID:        "ETH_USDT",
			Status:        status,
			CreatedAt:     created.Add(time.Duration(i) * time.Minute),
		})
	}
	if _, err := store.UpsertExchangeOrders(orders, created); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListExchangeOrders(ExchangeOrderFilter{Exchange: "gate", APIID: "gate-a", TradeEnv: "demo", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Orders) != 1 {
		t.Fatalf("pagination result=%#v", page)
	}
	filtered, err := store.ListExchangeOrders(ExchangeOrderFilter{Status: "filled", InstID: "ETH_USDT", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 2 {
		t.Fatalf("status filter result=%#v", filtered)
	}

	state := ExchangeOrderSyncState{
		Exchange:      "gate",
		APIID:         "gate-a",
		TradeEnv:      "demo",
		CursorJSON:    `{"offset":100}`,
		Status:        "synced",
		LastSuccessAt: created,
		LastReceived:  3,
		LastUpserted:  3,
		LastError:     "",
		UpdatedAt:     created,
	}
	if err := store.UpsertExchangeOrderSyncState(state); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetExchangeOrderSyncState("GATE", "gate-a", "DEMO")
	if err != nil || !ok {
		t.Fatalf("sync state ok=%v state=%#v err=%v", ok, got, err)
	}
	if got.Status != "synced" || got.CursorJSON != state.CursorJSON || got.LastReceived != 3 {
		t.Fatalf("sync state=%#v", got)
	}
}
