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
