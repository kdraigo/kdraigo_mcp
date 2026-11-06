package tools

import (
	"context"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// RegisterCandles adds get_candles, get_flow and get_data_coverage, calling
// the unauthenticated data_provider /api/v1/candles, /api/v1/flow and
// /api/v1/metadata.
func RegisterCandles(s *server.MCPServer, d Deps) {
	addDataCoverage(s, d)
	addFlow(s, d)
	tool := mcp.NewTool("get_candles",
		mcp.WithDescription("Fetch OHLCV candles from data_provider. Public endpoint, no auth required. At most 50,000 bars per call (about a month of 1m): for longer ranges, use a larger timeframe or split the range into several calls."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("Exchange ID, e.g. binance or bybit")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol, slash form e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Timeframe. One of: 1m, 2m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 1d, 1w, 1M. Case-sensitive: 1m is one minute, 1M is one month. Anything else is rejected with HTTP 400 — it is not silently served as 1m data.")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start time as Unix epoch MILLISECONDS, e.g. 1778803200000 (= 2026-05-15T00:00:00Z). Not seconds, not ISO 8601.")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End time as Unix epoch MILLISECONDS, e.g. 1778889600000 (= 2026-05-16T00:00:00Z). Not seconds, not ISO 8601.")),
		readOnly(),
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

// addFlow adds get_flow: per-bar trade flow from data_provider's trade
// streams (/api/v1/flow).
func addFlow(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_flow",
		mcp.WithDescription("Fetch trade flow per bar from data_provider: what executed, by taker side. Public endpoint, no auth required. "+
			"Each bar has buy_volume and sell_volume (base; a buy is a taker buying), buy_quote and sell_quote, delta (buy - sell volume), vwap, "+
			"buy_trades and sell_trades, max_print_quote (largest single taker print) and the prints of at least 10k and 100k quote (large_prints_10k, large_quote_10k, …). "+
			"On perpetuals (binance_futures, bybit_futures) also liquidations (liq_long_* = longs force-closed, liq_short_*), and open_interest (base), "+
			"open_interest_value, funding_rate (the current rate, not a settlement), next_funding_time, mark_price and index_price as they stood at the bar's end. "+
			"minutes = minutes stored in the bar; complete = every minute was seen whole. Binance liquidations are a lower bound (Binance streams at most one a second per symbol), and "+
			"Binance futures minutes come from aggregate trades, so a few percent of a minute's volume can sit in the neighbouring minute (daily totals are exact). "+
			"Liquidations, open interest and funding exist from the day collection started; trade columns can be backfilled further. At most 50,000 bars per call."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("Exchange ID: binance, binance_futures, bybit or bybit_futures")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol, slash form e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Timeframe. One of: 1m, 2m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 1d, 1w, 1M (1m is one minute, 1M one month).")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start time as Unix epoch MILLISECONDS, e.g. 1778803200000. Not seconds, not ISO 8601.")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End time as Unix epoch MILLISECONDS. Not seconds, not ISO 8601.")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := url.Values{}
		for _, name := range []string{"exchange", "symbol", "timeframe", "from", "to"} {
			v, err := req.RequireString(name)
			if err != nil {
				return toolErr(err), nil
			}
			q.Set(name, v)
		}
		body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/flow", q, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_flow", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}
