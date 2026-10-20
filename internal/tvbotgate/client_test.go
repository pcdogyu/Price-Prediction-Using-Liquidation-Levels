package gate

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSignsGateV4Request(t *testing.T) {
	const body = `{"contract":"BTC_USDT","size":"2","price":"0","tif":"ioc"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/futures/usdt/orders" || r.URL.RawQuery != "foo=bar" {
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		gotBody, _ := io.ReadAll(r.Body)
		if string(gotBody) != body {
			t.Fatalf("body = %q", gotBody)
		}
		bodyHash := sha512.Sum512(gotBody)
		timestamp := "1700000000"
		expectedText := strings.Join([]string{"POST", "/api/v4/futures/usdt/orders", "foo=bar", hex.EncodeToString(bodyHash[:]), timestamp}, "\n")
		mac := hmac.New(sha512.New, []byte("secret"))
		_, _ = mac.Write([]byte(expectedText))
		if r.Header.Get("KEY") != "key" || r.Header.Get("Timestamp") != timestamp || r.Header.Get("SIGN") != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("signature headers do not match: %#v", r.Header)
		}
		_, _ = w.Write([]byte(`{"id":123}`))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client(), Now: func() time.Time { return time.Unix(1700000000, 0) }}
	var out Order
	err := client.Do(context.Background(), http.MethodPost, "/futures/usdt/orders", map[string][]string{"foo": {"bar"}}, json.RawMessage(body), true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.IDString() != "123" {
		t.Fatalf("order id = %q", out.IDString())
	}
}

func TestClientDecodesNumericPositionFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/positions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":2,"leverage":5,"entry_price":67123.45,"mark_price":67200.1,"unrealised_pnl":12.5,"mode":"single","pos_margin_mode":"cross","lever":5}]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	positions, err := client.Positions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("positions = %#v", positions)
	}
	got := positions[0]
	if got.Contract != "BTC_USDT" || got.Size != "2" || got.Leverage != "5" || got.EntryPrice != "67123.45" || got.MarkPrice != "67200.1" || got.UnrealisedPnl != "12.5" || got.MarginMode != "cross" || got.Lever != "5" {
		t.Fatalf("decoded position = %#v", got)
	}
}

func TestClientDecodesNumericOpenOrderFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/futures/usdt/orders" || r.URL.Query().Get("status") != "open" {
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"id":123,"contract":"BTC_USDT","size":-8,"left":-8,"price":83065.8,"fill_price":0,"status":"open","finish_as":"-","text":"t-test","is_reduce_only":false,"is_close":false,"create_time":1761027807,"update_time":1761027808}]`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Credentials: Credentials{APIKey: "key", SecretKey: "secret"}, HTTPClient: server.Client()}
	orders, err := client.OpenOrders(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %#v", orders)
	}
	got := orders[0]
	if got.IDString() != "123" || got.Contract != "BTC_USDT" || got.Size != "-8" || got.Left != "-8" || got.Price != "83065.8" || got.FillPrice != "0" || got.CreateTime != "1761027807" || got.UpdateTime != "1761027808" {
		t.Fatalf("decoded order = %#v", got)
	}
}
