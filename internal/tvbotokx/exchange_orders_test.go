package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestClientOrdersHistoryArchiveSignsAndUsesCursor(t *testing.T) {
	fixedNow := time.Date(2026, 11, 4, 3, 0, 0, 123000000, time.UTC)
	secret := "secret"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/trade/orders-history-archive" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("instType") != "SWAP" || q.Get("after") != "ord-0" || q.Get("limit") != "100" {
			t.Fatalf("bad archive query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("x-simulated-trading") != "1" || r.Header.Get("OK-ACCESS-KEY") != "key" {
			t.Fatal("missing OKX demo/auth headers")
		}
		timestamp := fixedNow.UTC().Format("2006-01-02T15:04:05.000Z")
		wantSign := sign(timestamp, http.MethodGet, r.URL.Path+"?"+r.URL.Query().Encode(), "", secret)
		if r.Header.Get("OK-ACCESS-TIMESTAMP") != timestamp || r.Header.Get("OK-ACCESS-SIGN") != wantSign {
			t.Fatal("invalid OKX signature")
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"ETH-USDT-SWAP","ordId":"ord-1","clOrdId":"client-1","state":"filled","side":"buy","ordType":"limit","px":"2200","sz":"2","accFillSz":"2","cTime":"1793761200000","uTime":"1793761201000"}]}`))
	}))
	defer ts.Close()

	client := Client{
		BaseURL:     ts.URL,
		Credentials: Credentials{APIKey: "key", SecretKey: secret, Passphrase: "pass"},
		Demo:        true, HTTPClient: ts.Client(), Now: func() time.Time { return fixedNow },
	}
	orders, _, err := client.OrdersHistoryArchive(context.Background(), "SWAP", "ord-0", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].OrdID != "ord-1" || orders[0].State != "filled" || orders[0].RawJSON == "" {
		t.Fatalf("decoded orders=%#v", orders)
	}
	if got := strconv.FormatInt(1793761200000, 10); orders[0].CTime != got {
		t.Fatalf("unexpected order time=%s", orders[0].CTime)
	}
}
