package client

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"nhooyr.io/websocket"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
)

func testSigner(t *testing.T) (*auth.Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	s, err := auth.NewSigner("key-1", hex.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	return s, pub
}

// verifyV2 checks a request's version 2 signature the way users_service does,
// from the request as the server receives it.
func verifyV2(t *testing.T, pub ed25519.PublicKey, r *http.Request, keyHeader, sigHeader, tsHeader string, body []byte) {
	t.Helper()
	nonce := r.Header.Get("X-Nonce")
	if len(nonce) != 32 {
		t.Fatalf("nonce %q", nonce)
	}
	q := r.URL.Query()
	for _, k := range []string{"key_id", "signature", "timestamp", "nonce"} {
		if q.Has(k) {
			t.Fatalf("credential %q is in the URL, where access logs keep it", k)
		}
	}
	sum := sha256.Sum256(body)
	canonical := strings.Join([]string{"KDRAIGO-SIG-V2", r.Method, r.URL.Path, q.Encode(),
		r.Header.Get(tsHeader), nonce, hex.EncodeToString(sum[:])}, "\n")
	sig, _ := hex.DecodeString(r.Header.Get(sigHeader))
	if r.Header.Get(keyHeader) != "key-1" || !ed25519.Verify(pub, []byte(canonical), sig) {
		t.Fatalf("signature does not verify over %q", canonical)
	}
}

func TestDialSessionWS_SignsV2InHeaders(t *testing.T) {
	signer, pub := testSigner(t)
	checked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/dev/session/ws" || r.URL.Query().Get("id") != "sess-1" {
			t.Errorf("url %s", r.URL)
		}
		verifyV2(t, pub, r, "X-API-KEY", "X-SIGNATURE", "X-TIMESTAMP", nil)
		checked <- struct{}{}
		c, err := websocket.Accept(w, r, nil)
		if err == nil {
			c.Close(websocket.StatusNormalClosure, "")
		}
	}))
	defer srv.Close()

	ws, err := DialSessionWS(context.Background(), srv.URL, signer, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	ws.conn.Close(websocket.StatusNormalClosure, "")
	<-checked
}

func TestHTTPDo_SignsTheQuery(t *testing.T) {
	signer, pub := testSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyV2(t, pub, r, "X-Key-ID", "X-Signature", "X-Timestamp", nil)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// The service prefix is stripped by the gateway, so the signed path is
	// /api/v1/dev/orders; this test server sees the prefixless path as well.
	h := NewHTTP(srv.URL, srv.URL, signer)
	_, status, err := h.Do(context.Background(), true, HeaderStyleStandard, http.MethodGet, Backtester,
		"/api/v1/dev/orders", url.Values{"session_id": {"abc"}, "limit": {"50"}}, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status %d err %v", status, err)
	}
}
