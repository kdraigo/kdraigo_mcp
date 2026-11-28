package tools

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/orders"
)

const orderPlans = "/research/api/v1/dev/orders/plans"

// remoteBackend is the local server with executor: remote, kdraigo faked by
// routes.
func remoteBackend(t *testing.T, routes map[string]http.HandlerFunc) *server.MCPServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := auth.NewSigner("key-1", hex.EncodeToString(priv))
	require.NoError(t, err)
	h := client.NewHTTP(srv.URL, srv.URL, signer)
	s := server.NewMCPServer("test", "0")
	Register(s, Deps{HTTP: h, Orders: &AIOrders{Path: "/home/u/.kdraigo/orders.yaml", Remote: true, Manager: "om-bots-1", Server: orders.HTTPServer{HTTP: h}}})
	return s
}

// With executor: remote, order_propose asks the order manager through
// kdraigo and shows the order it built.
func TestOrders_RemoteProposes(t *testing.T) {
	var got map[string]any
	tk := paperTicket()
	s := remoteBackend(t, map[string]http.HandlerFunc{
		orderPlans: recordBody(&got, http.StatusOK, map[string]any{"id": "rq-1", "executor": "om-bots-1", "status": "proposed",
			"result":   map[string]any{"proposal": "p-1"},
			"proposal": map[string]any{"id": "p-1", "kind": "open", "status": "pending", "ticket": tk, "executor": "om-bots-1", "expires_at": "2026-11-28T15:05:00Z"}}),
	})
	out, isErr := call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "market", "venue": "binance_futures", "pair": "bchusdt", "risk": 1})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]any{"venue": "binance_futures", "pair": "BCHUSDT", "drawing_id": "ai_1", "entry_type": "market", "risk": 1.0, "manager": "om-bots-1"}, got)
	assert.Contains(t, out, "LONG BTCUSDT on binance_futures")
	assert.Contains(t, out, "built by your order manager, waits for the user's yes")
	assert.Contains(t, out, "15:05 UTC")
	assert.Contains(t, out, "also while this computer is closed")
}

func TestOrders_RemoteRefused(t *testing.T) {
	tk := paperTicket()
	tk.OK = false
	s := remoteBackend(t, map[string]http.HandlerFunc{
		orderPlans: jsonReply(map[string]any{"id": "rq-1", "status": "refused",
			"result": map[string]any{"error": "the order did not pass its checks, so nothing was proposed:\nreward to risk (1.20R, at least 1.50R)", "ticket": tk}}),
	})
	out, isErr := call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "market"})
	assert.True(t, isErr)
	assert.Contains(t, out, "Your order manager refused it")
	assert.Contains(t, out, "reward to risk")
	assert.Contains(t, out, "LONG BTCUSDT", "with the ticket it built")
}

func TestOrders_RemoteOffline(t *testing.T) {
	s := remoteBackend(t, map[string]http.HandlerFunc{
		orderPlans: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"the kdraigo tool on your computer is not connected: no order manager of yours is connected"}`))
		},
	})
	out, isErr := call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "market"})
	assert.True(t, isErr)
	assert.Contains(t, out, "no order manager of yours is connected")
}

func TestOrders_RemoteSetupAndActions(t *testing.T) {
	var got map[string]any
	s := remoteBackend(t, map[string]http.HandlerFunc{
		"/research/api/v1/dev/orders/actions": recordBody(&got, http.StatusOK, map[string]any{"id": "rq-2", "status": "refused",
			"result": map[string]any{"error": "no position is open on binance_futures BCHUSDT: nothing to close"}}),
		"/research/api/v1/dev/chart/view": jsonReply(map[string]any{"view": map[string]any{"venue": "binance_futures", "pair": "BCHUSDT"}}),
	})
	out, isErr := call(t, s, "order_setup", nil)
	require.False(t, isErr, out)
	assert.Contains(t, out, "go to your order manager (om-bots-1)")
	assert.Contains(t, out, "This computer holds no exchange key")
	assert.Contains(t, out, "order_close, order_cancel, order_move_stop and order_move_target")

	out, isErr = call(t, s, "order_close", map[string]any{"pair": "bchusdt"})
	assert.True(t, isErr)
	assert.Contains(t, out, "nothing to close")
	assert.Equal(t, "close", got["kind"])
	assert.Equal(t, "BCHUSDT", got["pair"])
	assert.Equal(t, "binance_futures", got["venue"], "the shared chart's venue")
	assert.Equal(t, "om-bots-1", got["manager"])

	out, isErr = call(t, s, "order_positions", nil)
	assert.True(t, isErr)
	assert.Contains(t, out, "next release")
}

func TestConfig_Remote(t *testing.T) {
	c := &orders.Config{Executor: "remote"}
	assert.Empty(t, c.Problems(), "no key and no limits here: they are the order manager's")
	assert.True(t, c.Remote())
	c.Executor = "cloud"
	assert.Contains(t, c.Problems()[0], `executor "cloud"`)
}
