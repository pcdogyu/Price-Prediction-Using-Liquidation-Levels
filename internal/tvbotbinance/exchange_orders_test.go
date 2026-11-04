package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestClientAllOrdersSignsAndPaginatesRequest(t *testing.T) {
	const secret = "unit-secret"
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	from := now.Add(-time.Hour)
	to := now
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fapi/v1/allOrders" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-MBX-APIKEY") != "unit-key" {
			t.Fatal("missing API key")
		}
		q := r.URL.Query()
		if q.Get("symbol") != "ETHUSDT" || q.Get("orderId") != "120" || q.Get("startTime") != strconv.FormatInt(from.UnixMilli(), 10) || q.Get("endTime") != strconv.FormatInt(to.UnixMilli(), 10) || q.Get("limit") != "1000" {
			t.Fatalf("bad all orders query: %s", r.URL.RawQuery)
		}
		payload, gotSig := splitSignatureTail(t, r.URL.RawQuery)
		if gotSig != sign(payload, secret) || payload != qWithoutSignature(q).Encode() {
			t.Fatalf("invalid signature payload=%s sig=%s", payload, gotSig)
		}
		_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","orderId":121,"clientOrderId":"client-121","status":"FILLED","origQty":"1","executedQty":"1","time":1793761200000}]`))
	}))
	defer ts.Close()

	client := Client{
		BaseURL:     ts.URL,
		Credentials: Credentials{APIKey: "unit-key", SecretKey: secret},
		HTTPClient:  ts.Client(),
		Now:         func() time.Time { return now },
	}
	orders, err := client.AllOrders(context.Background(), "ethusdt", 120, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].OrderID != 121 || orders[0].Status != "FILLED" || orders[0].ExecutedQty != "1" {
		t.Fatalf("decoded orders=%#v", orders)
	}
}
