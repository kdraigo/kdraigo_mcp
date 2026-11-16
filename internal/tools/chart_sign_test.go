package tools

import (
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// Every request a chart tool sends is signed as the platform checks it: over
// the path after the gateway's prefix, the query as the server parses it, and
// the body. chart_remove all=true shipped in v1.35.0 with its query written
// into the path: signed that way, every call answered 401 on production, and
// no test saw it because the stand-in servers did not check signatures. This
// one does, for each tool.
func TestChartRequestsAreSignedAsTheServerChecks(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := auth.NewSigner("key-1", hex.EncodeToString(priv))
	require.NoError(t, err)

	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// What research_service is handed: the gateway strips the service's prefix.
		path := strings.TrimPrefix(r.URL.Path, string(client.Research))
		want := signer.SignV2(r.Method, path, r.URL.Query(), r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), body)
		if r.Header.Get("X-Signature") != want {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid signature"}`))
			return
		}
		mu.Lock()
		seen[r.Method+" "+path+map[bool]string{true: "?" + r.URL.RawQuery, false: ""}[r.URL.RawQuery != ""]]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.RawQuery != "":
			_, _ = w.Write([]byte(`{"removed":3}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(path, "/drawings"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"drawings":[{"id":"ai_1","body":{"kind":"horizontal-line"}}],"listening":0}`))
		case strings.HasSuffix(path, "/chart/snapshots"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"0b9c2f6e-1111-2222-3333-444455556666","title":"t","venue":"binance_futures","pair":"BTCUSDT","timeframe":"1h"}`))
		case strings.HasSuffix(path, "/chart/drawing-types"):
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{"sharing":false,"reason":"off","drawings":[],"delivered":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	s := server.NewMCPServer("test", "0")
	Register(s, Deps{HTTP: client.NewHTTP(srv.URL, srv.URL, signer)})

	market := map[string]any{"venue": "binance_futures", "pair": "BTC/USDT"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range market {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"chart_view", nil},
		{"chart_drawing_types", nil},
		{"chart_draw", with(map[string]any{"drawings": []any{map[string]any{"kind": "horizontal-line", "points": []any{map[string]any{"price": 1}}}}})},
		{"chart_remove", with(map[string]any{"ids": []any{"ai_1"}})},
		{"chart_remove", with(map[string]any{"all": true})},
		{"chart_focus", map[string]any{"timeframe": "4h"}},
		{"chart_replay", map[string]any{"action": "exit"}},
		{"chart_snapshot", map[string]any{"title": "t"}},
	} {
		out, isErr := call(t, s, c.tool, c.args)
		assert.False(t, isErr, "%s %v: %s", c.tool, c.args, out)
		assert.NotContains(t, out, "invalid signature", c.tool)
	}
	assert.Equal(t, 1, seen["DELETE /api/v1/dev/charts/binance_futures/BTCUSDT/drawings?author=ai"], "remove all reached the server: %v", seen)
	assert.Equal(t, 1, seen["DELETE /api/v1/dev/charts/binance_futures/BTCUSDT/drawings/ai_1"])

	// A query written into a path is refused before it is sent: it can never be signed right.
	d := Deps{HTTP: client.NewHTTP(srv.URL, srv.URL, signer)}
	err = d.sendJSON(t.Context(), "x", http.MethodDelete, client.Research, "/api/v1/dev/charts/a/B/drawings?author=ai", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the query is in the path")
}
