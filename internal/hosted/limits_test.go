package hosted

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestLimiter(lim Limits) (*Limiter, *clock, *bytes.Buffer) {
	var logs bytes.Buffer
	c := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	l := NewLimiter(lim, func(tool string) bool { return strings.HasPrefix(tool, "long_") }, log.New(&logs, "", 0))
	l.now = c.now
	return l, c, &logs
}

func call(t *testing.T, l *Limiter, user uuid.UUID, tool string) (string, bool) {
	t.Helper()
	ctx := WithIdentity(context.Background(), &Identity{UserID: user})
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	res, err := l.Middleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})(ctx, req)
	require.NoError(t, err)
	return res.Content[0].(mcp.TextContent).Text, res.IsError
}

func TestLimiter_CallsAMinute(t *testing.T) {
	l, c, logs := newTestLimiter(Limits{PerMinute: 60, Burst: 3, Long: 2})
	alice, bob := uuid.New(), uuid.New()
	for i := 0; i < 3; i++ {
		_, refused := call(t, l, alice, "list_sessions")
		require.False(t, refused, "a burst of %d passes", 3)
	}
	msg, refused := call(t, l, alice, "list_sessions")
	assert.True(t, refused)
	assert.Equal(t, "Rate limit: at most 60 tool calls a minute per kdraigo account (its connected apps together). Try again in 1 s.", msg)
	_, refused = call(t, l, bob, "list_sessions")
	assert.False(t, refused, "another account has its own allowance")

	c.t = c.t.Add(time.Second)
	_, refused = call(t, l, alice, "list_sessions")
	assert.False(t, refused, "one a second at 60 a minute")
	_, refused = call(t, l, alice, "list_sessions")
	assert.True(t, refused)
	assert.Equal(t, 1, strings.Count(logs.String(), "rate limit:"), "one log line per account a minute")
	assert.Contains(t, logs.String(), alice.String())

	c.t = c.t.Add(time.Hour)
	for i := 0; i < 3; i++ {
		_, refused = call(t, l, alice, "list_sessions")
		require.False(t, refused, "refilled up to the burst, no further")
	}
	_, refused = call(t, l, alice, "list_sessions")
	assert.True(t, refused)
}

func TestLimiter_LongCallsInFlight(t *testing.T) {
	l, _, _ := newTestLimiter(Limits{PerMinute: 600, Burst: 100, Long: 2})
	alice := uuid.New()
	assert.Empty(t, l.admit(alice, "long_run", true))
	assert.Empty(t, l.admit(alice, "long_run", true))
	msg := l.admit(alice, "long_run", true)
	assert.Equal(t, "Rate limit: at most 2 long calls (runs, builds, reports) at once per kdraigo account. Wait for one to finish, then call long_run again.", msg)
	before := l.accounts[alice].tokens
	assert.Empty(t, l.admit(alice, "list_sessions", false), "short calls still pass")
	assert.Equal(t, before-1, l.accounts[alice].tokens, "a refused long call spent nothing")
	l.release(alice)
	assert.Empty(t, l.admit(alice, "long_run", true), "a finished long call frees its slot")

	// Through the middleware, the slot is held for the call and given back.
	carol := uuid.New()
	_, refused := call(t, l, carol, "long_build")
	assert.False(t, refused)
	assert.Zero(t, l.accounts[carol].long, "given back when the call returns")
	l.release(alice)
	l.release(alice)
	assert.Zero(t, l.accounts[alice].long)
}

func TestLimiter_WithoutIdentityAndSweep(t *testing.T) {
	l, c, _ := newTestLimiter(Limits{PerMinute: 60, Burst: 1, Long: 1})
	req := mcp.CallToolRequest{}
	req.Params.Name = "list_sessions"
	h := l.Middleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	for i := 0; i < 5; i++ {
		res, err := h(context.Background(), req)
		require.NoError(t, err)
		assert.False(t, res.IsError, "the local server (no identity) is not limited")
	}
	assert.Empty(t, l.accounts)

	idle, busy := uuid.New(), uuid.New()
	call(t, l, idle, "list_sessions")
	assert.Empty(t, l.admit(busy, "long_run", true))
	c.t = c.t.Add(5 * time.Minute)
	call(t, l, uuid.New(), "list_sessions")
	assert.NotContains(t, l.accounts, idle, "an account back at a full allowance is forgotten")
	assert.Contains(t, l.accounts, busy, "one with a call in flight is kept")
}

func TestConfigFromEnv_Limits(t *testing.T) {
	t.Setenv("KDRAIGO_MCP_RESOURCE", "https://mcp.kdraigo.test/mcp")
	t.Setenv("KDRAIGO_MCP_ISSUER", "https://mcp.kdraigo.test")
	t.Setenv("KDRAIGO_MCP_INTERNAL_TOKEN", "x")
	c, err := ConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, DefaultLimits, c.Limits)
	t.Setenv("KDRAIGO_MCP_CALLS_PER_MINUTE", "40")
	t.Setenv("KDRAIGO_MCP_LONG_CALLS", "2")
	c, err = ConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, Limits{PerMinute: 40, Burst: 10, Long: 2}, c.Limits)
	t.Setenv("KDRAIGO_MCP_LONG_CALLS", "0")
	_, err = ConfigFromEnv()
	assert.ErrorContains(t, err, "KDRAIGO_MCP_LONG_CALLS")
}
