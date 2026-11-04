package gate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientOrdersTimerangeUsesOffsetPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/orders_timerange" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("contract") != "ETH_USDT" || q.Get("from") != "1793761200" || q.Get("to") != "1793764800" || q.Get("limit") != "1000" || q.Get("offset") != "100" {
			t.Fatalf("bad timerange query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"id":123,"id_string":"123","contract":"ETH_USDT","size":8,"left":2,"price":2200,"fill_price":2201,"status":"finished","finish_as":"filled","text":"client-1","tif":"gtc","create_time":1793761200,"update_time":1793764800,"finish_time":1793764900,"order_value":"17600","fill_price":"2201"}]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	orders, err := client.OrdersTimerange(context.Background(), "ETH_USDT", time.Unix(1793761200, 0).UTC(), time.Unix(1793764800, 0).UTC(), 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].IDString() != "123" || orders[0].Status != "finished" || orders[0].FinishAs != "filled" || orders[0].FinishTime != "1793764900" || orders[0].TIF != "gtc" {
		t.Fatalf("decoded orders=%#v", orders)
	}
}
