package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTVBotUILimitCloseAddsLocalPendingOrder(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/tvbot/", nil)
	req.SetBasicAuth("admin", "Admin123")
	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("tvbot ui code=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.Bytes()
	for _, want := range [][]byte{
		[]byte("localPendingLimitCloses"),
		[]byte("pendingLimitCloseOrderCacheMs"),
		[]byte("mergeLocalPendingLimitCloseOrders"),
		[]byte("rememberLimitClosePendingOrder"),
		[]byte("_local_pending_limit_close"),
		[]byte("local_pending_count"),
		[]byte(`data-pos="`),
		[]byte(`Object.assign({}, body, { pos: pos })`),
		[]byte(`stateText === "new"`),
		[]byte("等待同步"),
		[]byte("同步中"),
		[]byte("Binance 平仓状态同步中"),
	} {
		if !bytes.Contains(body, want) {
			t.Fatalf("tvbot ui should include limit close pending order marker %q", want)
		}
	}
}
