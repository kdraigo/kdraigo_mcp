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

//go:embed indicators.md
var indicatorDocs string

// RegisterDocs adds the get_sdk_docs and get_indicator_docs tools.
// No backend calls; both ship embedded markdown.
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

	// get_indicator_docs serves the in-depth per-indicator reference (purpose,
	// calculation, what it signals, usage) plus the pointType explanation. Kept
	// separate from get_sdk_docs so the full ~95-indicator writeup does not bloat
	// the default SDK-docs response.
	indTool := mcp.NewTool("get_indicator_docs",
		mcp.WithDescription("Return the in-depth indicator reference for the kdraigo dev_sdk (what each indicator is for, how it's calculated, what it signals, and how to use it), plus the pointType/maType explanation. Optional `name` returns just one indicator (e.g. \"RSI\", \"BB\", \"MACD\") or one category heading (e.g. \"momentum\", \"volatility\"), case-insensitive."),
		mcp.WithString("name", mcp.Description("Optional indicator name or category to return only that section.")),
	)
	add(s, indTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := strings.TrimSpace(req.GetString("name", ""))
		if name == "" {
			return toolText(indicatorDocs), nil
		}
		out := extractHeading(indicatorDocs, name)
		if out == "" {
			return toolErr(fmt.Errorf("indicator or section %q not found", name)), nil
		}
		return toolText(out), nil
	})
}

// extractHeading returns the chunk starting at the first H2 (## ) or H3 (### )
// whose title contains `needle` (case-insensitive) and ending at the next
// heading of the same or higher level. This lets callers pull a single indicator
// (an H3 like "### `RSI` — ...") or a whole category (an H2 like "## Momentum").
func extractHeading(doc, needle string) string {
	needle = strings.ToLower(needle)
	lines := strings.Split(doc, "\n")

	headingLevel := func(l string) int {
		switch {
		case strings.HasPrefix(l, "### "):
			return 3
		case strings.HasPrefix(l, "## "):
			return 2
		default:
			return 0
		}
	}

	start, level := -1, 0
	for i, l := range lines {
		lvl := headingLevel(l)
		if lvl >= 2 && strings.Contains(strings.ToLower(l), needle) {
			start, level = i, lvl
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if lvl := headingLevel(lines[i]); lvl > 0 && lvl <= level {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
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
