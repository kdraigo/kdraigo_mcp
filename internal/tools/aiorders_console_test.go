package tools

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// consoleOwned asks live_trades with one signed heartbeat whose the shared
// console session is (bug #65): taken → this account's; "belongs to another
// user" → another account's on this computer; no answer → not known yet.
func TestConsoleOwned(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := auth.NewSigner("key-1", hex.EncodeToString(priv))
	require.NoError(t, err)
	var status int
	var body string
	var got struct {
		SessionID string `json:"session_id"`
		EventType string `json:"event_type"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/live/api/v1/telemetry", r.URL.Path, "live_trades through the gateway")
		assert.NotEmpty(t, r.Header.Get("X-Signature"), "signed with the account's key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	h := client.NewHTTP(srv.URL, srv.URL, signer)
	ctx := context.Background()

	status, body = http.StatusNoContent, ""
	owned, err := consoleOwned(ctx, h, "s-1")
	require.NoError(t, err)
	assert.True(t, owned)
	assert.Equal(t, "s-1", got.SessionID)
	assert.Equal(t, "heartbeat", got.EventType)

	status, body = http.StatusForbidden, `{"error":"session belongs to another user"}`
	owned, err = consoleOwned(ctx, h, "s-1")
	require.NoError(t, err)
	assert.False(t, owned, "another account's session")

	status, body = http.StatusNotFound, `{"error":"not found"}`
	owned, err = consoleOwned(ctx, h, "s-1")
	require.NoError(t, err)
	assert.True(t, owned, "a session nobody has: this account's")

	status, body = http.StatusServiceUnavailable, `{"error":"the API key could not be checked right now"}`
	_, err = consoleOwned(ctx, h, "s-1")
	assert.Error(t, err, "no answer about the session: not known yet")
}
