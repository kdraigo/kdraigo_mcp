package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// Deps is the set of dependencies passed to every tool's Register function.
type Deps struct {
	HTTP *client.HTTP
}

func toolErr(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}

func toolText(text string) *mcp.CallToolResult {
	return mcp.NewToolResultText(text)
}

type handler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// Deadlines for one tool call. The HTTP client has none of its own, so each
// tool picks what fits: a report over a long session takes the backend longer
// to build than a page of orders.
const (
	callTimeout  = 60 * time.Second
	heavyTimeout = 180 * time.Second
)

// add registers a tool whose call is bounded by callTimeout.
func add(s *server.MCPServer, t mcp.Tool, h handler) {
	addTimeout(s, t, callTimeout, h)
}

// addTimeout registers a tool whose call is bounded by d; zero leaves it to
// the tool, for a stream that runs as long as its session does.
func addTimeout(s *server.MCPServer, t mcp.Tool, d time.Duration, h handler) {
	if d <= 0 {
		s.AddTool(t, h)
		return
	}
	s.AddTool(t, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return h(ctx, req)
	})
}

// mcp-go marks every tool destructive unless told otherwise, so these say
// what each one really does.

// readOnly is a tool that changes nothing, here or on the platform.
func readOnly() mcp.ToolOption {
	return func(t *mcp.Tool) {
		t.Annotations.ReadOnlyHint = mcp.ToBoolPtr(true)
		t.Annotations.DestructiveHint = mcp.ToBoolPtr(false)
		t.Annotations.IdempotentHint = mcp.ToBoolPtr(true)
	}
}

// additive is a tool that creates or writes without removing anything.
func additive(idempotent bool) mcp.ToolOption {
	return func(t *mcp.Tool) {
		t.Annotations.ReadOnlyHint = mcp.ToBoolPtr(false)
		t.Annotations.DestructiveHint = mcp.ToBoolPtr(false)
		t.Annotations.IdempotentHint = mcp.ToBoolPtr(idempotent)
	}
}

// local is a tool that never leaves this machine.
func local() mcp.ToolOption {
	return func(t *mcp.Tool) { t.Annotations.OpenWorldHint = mcp.ToBoolPtr(false) }
}
