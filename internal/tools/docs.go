package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

//go:embed sdk.md
var sdkDocs string

// RegisterDocs adds the get_sdk_docs tool. No backend calls; ships embedded markdown.
func RegisterDocs(s *server.MCPServer) {
	tool := mcp.NewTool("get_sdk_docs",
		mcp.WithDescription("Return embedded kdraigo dev_sdk documentation. Optional `section` filters to a single H2 heading (case-insensitive substring match)."),
		mcp.WithString("section", mcp.Description("Optional section name to return only that section.")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		section := strings.TrimSpace(req.GetString("section", ""))
		if section == "" {
			return toolText(sdkDocs), nil
		}
		out := extractSection(sdkDocs, section)
		if out == "" {
			return toolErr(fmt.Errorf("section %q not found", section)), nil
		}
		return toolText(out), nil
	})
}

// extractSection returns the chunk of the document starting at the first H2 whose title
// contains `needle` (case-insensitive) and ending at the next H2.
func extractSection(doc, needle string) string {
	needle = strings.ToLower(needle)
	lines := strings.Split(doc, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") && strings.Contains(strings.ToLower(l), needle) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
