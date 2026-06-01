package tools

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// RegisterAnalytics adds get_analytics_series + get_analytics_types, both via analytics_service /dev path.
func RegisterAnalytics(s *server.MCPServer, d Deps) {
	addGetAnalyticsTypes(s, d)
	addGetAnalyticsSeries(s, d)
}

func addGetAnalyticsTypes(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_analytics_types",
		mcp.WithDescription("List available analytics metric types (e.g. equity, trading_frequency, wallet_quote)."),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodGet, client.Analytics, "/api/v1/dev/analytics/types", nil, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_analytics_types", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}

func addGetAnalyticsSeries(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_analytics_series",
		mcp.WithDescription("Time-bucketed analytics series for a session. `type` must be one of the values from get_analytics_types."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Required(), mcp.Description("Session type: 'backtest' or 'live'"), mcp.Enum("backtest", "live")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Metric type, e.g. equity")),
		mcp.WithString("timeframe", mcp.Description("Bucket timeframe, e.g. 1m, 1h, 1d"), mcp.DefaultString("1m")),
		mcp.WithNumber("from", mcp.Required(), mcp.Description("Start timestamp in Unix milliseconds")),
		mcp.WithNumber("to", mcp.Required(), mcp.Description("End timestamp in Unix milliseconds")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		sessionType, err := req.RequireString("session_type")
		if err != nil {
			return toolErr(err), nil
		}
		metric, err := req.RequireString("type")
		if err != nil {
			return toolErr(err), nil
		}
		from, err := req.RequireFloat("from")
		if err != nil {
			return toolErr(err), nil
		}
		to, err := req.RequireFloat("to")
		if err != nil {
			return toolErr(err), nil
		}
		tf := req.GetString("timeframe", "1m")

		q := url.Values{}
		q.Set("session_id", sessionID)
		q.Set("session_type", sessionType)
		q.Set("type", metric)
		q.Set("timeframe", tf)
		q.Set("from", strconv.FormatInt(int64(from), 10))
		q.Set("to", strconv.FormatInt(int64(to), 10))

		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodGet, client.Analytics, "/api/v1/dev/analytics/series", q, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_analytics_series", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}
