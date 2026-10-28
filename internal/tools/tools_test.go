package tools

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

const sid = "11111111-2222-3333-4444-555555555555"

// backend serves the gateway paths the tools call, each from its own handler;
// a path with no handler fails the test.
func backend(t *testing.T, routes map[string]http.HandlerFunc) *server.MCPServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := auth.NewSigner("key-1", hex.EncodeToString(priv))
	require.NoError(t, err)
	s := server.NewMCPServer("test", "0")
	Register(s, Deps{HTTP: client.NewHTTP(srv.URL, srv.URL, signer)})
	return s
}

func jsonReply(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// call runs a tool and returns its text and whether it reported an error.
func call(t *testing.T, s *server.MCPServer, name string, args map[string]any) (string, bool) {
	t.Helper()
	tool := s.GetTool(name)
	require.NotNil(t, tool, "tool %s not registered", name)
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := tool.Handler(context.Background(), req)
	require.NoError(t, err)
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestReplayToolsNeedTheirSwitch(t *testing.T) {
	s := backend(t, nil)
	assert.Nil(t, s.GetTool("create_backtest_session"))
	assert.Nil(t, s.GetTool("run_backtest_stream"))
	assert.NotNil(t, s.GetTool("get_session_report"))

	t.Setenv(ReplayEnv, "1")
	s = backend(t, nil)
	assert.NotNil(t, s.GetTool("create_backtest_session"))
	assert.NotNil(t, s.GetTool("run_backtest_stream"))
}

func TestAnnotationsSayWhatEachToolDoes(t *testing.T) {
	t.Setenv(ReplayEnv, "1")
	s := backend(t, nil)
	destructive := map[string]bool{"delete_session": true, "close_session": true, "update_session_metadata": true,
		"workspace_write_file": true, "workspace_delete_file": true, "workspace_stop": true, "workspace_delete": true}
	writes := map[string]bool{"scaffold_strategy": true, "create_backtest_session": true, "run_backtest_stream": true, "get_session_orders": true, "get_trades": true,
		"workspace_create": true, "workspace_build": true, "workspace_run": true}
	for name, st := range s.ListTools() {
		a := st.Tool.Annotations
		assert.Equal(t, destructive[name], *a.DestructiveHint, "%s destructive", name)
		assert.Equal(t, !destructive[name] && !writes[name], *a.ReadOnlyHint, "%s read-only", name)
	}
}

// reportFixture is a two-instrument session that finished early, with a
// position open at the end and one trade carrying half the result.
func reportFixture() performanceReport {
	return performanceReport{
		SessionID: sid, Quote: "USDT", InitialCapital: 1000, FinalEquity: 1100,
		Total: stats{
			NetPnL: 100, ReturnPct: ptr(10), GrossPnL: 130, Fees: 20, Funding: 10,
			MaxDrawdown: 5, MaxDrawdownPct: ptr(0.495),
			Trades: 12, Wins: 6, Losses: 6, WinRate: ptr(0.5), ProfitFactor: ptr(2),
			BestTrade: ptr(50), WorstTrade: ptr(-10), LongTrades: 8, ShortTrades: 4, LongWins: 5, ShortWins: 1,
			OpenAtEnd: 1, Orders: orderBreakdown{Total: orderTally{Placed: 30, Filled: 24, Canceled: 6}, StopLoss: orderTally{Placed: 9, Filled: 8, Canceled: 1}},
		},
		Instruments: []stats{
			{Exchange: "binance_futures", Pair: "BTC/USDT", Model: "positions", NetPnL: 80, ReturnPct: ptr(8), Trades: 8, Wins: 4, OpenAtEnd: 1},
			{Exchange: "binance_futures", Pair: "ETH/USDT", Model: "positions", NetPnL: 20, ReturnPct: ptr(2), Trades: 4, Wins: 2},
		},
	}
}

func dayPoint(day string, v float64) seriesPoint {
	t, _ := time.Parse("2006-01-02", day)
	return seriesPoint{OpenTime: t.UnixMilli(), Value: v}
}

func TestSessionReport(t *testing.T) {
	start, end := time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, sid, r.URL.Query().Get("session_id"))
			assert.Equal(t, "backtest", r.URL.Query().Get("session_type"))
			jsonReply(reportFixture())(w, r)
		},
		"/frontend-api/api/v1/dev/sessions/" + sid: jsonReply(sessionDetail{
			ID: sid, Name: "v1 ema", Status: "RUNNING", Timeframe: "1h", StartAt: &start, EndAt: &end,
			Progress: &sessionProgress{ProcessedCandles: 800, TotalCandles: 1000, Percent: 80, LastCandleTime: &last},
		}),
		"/analytics/api/v1/dev/analytics/series": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "equity", r.URL.Query().Get("type"))
			assert.Equal(t, "1d", r.URL.Query().Get("timeframe"))
			jsonReply(seriesResponse{Points: []seriesPoint{
				dayPoint("2026-01-30", 1010), dayPoint("2026-01-31", 1005), dayPoint("2026-02-01", 1100),
			}})(w, r)
		},
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Open at the end: 1 position(s), which this analytics_service (report version 0) counts in the figures above.")

	for _, want := range []string{
		`Session ` + sid + ` "v1 ema" · backtest · RUNNING · 1h`,
		"Capital 1000.00 USDT → 1100.00 on closed trades · return +10.00% · max DD 5.00 (0.5%) · return/DD 20.20",
		"Net +100.00 = gross +130.00 − fees 20.00 − funding 10.00",
		"best +50.00 · worst -10.00",
		"Trades 12 (long 8 / short 4) · win rate 50.0% (long 62.5% / short 25.0%) · PF 2.00 · avg trade +8.33",
		"Orders placed/filled/canceled: all 30/24/6 · stop_loss 9/8/1\n",
		"- Run not finished (status RUNNING)",
		"- Processed 800 of 1000 candles (80.0%), last candle 2026-02-01 12:00 against a window ending 2026-02-02 00:00",
		"- Only 12 closed trades",
		"- 1 position(s) open at the end, valued at their last fill and counted in the net P&L.",
		"- The best trade (+50.00) is 50% of the net P&L",
		"binance_futures\tBTC/USDT\tpositions\t+80.00\t+8.00%\t8\t",
		"2 months: 0 losing, longest losing run 0 · best 2026-02 +95.00 · worst 2026-01 +5.00",
		"3 days: 2 up, 1 down, 0 flat · best day 2026-02-01 +95.00 · worst day 2026-01-31 -5.00",
		"2026-01\t+5.00\t+0.50%\t2\t1\t1\t2026-01-31 -5.00",
		"2026-02\t+95.00\t+9.45%\t1\t1\t0\t—",
	} {
		assert.Contains(t, out, want)
	}
}

// Report version 3 leaves what is open out of the figures and lists it apart:
// at the engine's recorded last close, or, for a session from before the
// engine recorded one, at the last close the market data has, labelled so.
func TestSessionReport_OpenAtEnd(t *testing.T) {
	start, end := time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	markAt := end.Add(-time.Millisecond)
	rep := reportFixture()
	rep.ReportVersion = 3
	rep.Total.OpenAtEnd = 2
	rep.Total.Open = &openBlock{
		Positions: []openPosition{
			{Exchange: "binance_futures", Pair: "BTC/USDT", Side: "long", Qty: 0.1, AvgEntry: 50000,
				MarkPrice: ptr(50800), MarkAt: &markAt, Unrealized: ptr(80), Fees: 2, Funding: 1},
			{Exchange: "binance_futures", Pair: "ETH/USDT", Side: "short", Qty: 2, AvgEntry: 3000, Realized: 5, Fees: 1},
		},
		Realized: 5, Fees: 3, Funding: 1,
	}
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(rep),
		"/frontend-api/api/v1/dev/sessions/" + sid: jsonReply(sessionDetail{
			ID: sid, Status: "DONE", Timeframe: "1h", StartAt: &start, EndAt: &end,
		}),
		"/analytics/api/v1/dev/analytics/series": jsonReply(seriesResponse{}),
		// Only ETH has no recorded price.
		"/data/api/v1/candles": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "ETH/USDT", r.URL.Query().Get("symbol"))
			assert.Equal(t, strconv.FormatInt(end.UnixMilli(), 10), r.URL.Query().Get("to"))
			jsonReply([]map[string]any{{"close": 2950.0, "close_time": end.UnixMilli() - 1}})(w, r)
		},
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	for _, want := range []string{
		"Capital 1000.00 USDT → 1100.00 on closed trades",
		"## open at the end: 2 position(s), left out of every figure above",
		"Unrealized +180.00 at the last close · paid on them so far: fees 3.00, funding 1.00 · realized by partial closes +5.00",
		"binance_futures\tBTC/USDT\tlong\t0.100000\t50000\t50800\t",
		"\t+80.00\t0.00\t2.00\t1.00\t",
		"\tengine\n",
		"binance_futures\tETH/USDT\tshort\t2.000000\t3000\t2950\t",
		"\t+100.00\t+5.00\t1.00\t0.00\t",
		"\tlooked up\n",
		"looked up: the engine recorded no price at the end",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "Marked to the last close")
}

// The account's value at each day's close and buy and hold come apart from
// the closed-trade headline.
func TestSessionReport_MarkToMarketAndBuyAndHold(t *testing.T) {
	rep := reportFixture()
	rep.ReportVersion = 3
	require.NoError(t, json.Unmarshal([]byte(`{
		"final_equity": 950, "return_pct": -5, "max_drawdown": 120, "max_drawdown_pct": 11.2,
		"curve": [{"day": "2026-01-30", "equity": 1000}, {"day": "2026-01-31", "equity": 880}, {"day": "2026-02-01", "equity": 950}]}`), &rep.MarkToMarket))
	require.NoError(t, json.Unmarshal([]byte(`{
		"instruments": [
			{"exchange": "binance_futures", "pair": "BTC/USDT", "first_open": 100, "last_close": 120, "return_pct": 20, "max_drawdown_pct": 20},
			{"exchange": "binance_futures", "pair": "ETH/USDT", "first_open": 10, "last_close": 9, "return_pct": -10, "max_drawdown_pct": 10}],
		"basket_return_pct": 5, "excess_pct": -10}`), &rep.Benchmark))
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(rep),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
		"/frontend-api/api/v1/dev/sessions/" + sid:    jsonReply(sessionDetail{ID: sid, Status: "DONE", Timeframe: "1h"}),
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	for _, want := range []string{
		"## mark to market and buy & hold (daily closes; apart from the figures above)",
		"Value at each day's close, open positions included: 1000.00 → 950.00 USDT · return -5.00% · max DD 120.00 (11.2%) over 3 days",
		"Buy & hold, first open → last close: BTC/USDT +20.0% (max DD 20.0%) · ETH/USDT -10.0% (max DD 10.0%) · equal weight +5.0% · the strategy -10.0% against it",
	} {
		assert.Contains(t, out, want)
	}

	rep.MarkToMarket, rep.Benchmark = nil, nil
	s = backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(rep),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
		"/frontend-api/api/v1/dev/sessions/" + sid:    jsonReply(sessionDetail{ID: sid, Status: "DONE", Timeframe: "1h"}),
	})
	out, _ = call(t, s, "get_session_report", map[string]any{"session_id": sid})
	assert.NotContains(t, out, "mark to market", "not recorded: no block")
}

func TestLiveReportSkipsTheBacktestRecord(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(reportFixture()),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid, "session_type": "live"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Session "+sid+" · live")
	assert.NotContains(t, out, "Session record not available")
	assert.Contains(t, out, "## months: no days")
}

func TestReportFailsOnlyWithoutPerformance(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(reportFixture()),
		"/frontend-api/api/v1/dev/sessions/" + sid:    func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) },
		"/analytics/api/v1/dev/analytics/series":      func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 502) },
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Session record not available, so completion is unchecked")
	assert.Contains(t, out, "## months: not available (daily equity: HTTP 502")

	s = backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"error":"session not found"}`, 404) },
		"/frontend-api/api/v1/dev/sessions/" + sid:    jsonReply(sessionDetail{ID: sid}),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
	})
	out, isErr = call(t, s, "get_session_report", map[string]any{"session_id": sid})
	assert.True(t, isErr)
	assert.Contains(t, out, "HTTP 404")
}

func TestReportOfAWideSessionFitsTheCap(t *testing.T) {
	rep := reportFixture()
	rep.Instruments = nil
	for i := range 50 {
		rep.Instruments = append(rep.Instruments, stats{Exchange: "binance_futures", Pair: fmt.Sprintf("COIN%d/USDT", i), Model: "positions",
			NetPnL: 1.5, ReturnPct: ptr(0.15), Trades: 40, Wins: 20, ProfitFactor: ptr(1.1), AvgWin: ptr(3), AvgLoss: ptr(-2.5), MaxDrawdownPct: ptr(3)})
	}
	var days []seriesPoint
	for i := range 1100 {
		days = append(days, seriesPoint{OpenTime: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).UnixMilli(), Value: 1000 + float64(i%17)})
	}
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(rep),
		"/frontend-api/api/v1/dev/sessions/" + sid:    jsonReply(sessionDetail{ID: sid, Status: "COMPLETED"}),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{Points: days}),
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	assert.Less(t, len(out), render.MaxBytes)
	assert.Contains(t, out, "COIN49/USDT")
	assert.Contains(t, out, "2026-01\t") // the last month made it in
}

func ordersFor(n int) []order {
	out := make([]order, n)
	for i := range out {
		out[i] = order{ID: fmt.Sprintf("o%d", i), Exchange: "binance_futures", Pair: "BTC/USDT", Type: "STOP_LOSS", Side: "SELL",
			Role: "stop_loss", Status: "FILLED", Amount: 0.01, Price: 60000, StopPrice: ptr(60000), FilledQty: 0.01, AvgPrice: 59990,
			Fee: 0.24, FeeAsset: "USDT", GroupID: "7", CreatedAt: time.Date(2026, 1, 1, 0, i%60, 0, 0, time.UTC),
			Reason: map[string]any{"leg": "stop", "stop": 60000}, Logs: []string{"trail", "moved"}}
	}
	return out
}

func TestOrdersPageWithReason(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/frontend-api/api/v1/dev/orders": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			assert.Equal(t, sid, q.Get("session_id"))
			assert.Equal(t, "50", q.Get("limit"), "with_reason caps the page")
			assert.Equal(t, "full", q.Get("fields"))
			assert.Equal(t, "stop_loss", q.Get("role"))
			assert.Equal(t, "event", q.Get("time"))
			jsonReply(ordersPage{Orders: ordersFor(2), Total: 120, Limit: 50, Offset: 0})(w, r)
		},
	})
	out, isErr := call(t, s, "get_session_orders", map[string]any{
		"session_id": sid, "role": "stop_loss", "time": "event", "limit": 200, "with_reason": true,
	})
	require.False(t, isErr, out)
	assert.Contains(t, out, "with_reason pages are at most 50 orders; limit lowered from 200.")
	assert.Contains(t, out, "Orders 1–2 of 120, newest first")
	assert.Contains(t, out, `{"leg":"stop","stop":60000}`+"\ttrail | moved")
	assert.Contains(t, out, "More: use offset=2, or save_to")
}

func TestOrdersSaveToPagesThroughEverything(t *testing.T) {
	all := ordersFor(2500) // newest first, as /orders pages them
	pages := 0
	s := backend(t, map[string]http.HandlerFunc{
		"/frontend-api/api/v1/dev/orders": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			assert.Equal(t, "summary", q.Get("fields"))
			assert.Equal(t, "BTC/USDT", q.Get("pair"))
			if pages++; pages == 2 {
				// The run places an order between two pages: everything
				// shifts down by one, so page two starts with page one's last.
				// The new order itself is on a page already read.
				all = append([]order{{ID: "new", Pair: "BTC/USDT"}}, all...)
			}
			off, _ := strconv.Atoi(q.Get("offset"))
			lim, _ := strconv.Atoi(q.Get("limit"))
			page := append([]order{}, all[off:min(off+lim, len(all))]...)
			for i := range page {
				page[i].Reason, page[i].Logs = nil, nil // summary leaves them out
			}
			jsonReply(ordersPage{Orders: page, Total: len(all), Limit: lim, Offset: off})(w, r)
		},
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.csv")
	out, isErr := call(t, s, "get_session_orders", map[string]any{"session_id": sid, "pair": "BTC/USDT", "save_to": path})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Saved 2500 orders")
	assert.Contains(t, out, "Oldest first")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2501)
	assert.Equal(t, strings.Join(orderCSVHeader, ","), lines[0])
	assert.Equal(t, "o2499,2026-01-01T00:39:00Z,,binance_futures,BTC/USDT,SELL,STOP_LOSS,stop_loss,FILLED,0.01,60000,60000,0.01,59990,0.24,USDT,7", lines[1])
	assert.True(t, strings.HasPrefix(lines[2500], "o0,"))
	ids := map[string]bool{}
	for _, l := range lines[1:] {
		id, _, _ := strings.Cut(l, ",")
		assert.False(t, ids[id], "order %s written twice", id)
		ids[id] = true
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".*"))
	assert.Empty(t, leftovers, "temporary file left behind")

	for _, bad := range []string{"orders.csv", filepath.Join(dir, "orders.txt"), filepath.Join(dir, "missing", "o.csv")} {
		out, isErr := call(t, s, "get_session_orders", map[string]any{"session_id": sid, "save_to": bad})
		assert.True(t, isErr, "%s accepted: %s", bad, out)
	}
}

// flatSeries is a constant series over [from, to) at step.
func flatSeries(from, to time.Time, step time.Duration) seriesResponse {
	var pts []seriesPoint
	for t := from; t.Before(to); t = t.Add(step) {
		pts = append(pts, seriesPoint{OpenTime: t.UnixMilli(), Value: 1000})
	}
	return seriesResponse{Points: pts}
}

func TestSeriesIsClampedToTheBacktestThenCoarsened(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(50 * time.Hour)
	s := backend(t, map[string]http.HandlerFunc{
		"/frontend-api/api/v1/dev/sessions/" + sid: jsonReply(sessionDetail{ID: sid, StartAt: &start, EndAt: &end}),
		"/analytics/api/v1/dev/analytics/series": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			assert.Equal(t, "5m", q.Get("timeframe"))
			assert.Equal(t, strconv.FormatInt(start.UnixMilli(), 10), q.Get("from"))
			assert.Equal(t, strconv.FormatInt(end.UnixMilli(), 10), q.Get("to"))
			assert.Equal(t, "binance_futures", q.Get("exchange"))
			jsonReply(flatSeries(start, end, 5*time.Minute))(w, r)
		},
	})
	out, isErr := call(t, s, "get_analytics_series", map[string]any{
		"session_id": sid, "session_type": "backtest", "type": "equity", "timeframe": "1m",
		"from": 0, "to": 4102444800000, "exchange": "binance_futures", "pair": "BTC/USDT",
	})
	require.False(t, isErr, out)
	assert.Contains(t, out, "1m would be 3000 buckets over the session (max 2000), so this is 5m.")
	assert.Contains(t, out, "equity · 5m · binance_futures BTC/USDT · 600 buckets")
	assert.Contains(t, out, "2026-01-01 00:05\t1000\n")
}

func TestLiveSeriesWindowComesFromDailyEquity(t *testing.T) {
	day0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/series": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("timeframe") == "1d" {
				jsonReply(flatSeries(day0, day0.AddDate(0, 0, 3), 24*time.Hour))(w, r)
				return
			}
			assert.Equal(t, "drawdown", q.Get("type"))
			assert.Equal(t, "5m", q.Get("timeframe"), "3 days at 1m is 4320 buckets")
			assert.Equal(t, strconv.FormatInt(day0.UnixMilli(), 10), q.Get("from"))
			assert.Equal(t, strconv.FormatInt(day0.AddDate(0, 0, 3).UnixMilli(), 10), q.Get("to"))
			jsonReply(flatSeries(day0, day0.AddDate(0, 0, 3), 5*time.Minute))(w, r)
		},
	})
	out, isErr := call(t, s, "get_analytics_series", map[string]any{
		"session_id": sid, "session_type": "live", "type": "drawdown", "timeframe": "1m", "from": 0, "to": 4102444800000,
	})
	require.False(t, isErr, out)
	assert.Contains(t, out, "so this is 5m.")
}

func TestNarrowSeriesGoesStraightThrough(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := backend(t, map[string]http.HandlerFunc{
		// No session route: a narrow range must not look up the window.
		"/analytics/api/v1/dev/analytics/series": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "1m", r.URL.Query().Get("timeframe"))
			jsonReply(flatSeries(from, from.Add(time.Hour), time.Minute))(w, r)
		},
	})
	out, isErr := call(t, s, "get_analytics_series", map[string]any{
		"session_id": sid, "session_type": "backtest", "type": "equity", "timeframe": "1m",
		"from": float64(from.UnixMilli()), "to": float64(from.Add(time.Hour).UnixMilli()), // JSON numbers
	})
	require.False(t, isErr, out)
	assert.Contains(t, out, "60 buckets")
	assert.NotContains(t, out, "so this is")
}

func TestPositionsArePaged(t *testing.T) {
	opened := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	s := backend(t, map[string]http.HandlerFunc{
		"/frontend-api/api/v1/dev/sessions/" + sid + "/positions/page": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			assert.Equal(t, "200", q.Get("limit"))
			assert.Equal(t, "400", q.Get("offset"))
			jsonReply(positionsPage{Positions: []position{{Exchange: "binance_futures", Pair: "BTC/USDT", Side: "LONG", Size: 0.01,
				EntryPrice: 60000, Leverage: 3, RealizedPnL: 12.5, OpenedAt: opened}}, Total: 950, Limit: 200, Offset: 400, Open: 1, Liquidated: 2})(w, r)
		},
	})
	out, isErr := call(t, s, "get_session_positions", map[string]any{"session_id": sid, "offset": 400})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Positions 401–401 of 950, oldest first · 1 open at the end · 2 liquidated")
	assert.Contains(t, out, "2026-01-01 08:00\t—\tbinance_futures\tBTC/USDT\tLONG\t0.01\t60000\t3\t0\t12.5\t0\t\n")
	assert.Contains(t, out, "More: use offset=401.")

	_, isErr = call(t, s, "get_session_positions", map[string]any{"session_id": sid, "pair": "BTC/USDT"})
	assert.True(t, isErr, "pair without exchange")
}

func TestSessionListAndDetail(t *testing.T) {
	created := time.Date(2026, 10, 22, 7, 27, 0, 0, time.UTC)
	s := backend(t, map[string]http.HandlerFunc{
		"/frontend-api/api/v1/dev/sessions": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "2", r.URL.Query().Get("limit"))
			assert.Equal(t, "4", r.URL.Query().Get("offset"))
			jsonReply([]sessionSummary{
				{ID: sid, Status: "DONE", Exchange: "binance_futures", Exchanges: []string{"binance_futures"}, Pair: "BTC/USDT", Timeframe: "1h",
					CreatedAt: created, Tags: []string{"baseline"}, IsFavorite: true, Progress: &sessionProgress{Percent: 100}},
				{ID: "other", Name: "tab\tname", Status: "PROCESSING", Exchange: "bybit", Pair: "ETH/USDT", Timeframe: "15m", CreatedAt: created},
			})(w, r)
		},
		"/frontend-api/api/v1/dev/sessions/" + sid: jsonReply(sessionDetail{
			ID: sid, Status: "DONE", Timeframe: "1h", Exchange: "binance_futures", Exchanges: []string{"binance_futures"},
			Wallets:     []wallet{{Exchange: "binance_futures", Asset: "USDT", Balance: 472.13741}},
			Instruments: []instrument{{Exchange: "binance_futures", Pair: "AVAX/USDT", OrderCount: 132, Filled: 122}, {Exchange: "binance_futures", Pair: "ZEC/USDT"}},
		}),
	})
	out, isErr := call(t, s, "list_sessions", map[string]any{"limit": 2, "offset": 4})
	require.False(t, isErr, out)
	assert.Contains(t, out, sid+"\t\tDONE\t100%\tbinance_futures\tBTC/USDT\t1h\t2026-10-22 07:27\tbaseline,★\n")
	assert.Contains(t, out, "other\ttab name\tPROCESSING\t—\tbybit\tETH/USDT\t15m")
	assert.Contains(t, out, "More may follow: offset=6.")

	out, isErr = call(t, s, "get_session_detail", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Session "+sid+" · DONE · 1h · 2 instruments on binance_futures\n")
	assert.Contains(t, out, "## wallets: at the end of a finished run")
	assert.Contains(t, out, "binance_futures\tAVAX/USDT\t132\t122\t")
}

func TestToolCallsHaveADeadline(t *testing.T) {
	s := server.NewMCPServer("test", "0")
	var bounded bool
	add(s, mcp.NewTool("probe"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, bounded = ctx.Deadline()
		return toolText(""), nil
	})
	call(t, s, "probe", nil)
	assert.True(t, bounded)
}

func ptr(v float64) *float64 { return &v }

// close_session ends the session on the engine; one the engine no longer
// holds is reported, not an error.
func TestCloseSession(t *testing.T) {
	closed := 0
	s := backend(t, map[string]http.HandlerFunc{
		"/api/v1/dev/session/" + sid + "/close": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.NotEmpty(t, r.Header.Get("X-API-KEY"), "signed for the engine")
			closed++
			jsonReply(map[string]any{"id": sid, "status": "FAILED", "closed": true})(w, r)
		},
		"/api/v1/dev/session/22222222-2222-3333-4444-555555555555/close": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"session not found","code":"session_unknown"}`, http.StatusNotFound)
		},
	})
	out, isErr := call(t, s, "close_session", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	assert.Contains(t, out, `"closed":true`)
	assert.Equal(t, 1, closed)

	out, isErr = call(t, s, "close_session", map[string]any{"session_id": "22222222-2222-3333-4444-555555555555"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "the engine holds no such session")
}

// delete_session closes a live session on the engine before deleting its
// record, so it stops holding a slot; one the engine no longer holds is just
// deleted.
func TestDeleteSessionClosesFirst(t *testing.T) {
	var order []string
	s := backend(t, map[string]http.HandlerFunc{
		"/api/v1/dev/session/" + sid + "/close": func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "close")
			http.Error(w, `{"code":"session_unknown"}`, http.StatusNotFound)
		},
		"/frontend-api/api/v1/dev/sessions/" + sid: func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodDelete, r.Method)
			order = append(order, "delete")
			w.WriteHeader(http.StatusNoContent)
		},
	})
	out, isErr := call(t, s, "delete_session", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	assert.Equal(t, []string{"close", "delete"}, order)
}

// get_data_coverage lists each market's first and last bar, filtered.
func TestDataCoverage(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/data/api/v1/metadata": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"binance":{"symbols":{"BTCUSDT":{"start":1740787200000,"end":1793050380000},"ETHUSDT":{"start":1740787200000,"end":1793050380000}}},` +
				`"binance_futures":{"symbols":{"BTCUSDT":{"start":1704067200000,"end":1793050380000}}}}`))
		},
	})
	out, isErr := call(t, s, "get_data_coverage", map[string]any{"symbol": "BTC/USDT"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "binance\tBTCUSDT\t2025-03-01\t")
	assert.Contains(t, out, "binance_futures\tBTCUSDT\t2024-01-01\t")
	assert.NotContains(t, out, "ETHUSDT")

	out, _ = call(t, s, "get_data_coverage", map[string]any{"exchange": "kraken"})
	assert.Contains(t, out, "No history")
}
