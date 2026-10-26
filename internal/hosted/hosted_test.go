package hosted

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/tools"
)

const (
	testResource = "https://mcp.kdraigo.test/mcp"
	testIssuer   = "https://mcp.kdraigo.test"
	internalTok  = "internal-secret"
)

// fakeUsers is users_service's internal port: introspection and minting.
type fakeUsers struct {
	mu          sync.Mutex
	introspects int
	mints       []map[string]any
	revoked     bool
	user, grant uuid.UUID
	keyID       uuid.UUID
}

func (f *fakeUsers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Internal-Token") != internalTok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/internal/oauth/introspect":
		f.introspects++
		switch body["token"] {
		case "good":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "user_id": f.user, "grant_id": f.grant,
				"client_id": "https://claude.ai/oauth/client.json", "scope": "kdraigo", "aud": testResource, "exp": time.Now().Add(time.Hour).Unix()})
		case "other-aud":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "user_id": f.user, "grant_id": f.grant,
				"scope": "kdraigo", "aud": "https://elsewhere.example/mcp", "exp": time.Now().Add(time.Hour).Unix()})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		}
	case "/api/v1/internal/api-keys":
		if f.revoked {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mints = append(f.mints, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"key_id": f.keyID, "expires_at": time.Now().Add(24 * time.Hour)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func rpc(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHandler(t *testing.T) {
	users := &fakeUsers{user: uuid.New(), grant: uuid.New(), keyID: uuid.New()}
	usersSrv := httptest.NewServer(users)
	defer usersSrv.Close()

	// The platform: records which key signed each call.
	var mu sync.Mutex
	var signedBy []string
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		signedBy = append(signedBy, r.Header.Get("X-Key-ID"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer platform.Close()

	cfg := Config{Resource: testResource, Issuer: testIssuer, UsersInternalURL: usersSrv.URL, InternalToken: internalTok}
	s := server.NewMCPServer("kdraigo", "test", server.WithInstructions("hosted kdraigo MCP"))
	tools.RegisterHosted(s, tools.Deps{HTTP: client.NewHTTP(platform.URL, platform.URL, nil)})
	h := Handler(cfg, server.NewStreamableHTTPServer(s, server.WithStateLess(true)), NewIntrospector(cfg), NewKeyring(cfg),
		log.New(io.Discard, "", 0))
	srv := httptest.NewServer(h)
	defer srv.Close()
	mcpURL := srv.URL + "/mcp"

	// --- Discovery: a 401 that points at the metadata, served at both paths.
	resp := rpc(t, mcpURL, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Bearer resource_metadata="https://mcp.kdraigo.test/.well-known/oauth-protected-resource/mcp", scope="kdraigo"`,
		resp.Header.Get("WWW-Authenticate"))
	for _, p := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		r, err := http.Get(srv.URL + p)
		require.NoError(t, err)
		var md map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&md))
		r.Body.Close()
		assert.Equal(t, testResource, md["resource"], p)
		assert.Equal(t, []any{testIssuer}, md["authorization_servers"], p)
		assert.Equal(t, []any{"kdraigo"}, md["scopes_supported"], p)
	}

	// --- Tokens that are not good here.
	resp = rpc(t, mcpURL, "nonsense", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), `error="invalid_token"`)
	resp = rpc(t, mcpURL, "other-aud", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a token for another resource is refused")

	// --- A good token: the MCP server answers, as the user.
	resp = rpc(t, mcpURL, "good", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "hosted kdraigo MCP", "the instructions travel with the server")

	resp = rpc(t, mcpURL, "good", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `"workspace_run"`)
	assert.NotContains(t, string(body), `"scaffold_strategy"`, "nothing that writes to the caller's disk")
	assert.NotContains(t, string(body), `"run_backtest_stream"`)

	for i := 0; i < 2; i++ {
		resp = rpc(t, mcpURL, "good", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ = io.ReadAll(resp.Body)
		assert.NotContains(t, string(body), `"isError":true`, string(body))
	}
	mu.Lock()
	assert.Equal(t, []string{users.keyID.String(), users.keyID.String()}, signedBy, "signed with the key minted for the connection")
	mu.Unlock()
	users.mu.Lock()
	require.Len(t, users.mints, 1, "one key, reused")
	assert.Equal(t, "mcp", users.mints[0]["kind"])
	assert.Equal(t, users.grant.String(), users.mints[0]["grant_id"])
	assert.Equal(t, users.user.String(), users.mints[0]["user_id"])
	assert.EqualValues(t, 86400, users.mints[0]["ttl_seconds"])
	assert.Equal(t, "AI app claude.ai", users.mints[0]["label"])
	assert.Equal(t, 3, users.introspects, "a token's answer is cached: one call each for nonsense, other-aud and good")
	users.mu.Unlock()
}

// A connection revoked after the token was checked gets a 401, and a
// platform that does not answer a 503 (the token may still be good).
func TestHandler_RevokedAndUnavailable(t *testing.T) {
	users := &fakeUsers{user: uuid.New(), grant: uuid.New(), keyID: uuid.New(), revoked: true}
	usersSrv := httptest.NewServer(users)
	cfg := Config{Resource: testResource, Issuer: testIssuer, UsersInternalURL: usersSrv.URL, InternalToken: internalTok}
	s := server.NewMCPServer("kdraigo", "test")
	h := Handler(cfg, server.NewStreamableHTTPServer(s, server.WithStateLess(true)), NewIntrospector(cfg), NewKeyring(cfg),
		log.New(io.Discard, "", 0))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp := rpc(t, srv.URL+"/mcp", "good", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "revoked")

	usersSrv.Close()
	resp = rpc(t, srv.URL+"/mcp", "fresh-token", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestSameResource(t *testing.T) {
	assert.True(t, sameResource("https://MCP.kdraigo.com/mcp", "https://mcp.kdraigo.com/mcp"))
	assert.True(t, sameResource("https://mcp.kdraigo.com/mcp/", "https://mcp.kdraigo.com/mcp"))
	assert.False(t, sameResource("https://mcp.kdraigo.com/other", "https://mcp.kdraigo.com/mcp"))
	assert.False(t, sameResource("", "https://mcp.kdraigo.com/mcp"))
}
