package tools

import (
	"context"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// RegisterCandles adds get_candles, calling the unauthenticated data_provider /api/v1/candles.
func RegisterCandles(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_candles",
		mcp.WithDescription("Fetch OHLCV candles from data_provider. Public endpoint, no auth required."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("Exchange ID, e.g. binance or bybit")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol, slash form e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Timeframe, e.g. 1m, 5m, 1h, 1d")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start time as Unix epoch MILLISECONDS, e.g. 1778803200000 (= 2026-05-15T00:00:00Z). Not seconds, not ISO 8601.")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End time as Unix epoch MILLISECONDS, e.g. 1778889600000 (= 2026-05-16T00:00:00Z). Not seconds, not ISO 8601.")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		exchange, err := req.RequireString("exchange")
		if err != nil {
			return toolErr(err), nil
		}
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return toolErr(err), nil
		}
		timeframe, err := req.RequireString("timeframe")
		if err != nil {
			return toolErr(err), nil
		}
		from, err := req.RequireString("from")
		if err != nil {
			return toolErr(err), nil
		}
		to, err := req.RequireString("to")
		if err != nil {
			return toolErr(err), nil
		}
		q := url.Values{}
		q.Set("exchange", exchange)
		q.Set("symbol", symbol)
		q.Set("timeframe", timeframe)
		q.Set("from", from)
		q.Set("to", to)
		body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/candles", q, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_candles", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}
