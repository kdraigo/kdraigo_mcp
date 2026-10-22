package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

func httpErr(name string, status int, body []byte) error {
	return fmt.Errorf("%s: HTTP %d: %s", name, status, string(body))
}

// getJSON is a signed GET through the gateway, decoded into out. name is the
// tool, for the error.
func (d Deps) getJSON(ctx context.Context, name string, svc client.ServicePrefix, path string, q url.Values, out any) error {
	body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodGet, svc, path, q, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if status < 200 || status >= 300 {
		return httpErr(name, status, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", name, err)
	}
	return nil
}

// pageArgs reads limit and offset, keeping limit within 1..maxLimit.
func pageArgs(req mcp.CallToolRequest, def, maxLimit int) (limit, offset int) {
	limit = req.GetInt("limit", def)
	if limit < 1 {
		limit = def
	}
	return min(limit, maxLimit), max(req.GetInt("offset", 0), 0)
}
