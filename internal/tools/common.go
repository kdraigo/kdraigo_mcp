package tools

import (
	"context"

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

// add registers a tool with the server using a closure-friendly signature.
func add(s *server.MCPServer, t mcp.Tool, h func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	s.AddTool(t, h)
}
