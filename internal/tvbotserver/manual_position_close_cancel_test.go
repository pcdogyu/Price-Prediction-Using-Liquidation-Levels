package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	gate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	okx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func TestCancelAllGateContractOrdersCancelsOnlyRequestedContract(t *testing.T) {
	var mu sync.Mutex
	btcOpen := true
	var canceled []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/futures/usdt/orders":
			mu.Lock()
			open := btcOpen
			mu.Unlock()
			if open {
				_, _ = w.Write([]byte(`[{"id":101,"contract":"BTC_USDT","size":1,"left":1,"status":"open"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v4/futures/usdt/orders/101":
			mu.Lock()
			btcOpen = false
			canceled = append(canceled, r.URL.Query().Get("contract"))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":101,"contract":"BTC_USDT","status":"finished"}`))
		default:
			t.Fatalf("unexpected Gate request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := gate.Client{
		BaseURL:     server.URL,
		Credentials: gate.Credentials{APIKey: "key", SecretKey: "secret"},
		HTTPClient:  server.Client(),
	}
	if err := cancelAllGateContractOrders(context.Background(), client, "btc_usdt"); err != nil {
		t.Fatalf("cancel Gate orders: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(canceled) != 1 || canceled[0] != "BTC_USDT" {
		t.Fatalf("unexpected Gate cancellations: %#v", canceled)
	}
}

func TestCancelAllOKXInstrumentOrdersCancelsNormalAndAlgorithmOrders(t *testing.T) {
	var mu sync.Mutex
	normal := map[string]bool{"entry-1": true, "other-1": true}
	algoOpen := true
	var canceledNormal []string
	var canceledAlgo []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeEnvelope := func(data string) {
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":` + data + `}`))
		}
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			mu.Lock()
			rows := "["
			if normal["entry-1"] {
				rows += `{"instId":"BTC-USDT-SWAP","ordId":"entry-1","clOrdId":"entry-client","state":"live"},`
			}
			if normal["other-1"] {
				rows += `{"instId":"ETH-USDT-SWAP","ordId":"other-1","clOrdId":"other-client","state":"live"},`
			}
			mu.Unlock()
			if rows[len(rows)-1] == ',' {
				rows = rows[:len(rows)-1]
			}
			writeEnvelope(rows + "]")
		case "/api/v5/trade/cancel-order":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			normal[body["ordId"]] = false
			canceledNormal = append(canceledNormal, body["ordId"])
			mu.Unlock()
			writeEnvelope(`[{"ordId":"` + body["ordId"] + `","sCode":"0","sMsg":""}]`)
		case "/api/v5/trade/orders-algo-pending":
			mu.Lock()
			open := algoOpen
			mu.Unlock()
			if open && r.URL.Query().Get("ordType") == "conditional" {
				writeEnvelope(`[{"instId":"BTC-USDT-SWAP","algoId":"algo-1","ordType":"conditional","state":"live"}]`)
				return
			}
			writeEnvelope(`[]`)
		case "/api/v5/trade/cancel-algos":
			var body []map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			algoOpen = false
			for _, item := range body {
				canceledAlgo = append(canceledAlgo, item["algoId"])
			}
			mu.Unlock()
			writeEnvelope(`[{"algoId":"algo-1","sCode":"0","sMsg":""}]`)
		default:
			t.Fatalf("unexpected OKX request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := okx.Client{
		BaseURL:     server.URL,
		Credentials: okx.Credentials{APIKey: "key", SecretKey: "secret", Passphrase: "pass"},
		HTTPClient:  server.Client(),
	}
	if err := cancelAllOKXInstrumentOrders(context.Background(), client, "btc-usdt-swap"); err != nil {
		t.Fatalf("cancel OKX orders: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(canceledNormal) != 1 || canceledNormal[0] != "entry-1" {
		t.Fatalf("unexpected OKX normal cancellations: %#v", canceledNormal)
	}
	if len(canceledAlgo) != 1 || canceledAlgo[0] != "algo-1" {
		t.Fatalf("unexpected OKX algorithm cancellations: %#v", canceledAlgo)
	}
	if normal["other-1"] == false {
		t.Fatal("cancellation must not touch another OKX instrument")
	}
}
