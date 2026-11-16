package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

func httpErr(name string, status int, body []byte) error {
	// A service's own message reads better than its JSON.
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s: HTTP %d: %s", name, status, e.Error)
	}
	return fmt.Errorf("%s: HTTP %d: %s", name, status, string(body))
}

// sendJSON is a signed request with a JSON body (nil for none), decoded into
// out (nil to ignore the answer).
func (d Deps) sendJSON(ctx context.Context, name, method string, svc client.ServicePrefix, path string, body, out any) error {
	return d.sendQuery(ctx, name, method, svc, path, nil, body, out)
}

// sendQuery is sendJSON with a query. The query is signed apart from the
// path, so one written into the path makes a signature no server accepts
// (chart_remove all=true answered 401 that way in v1.35.0): it is refused here.
func (d Deps) sendQuery(ctx context.Context, name, method string, svc client.ServicePrefix, path string, q url.Values, body, out any) error {
	if strings.Contains(path, "?") {
		return fmt.Errorf("%s: internal error: the query is in the path %q; pass it as url.Values", name, path)
	}
	resp, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, method, svc, path, q, body)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if status < 200 || status >= 300 {
		return httpErr(name, status, resp)
	}
	if out == nil || len(resp) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", name, err)
	}
	return nil
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
