package hosted

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Per-account limits on the hosted server. An AI app's agent loop can call
// tools faster than any person, and a client that gets HTTP 429 from the
// gateway only knows the connector failed. Here, over a limit, the call
// answers with a tool error that says how long to wait: the agent reads it
// and waits, or tells the user why. Counted per account, across all its
// connected apps; in memory, as one hosted server runs.

// Limits bound one account's tool calls.
type Limits struct {
	// PerMinute is how many calls a minute, refilled evenly; Burst how many
	// may come at once after a quiet spell.
	PerMinute int
	Burst     int
	// Long is how many long calls (runs, builds, reports) may be in flight.
	Long int
}

// DefaultLimits: far above what a person at a chat does, low enough that a
// runaway loop is stopped within seconds.
var DefaultLimits = Limits{PerMinute: 120, Burst: 30, Long: 4}

// Limiter applies Limits per account.
type Limiter struct {
	lim    Limits
	isLong func(tool string) bool
	log    *log.Logger
	now    func() time.Time

	mu       sync.Mutex
	accounts map[uuid.UUID]*allowance
	swept    time.Time
}

type allowance struct {
	tokens  float64
	at      time.Time
	long    int
	noticed time.Time // when a refusal was last logged
}

// NewLimiter limits calls by lim; isLong names the long tools.
func NewLimiter(lim Limits, isLong func(tool string) bool, logger *log.Logger) *Limiter {
	return &Limiter{lim: lim, isLong: isLong, log: logger, now: time.Now, accounts: map[uuid.UUID]*allowance{}}
}

// Middleware checks each tool call against its account's limits. A call
// without an identity (not through Handler) is not limited.
func (l *Limiter) Middleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := IdentityFrom(ctx)
		if id == nil {
			return next(ctx, req)
		}
		tool := req.Params.Name
		long := l.isLong(tool)
		if msg := l.admit(id.UserID, tool, long); msg != "" {
			return mcp.NewToolResultError(msg), nil
		}
		if long {
			defer l.release(id.UserID)
		}
		return next(ctx, req)
	}
}

// admit takes one call from the account's allowance (and a long slot for a
// long tool), or says why not.
func (l *Limiter) admit(user uuid.UUID, tool string, long bool) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	a := l.accounts[user]
	if a == nil {
		a = &allowance{tokens: float64(l.lim.Burst), at: now}
		l.accounts[user] = a
	}
	rate := float64(l.lim.PerMinute) / 60
	a.tokens = math.Min(float64(l.lim.Burst), a.tokens+now.Sub(a.at).Seconds()*rate)
	a.at = now
	var msg string
	switch {
	case a.tokens < 1:
		wait := int(math.Ceil((1 - a.tokens) / rate))
		msg = fmt.Sprintf("Rate limit: at most %d tool calls a minute per kdraigo account (its connected apps together). Try again in %d s.", l.lim.PerMinute, wait)
	case long && a.long >= l.lim.Long:
		msg = fmt.Sprintf("Rate limit: at most %d long calls (runs, builds, reports) at once per kdraigo account. Wait for one to finish, then call %s again.", l.lim.Long, tool)
	default:
		a.tokens--
		if long {
			a.long++
		}
		return ""
	}
	// One log line per account a minute: a looping agent would flood it.
	if now.Sub(a.noticed) >= time.Minute {
		a.noticed = now
		l.log.Printf("rate limit: account %s refused %s: %s", user, tool, msg)
	}
	return msg
}

func (l *Limiter) release(user uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.accounts[user]; a != nil && a.long > 0 {
		a.long--
	}
}

// sweep forgets, once a minute, the accounts back at a full allowance with
// nothing in flight.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	refill := time.Duration(float64(l.lim.Burst) / (float64(l.lim.PerMinute) / 60) * float64(time.Second))
	for user, a := range l.accounts {
		if a.long == 0 && now.Sub(a.at) > refill && now.Sub(a.noticed) > time.Minute {
			delete(l.accounts, user)
		}
	}
}

type identityKey struct{}

// WithIdentity carries the caller's identity to the tools.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom is the caller's identity, or nil outside Handler.
func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	return id
}
