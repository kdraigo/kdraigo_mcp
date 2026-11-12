package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

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
	addPositioning(s, d)
	tool := mcp.NewTool("get_candles",
		mcp.WithDescription("Fetch OHLCV candles from data_provider. Public endpoint, no auth required. At most 50,000 bars per call (about a month of 1m): for longer ranges, use a larger timeframe or split the range into several calls. "+
			"A range with no bars says why (before the market's history, after its newest bar, or a gap in it); a market the exchange does not have, times in seconds and a reversed or future range are errors."),
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
		fromMs, toMs, err := millisRange(from, to, time.Now())
		if err != nil {
			return toolErr(fmt.Errorf("get_candles: %w", err)), nil
		}
		q := url.Values{}
		q.Set("exchange", exchange)
		q.Set("symbol", symbol)
		q.Set("timeframe", timeframe)
		q.Set("from", strconv.FormatInt(fromMs, 10))
		q.Set("to", strconv.FormatInt(toMs, 10))
		body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/candles", q, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_candles", status, body)), nil
		}
		if b := bytes.TrimSpace(body); string(b) == "null" || string(b) == "[]" {
			return d.noCandles(ctx, exchange, symbol, fromMs, toMs), nil
		}
		return toolText(string(body)), nil
	})
}

// minMillis is 1973-03-03 in milliseconds: a time below it is seconds (as
// seconds it is the year 5138), or a mistake.
const minMillis = 100_000_000_000

// millisRange reads a request's from and to as Unix milliseconds and refuses
// what would only come back empty: seconds, a range that ends before it
// starts, a start in the future.
func millisRange(from, to string, now time.Time) (int64, int64, error) {
	parse := func(name, raw string) (int64, error) {
		v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s %q: give Unix epoch milliseconds, a whole number like 1778803200000 (2026-05-15 00:00 UTC)", name, raw)
		}
		if v < minMillis {
			if v > 0 {
				return 0, fmt.Errorf("%s %d looks like seconds (%s UTC as seconds, %s as milliseconds): give milliseconds, %d", name, v, utc(v*1000), utc(v), v*1000)
			}
			return 0, fmt.Errorf("%s %d is not a time the data holds: give Unix epoch milliseconds, like 1778803200000", name, v)
		}
		return v, nil
	}
	f, err := parse("from", from)
	if err != nil {
		return 0, 0, err
	}
	t, err := parse("to", to)
	if err != nil {
		return 0, 0, err
	}
	if t < f {
		return 0, 0, fmt.Errorf("to (%s UTC) is before from (%s UTC): the range is reversed", utc(t), utc(f))
	}
	if f > now.UnixMilli() {
		return 0, 0, fmt.Errorf("from (%s UTC) is in the future: there are no bars after now (%s UTC)", utc(f), utc(now.UnixMilli()))
	}
	return f, t, nil
}

func utc(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04") }

// noCandles says why a request came back empty: a market the exchange does
// not have is a wrong request; a range outside its history, or in a gap of
// it, is a right one with nothing in it.
func (d Deps) noCandles(ctx context.Context, exchange, symbol string, from, to int64) *mcp.CallToolResult {
	what := fmt.Sprintf("no candles for %s on %s from %s to %s UTC", symbol, exchange, utc(from), utc(to))
	body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/metadata", nil, nil)
	var meta map[string]struct {
		Symbols map[string]struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"symbols"`
	}
	if err != nil || status < 200 || status >= 300 || json.Unmarshal(body, &meta) != nil {
		return toolText(fmt.Sprintf("No candles: %s (the coverage could not be read to say why; get_data_coverage lists what there is).", what))
	}
	symbols := meta[exchange].Symbols
	key := strings.ReplaceAll(symbol, "/", "")
	span, ok := symbols[key]
	if !ok {
		if _, upper := symbols[strings.ToUpper(key)]; upper {
			return toolErr(fmt.Errorf("get_candles: symbols are upper case: %s, not %s", strings.ToUpper(symbol), symbol))
		}
		msg := fmt.Sprintf("get_candles: %s is not a market on %s, so there is nothing to return", symbol, exchange)
		if alike := alikeSymbols(symbols, strings.ToUpper(key)); len(alike) > 0 {
			msg += fmt.Sprintf(" (it has %s, stored without the slash)", strings.Join(alike, ", "))
		}
		return toolErr(fmt.Errorf("%s. get_data_coverage lists every market", msg))
	}
	switch {
	case to < span.Start:
		return toolText(fmt.Sprintf("No candles: %s. Its history starts %s UTC: the range ends before it.", what, utc(span.Start)))
	case from > span.End:
		return toolText(fmt.Sprintf("No candles: %s. Its newest bar is %s UTC: the range starts after it.", what, utc(span.End)))
	default:
		return toolText(fmt.Sprintf("No candles: %s, inside its history (%s to %s UTC): a gap in the exchange's own record.", what, utc(span.Start), utc(span.End)))
	}
}

// alikeSymbols are up to five of an exchange's symbols that share key's
// first three letters, for a near miss.
func alikeSymbols(symbols map[string]struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}, key string) []string {
	if len(key) < 3 {
		return nil
	}
	var out []string
	for s := range symbols {
		if strings.HasPrefix(s, key[:3]) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// Market data answers (get_flow, get_positioning) come as CSV by default:
// the agent reading them often reaches data only through these replies, and
// a year of daily bars in JSON rows was 137 KB and cut off.
const maxMarketReply = 100 << 10

var marketShapeArgs = []mcp.ToolOption{
	mcp.WithString("from", mcp.Description("Start time as Unix epoch MILLISECONDS, e.g. 1778803200000. Not seconds, not ISO 8601. Required unless `at` is given.")),
	mcp.WithString("to", mcp.Description("End time as Unix epoch MILLISECONDS. Required unless `at` is given.")),
	mcp.WithString("at", mcp.Description("Instead of a window: up to 1000 times (Unix ms, comma-separated). Each answers the newest bar of the timeframe that had CLOSED by that time (no look-ahead), with the time first as `at`. One call reads values at many event times.")),
	mcp.WithString("fields", mcp.Description("Columns to return, comma-separated (open_time always comes), e.g. open_interest,long_short_ratio. Fewer columns, smaller answer.")),
	mcp.WithString("format", mcp.Description("csv (default): a header row, then one row per bar; an empty cell is null. json: rows of objects."), mcp.Enum("csv", "json")),
}

// marketHandler forwards a market data request to data_provider and shapes the answer.
func marketHandler(d Deps, name, path string) handler {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := url.Values{}
		for _, n := range []string{"exchange", "symbol", "timeframe"} {
			v, err := req.RequireString(n)
			if err != nil {
				return toolErr(err), nil
			}
			q.Set(n, v)
		}
		if at := strings.ReplaceAll(strings.TrimSpace(req.GetString("at", "")), " ", ""); at != "" {
			q.Set("at", at)
		} else {
			from, to := req.GetString("from", ""), req.GetString("to", "")
			if from == "" || to == "" {
				return toolErr(fmt.Errorf("give from and to (Unix ms), or at (times to look up)")), nil
			}
			q.Set("from", from)
			q.Set("to", to)
		}
		if f := strings.ReplaceAll(strings.TrimSpace(req.GetString("fields", "")), " ", ""); f != "" {
			q.Set("fields", f)
		}
		format := req.GetString("format", "csv")
		switch format {
		case "csv":
			q.Set("format", "columns")
		case "json":
		default:
			return toolErr(fmt.Errorf("format: csv or json, not %q", format)), nil
		}
		body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, path, q, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr(name, status, body)), nil
		}
		text := string(body)
		if format == "csv" {
			if text, err = columnsCSV(body); err != nil {
				return toolErr(fmt.Errorf("%s: %w", name, err)), nil
			}
		}
		if len(text) > maxMarketReply {
			return toolErr(fmt.Errorf("the answer is %d KB, more than a reply holds (%d KB): ask for fewer columns (fields), a longer timeframe or a shorter window, or look up the times you need with at",
				len(text)>>10, maxMarketReply>>10)), nil
		}
		return toolText(text), nil
	}
}

// columnsCSV turns data_provider's columns form ({"name": [values…]}, in
// order) into CSV: a header, then one row per bar; null is an empty cell.
func columnsCSV(body []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", fmt.Errorf("not the columns form")
	}
	var names []string
	var cols [][]any
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		name, _ := tok.(string)
		var vals []any
		if err := dec.Decode(&vals); err != nil {
			return "", fmt.Errorf("column %s: %w", name, err)
		}
		names = append(names, name)
		cols = append(cols, vals)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(names)
	n := 0
	if len(cols) > 0 {
		n = len(cols[0])
	}
	row := make([]string, len(cols))
	for i := 0; i < n; i++ {
		for c := range cols {
			row[c] = ""
			if i >= len(cols[c]) {
				continue
			}
			switch v := cols[c][i].(type) {
			case json.Number:
				row[c] = v.String()
			case bool:
				row[c] = fmt.Sprint(v)
			case string:
				row[c] = v
			}
		}
		_ = w.Write(row)
	}
	w.Flush()
	return buf.String(), w.Error()
}

// addFlow adds get_flow: per-bar trade flow from data_provider's trade
// streams (/api/v1/flow).
func addFlow(s *server.MCPServer, d Deps) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Fetch trade flow per bar from data_provider: what executed, by taker side. Public endpoint, no auth required. " +
			"Each bar has buy_volume and sell_volume (base; a buy is a taker buying), buy_quote and sell_quote, delta (buy - sell volume), vwap, " +
			"buy_trades and sell_trades, max_print_quote (largest single taker print) and the prints of at least 10k and 100k quote (large_prints_10k, large_quote_10k, …). " +
			"On perpetuals (binance_futures, bybit_futures) also liquidations (liq_long_* = longs force-closed, liq_short_*), and open_interest (base), " +
			"open_interest_value, funding_rate (the current rate, not a settlement), next_funding_time, mark_price and index_price as they stood at the bar's end. " +
			"These perpetual fields are NULL (an empty CSV cell) for bars before the perpetual's live state was collected (6 Nov 2026), never 0; a 0 is a real zero. " +
			"For open interest history use get_positioning (back to 2020). " +
			"minutes = minutes stored in the bar; complete = every minute was seen whole. Binance liquidations are a lower bound (Binance streams at most one a second per symbol), and " +
			"Binance futures minutes come from aggregate trades, so a few percent of a minute's volume can sit in the neighbouring minute (daily totals are exact). " +
			"Trade columns go back to 1 Mar 2025. At most 50,000 bars per call; answers over 100 KB are refused with what to narrow."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("Exchange ID: binance, binance_futures, bybit or bybit_futures")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol, slash form e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Timeframe. One of: 1m, 2m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 1d, 1w, 1M (1m is one minute, 1M one month).")),
	}
	opts = append(append(opts, marketShapeArgs...), readOnly())
	add(s, mcp.NewTool("get_flow", opts...), marketHandler(d, "get_flow", "/api/v1/flow"))
}

// addPositioning adds get_positioning: futures open interest and long/short
// ratios from data_provider (/api/v1/positioning).
func addPositioning(s *server.MCPServer, d Deps) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Fetch futures positioning per bar from data_provider: open interest and who is long, for the perpetuals on binance_futures and bybit_futures. Public endpoint, no auth required. " +
			"Built from the venues' 5-minute periods, history back to 2020-09 (or the listing). Each bar has open_interest (base units, one side, comparable across venues), " +
			"open_interest_value (quote; Binance only), long_short_ratio and long_account_share (all accounts long vs short), top_account_ratio and top_position_ratio " +
			"(the top 20 % of accounts by margin, counted by account and by position size; Binance only), taker_buy_sell_ratio (taker buy over sell volume; Binance only; " +
			"geometric mean over the bar) and periods (5-minute periods in the bar). Snapshot fields are the bar's last value, i.e. the value at the bar's close; " +
			"NULL (an empty CSV cell) = not published by the venue. Timeframe 5m or longer. " +
			"To read values at event times use at (up to 1000 times per call) rather than one call per time. At most 50,000 bars per call; answers over 100 KB are refused with what to narrow."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("binance_futures or bybit_futures")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol, slash form e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Timeframe, 5m or longer: 5m, 15m, 30m, 1h, 2h, 4h, 1d, 1w, 1M.")),
	}
	opts = append(append(opts, marketShapeArgs...), readOnly())
	add(s, mcp.NewTool("get_positioning", opts...), marketHandler(d, "get_positioning", "/api/v1/positioning"))
}
