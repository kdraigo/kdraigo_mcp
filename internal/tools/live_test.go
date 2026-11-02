package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/config"
)

const liveBase = "/live/api/v1/dev/live-sessions"

func liveFixture() []liveSession {
	beat := time.Now().Add(-4 * time.Second)
	name, env := "kd-sub2", "real_binance_futures"
	stopped, reason := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), "user stop"
	return []liveSession{
		{SessionID: "old-session", Exchange: "binance_futures", Symbol: "ETH/USDT", Status: "STOPPED",
			StartedAt: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC), StoppedAt: &stopped, StoppedReason: &reason,
			InitialBalances: []liveBalance{{Asset: "USDT", Free: 300}}, CurrentBalances: []liveBalance{{Asset: "USDT", Free: 280}}},
		{SessionID: sid, Exchange: "binance_futures", Symbol: "BTC/USDT", Status: "RUNNING",
			StartedAt: time.Date(2026, 10, 15, 13, 3, 0, 0, time.UTC), LastHeartbeatAt: &beat,
			UptimeSeconds: 16*86400 + 4*3600, CandlesProcessed: 23000, OrdersPlaced: 6,
			StrategyName: &name, Environment: &env, StrategyConfig: json.RawMessage(`{"risk": 0.025, "half_at_r": 2}`),
			InitialBalances: []liveBalance{{Asset: "USDT", Free: 300}},
			CurrentBalances: []liveBalance{{Asset: "USDT", Free: 290.5, Locked: 22.25}, {Asset: "BNB", Free: 0}}},
	}
}

func liveOrdersFixture() []liveOrder {
	at := func(h int) time.Time { return time.Date(2026, 10, 31, h, 0, 0, 0, time.UTC) }
	return []liveOrder{
		{OrderID: "1", Exchange: "binance_futures", Symbol: "BTC/USDT", Side: "BUY", Type: "MARKET", Status: "FILLED",
			Qty: 0.01, FilledQty: 0.01, AvgPrice: 100000, Fee: 0.4, FeeAsset: "USDT", CreatedAt: at(1), UpdatedAt: at(1)},
		{OrderID: "2", Exchange: "binance_futures", Symbol: "BTC/USDT", Side: "SELL", Type: "STOP_MARKET", Status: "NEW",
			Qty: 0.01, StopPrice: 98000, ReduceOnly: true, GroupID: "g1", CreatedAt: at(2), UpdatedAt: at(2)},
		{OrderID: "3", Exchange: "binance_futures", Symbol: "BTC/USDT", Side: "SELL", Type: "LIMIT", Status: "NEW",
			Qty: 0.005, Price: 104000, ReduceOnly: true, GroupID: "g1", CreatedAt: at(3), UpdatedAt: at(3)},
		{OrderID: "4", Exchange: "binance_futures", Symbol: "ETH/USDT", Side: "SELL", Type: "MARKET", Status: "FILLED",
			Qty: 1, FilledQty: 1, AvgPrice: 3000, CreatedAt: at(4), UpdatedAt: at(4)},
		{OrderID: "5", Exchange: "binance_futures", Symbol: "ETH/USDT", Side: "BUY", Type: "LIMIT", Status: "CANCELLED",
			Qty: 1, Price: 2900, CreatedAt: at(5), UpdatedAt: at(6)},
	}
}

func liveReport() performanceReport {
	rep := reportFixture()
	rep.ReportVersion = 3
	rep.Total.OpenAtEnd = 2
	rep.Total.Open = &openBlock{Positions: []openPosition{
		{Exchange: "binance_futures", Pair: "BTC/USDT", Side: "long", Qty: 0.01, AvgEntry: 100000, Fees: 0.4},
		{Exchange: "binance_futures", Pair: "ETH/USDT", Side: "short", Qty: 1, AvgEntry: 3000},
	}, Fees: 0.4}
	return rep
}

// marks: BTC has a mark bar, ETH's mark series is empty, so it falls back to
// the last 1m close.
func liveMarks(t *testing.T) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/data/api/v1/mark-candles": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("symbol") {
			case "BTC/USDT":
				jsonReply([]map[string]any{{"close": 101000.0, "open_time": int64(1_761_900_000_000)}})(w, r)
			default:
				jsonReply([]map[string]any{})(w, r)
			}
		},
		"/data/api/v1/candles": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "ETH/USDT", r.URL.Query().Get("symbol"))
			jsonReply([]map[string]any{{"close": 2950.0, "close_time": time.Now().Add(-30 * time.Second).UnixMilli()}})(w, r)
		},
	}
}

func merge(ms ...map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	out := map[string]http.HandlerFunc{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestLiveSessionList(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	list := liveFixture()
	beat := now.Add(-4 * time.Second)
	list[1].LastHeartbeatAt = &beat
	out := renderLiveSessions([]liveSession{list[1], list[0]}, 20, 0, now)
	for _, want := range []string{
		"Live sessions 1–2 of 2, newest first",
		sid + "\tkd-sub2\treal_binance_futures\tbinance_futures\tBTC/USDT\tRUNNING\t2026-10-15 13:03\t4 s ago\t16d 4h\t6\t23000\tUSDT 312.75\tUSDT 300.00\n",
		"old-session\t—\t—\tbinance_futures\tETH/USDT\tSTOPPED\t2026-10-02 08:00\tnever\t0 s\t0\t0\tUSDT 280.00\tUSDT 300.00\n",
		"Next: get_session_detail session_type=live",
	} {
		assert.Contains(t, out, want)
	}
	assert.Contains(t, renderLiveSessions(nil, 20, 0, now), "No live sessions on this account")

	// Through the tool: newest first, from live_trades' signed route.
	s := backend(t, map[string]http.HandlerFunc{liveBase: func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("X-Signature"), "signed")
		jsonReply(liveFixture())(w, r)
	}})
	out, isErr := call(t, s, "list_sessions", map[string]any{"session_type": "live"})
	require.False(t, isErr, out)
	assert.Less(t, strings.Index(out, sid), strings.Index(out, "old-session"), "newest first")

	_, isErr = call(t, s, "list_sessions", map[string]any{"session_type": "paper"})
	assert.True(t, isErr)
}

func TestLiveDetail(t *testing.T) {
	s := backend(t, merge(liveMarks(t), map[string]http.HandlerFunc{
		liveBase:                         jsonReply(liveFixture()),
		liveBase + "/" + sid + "/orders": jsonReply(liveOrdersPage{Orders: liveOrdersFixture(), Total: 5}),
		"/analytics/api/v1/dev/analytics/performance": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "live", r.URL.Query().Get("session_type"))
			jsonReply(liveReport())(w, r)
		},
	}))
	out, isErr := call(t, s, "get_session_detail", map[string]any{"session_id": sid, "session_type": "live"})
	require.False(t, isErr, out)
	for _, want := range []string{
		"Live session " + sid + " · strategy kd-sub2 · real_binance_futures · RUNNING · last heartbeat ",
		"up 16d 4h · candles processed 23000 · orders placed 6 · first symbol binance_futures BTC/USDT",
		"USDT\t290.50\t22.25\t312.75\t300.00\t+12.75\n",
		"## open now: 2 position(s), unrealized +60.00 at the price now",
		// BTC long at its mark: +10 (0.01 × 1000); its stop and target are the resting bracket.
		"binance_futures\tBTC/USDT\tlong\t0.010000\t100000\t101000\t",
		"\tmark\t+10.00\t98000\t104000\t",
		// ETH short at the last trade, nothing resting: the canceled BUY doesn't count.
		"binance_futures\tETH/USDT\tshort\t1.000000\t3000\t2950\t",
		"\tlast trade\t+50.00\tnone\t—\t",
		"## last 5 of 5 orders, newest first",
		"5\t2026-10-31 05:00\tETH/USDT\tBUY\tLIMIT\tlimit\tCANCELLED\t",
		"2\t2026-10-31 02:00\tBTC/USDT\tSELL\tSTOP_MARKET\tstop_loss\tNEW\t",
		"3\t2026-10-31 03:00\tBTC/USDT\tSELL\tLIMIT\ttake_profit\tNEW\t",
		`{"half_at_r":2,"risk":0.025}`,
		"Next: get_session_report session_id=" + sid + " session_type=live",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "Stopped ")

	out, isErr = call(t, s, "get_session_detail", map[string]any{"session_id": "nope", "session_type": "live"})
	assert.True(t, isErr)
	assert.Contains(t, out, "no live session nope on this account")
}

// Analytics down: the bot's status, wallet and orders still come back.
func TestLiveDetailWithoutResults(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		liveBase:                         jsonReply(liveFixture()),
		liveBase + "/old-session/orders": jsonReply(liveOrdersPage{}),
		"/analytics/api/v1/dev/analytics/performance": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 502) },
	})
	out, isErr := call(t, s, "get_session_detail", map[string]any{"session_id": "old-session", "session_type": "live"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Stopped 2026-10-20 09:00 UTC: user stop")
	assert.Contains(t, out, "## open now: not available (live results: HTTP 502")
	assert.Contains(t, out, "## orders: none yet")
}

func TestLiveRole(t *testing.T) {
	for _, c := range []struct {
		o    liveOrder
		want string
	}{
		{liveOrder{Type: "MARKET"}, "market"},
		{liveOrder{Type: "LIMIT"}, "limit"},
		{liveOrder{Type: "LIMIT", ReduceOnly: true}, "take_profit"},
		{liveOrder{Type: "LIMIT", GroupID: "g"}, "take_profit"},
		{liveOrder{Type: "STOP_MARKET", GroupID: "g"}, "stop_loss"},
		{liveOrder{Type: "TAKE_PROFIT_MARKET"}, "take_profit"},
		{liveOrder{Type: "LIQUIDATION"}, "liquidation"},
	} {
		assert.Equal(t, c.want, liveRole(&c.o), "%+v", c.o)
	}
}

func TestLiveOrdersFiltersReasonsAndExport(t *testing.T) {
	reasons := 0
	s := backend(t, map[string]http.HandlerFunc{
		liveBase + "/" + sid + "/orders": jsonReply(liveOrdersPage{Orders: liveOrdersFixture(), Total: 5}),
		liveBase + "/" + sid + "/orders/2/reasoning": func(w http.ResponseWriter, r *http.Request) {
			reasons++
			jsonReply(map[string]any{"reasoning": map[string]any{"why": "stop under the swing low"}, "logs": []string{"armed", "placed"}})(w, r)
		},
		liveBase + "/" + sid + "/orders/3/reasoning": func(w http.ResponseWriter, r *http.Request) {
			reasons++
			http.Error(w, `{"error":"no reasoning recorded for this order"}`, 404)
		},
	})
	get := func(args map[string]any) string {
		t.Helper()
		args["session_id"], args["session_type"] = sid, "live"
		out, isErr := call(t, s, "get_session_orders", args)
		require.False(t, isErr, out)
		return out
	}

	out := get(map[string]any{})
	assert.Contains(t, out, "Orders 1–5 of 5, newest first")
	assert.Less(t, strings.Index(out, "\n5\t"), strings.Index(out, "\n1\t"), "newest first")

	out = get(map[string]any{"pair": "BTCUSDT", "status": "NEW"})
	assert.Contains(t, out, "Orders 1–2 of 2")
	assert.Contains(t, out, "2\t2026-10-31 02:00\t—\tbinance_futures\tBTC/USDT\tSELL\tSTOP_MARKET\tstop_loss\tNEW\t0.01\t0\t98000\t")

	out = get(map[string]any{"status": "CANCELED"})
	assert.Contains(t, out, "Orders 1–1 of 1", "CANCELLED is CANCELED")

	out = get(map[string]any{"role": "take_profit,stop_loss", "with_reason": true})
	assert.Contains(t, out, "Orders 1–2 of 2")
	assert.Contains(t, out, `{"why":"stop under the swing low"}`+"\tarmed | placed")
	assert.Equal(t, 2, reasons, "one read an order on the page")

	// event time: when it filled or was canceled; resting orders have none.
	from := time.Date(2026, 10, 31, 5, 30, 0, 0, time.UTC).UnixMilli()
	out = get(map[string]any{"time": "event", "from": float64(from)})
	assert.Contains(t, out, "Orders 1–1 of 1")
	assert.Contains(t, out, "\n5\t")

	out = get(map[string]any{"limit": float64(2), "offset": float64(4)})
	assert.Contains(t, out, "Orders 5–5 of 5")

	path := filepath.Join(t.TempDir(), "live.csv")
	out = get(map[string]any{"save_to": path, "pair": "ETH/USDT"})
	assert.Contains(t, out, "Saved 2 orders")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.Len(t, lines, 3)
	assert.True(t, strings.HasPrefix(lines[1], "4,2026-10-31T04:00:00Z,2026-10-31T04:00:00Z,binance_futures,ETH/USDT,SELL,MARKET,market,FILLED,"), "oldest first: %s", lines[1])
	assert.True(t, strings.HasPrefix(lines[2], "5,2026-10-31T05:00:00Z,,binance_futures,ETH/USDT,BUY,LIMIT,limit,CANCELLED,"), "a canceled order has no executed_at: %s", lines[2])
}

// live_trades pages by position, oldest first: the tool reads every page.
func TestLiveOrdersReadsEveryPage(t *testing.T) {
	const n = 1500
	all := make([]liveOrder, n)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range all {
		all[i] = liveOrder{OrderID: strconv.Itoa(i), Exchange: "binance_futures", Symbol: "BTC/USDT", Side: "BUY", Type: "LIMIT",
			Status: "FILLED", CreatedAt: start.Add(time.Duration(i) * time.Minute)}
	}
	pages := 0
	s := backend(t, map[string]http.HandlerFunc{liveBase + "/" + sid + "/orders": func(w http.ResponseWriter, r *http.Request) {
		pages++
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		jsonReply(liveOrdersPage{Orders: all[off:min(off+lim, n)], Total: n})(w, r)
	}})
	out, isErr := call(t, s, "get_session_orders", map[string]any{"session_id": sid, "session_type": "live", "limit": float64(1)})
	require.False(t, isErr, out)
	assert.Equal(t, 2, pages)
	assert.Contains(t, out, fmt.Sprintf("Orders 1–1 of %d", n))
	assert.Contains(t, out, fmt.Sprintf("\n%d\t", n-1), "the newest")
}

func TestLiveReportValuesOpenPositionsNow(t *testing.T) {
	s := backend(t, merge(liveMarks(t), map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(liveReport()),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
	}))
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid, "session_type": "live"})
	require.False(t, isErr, out)
	for _, want := range []string{
		"## open now: 2 position(s), left out of every figure above",
		"Unrealized +60.00 at the price now · paid on them so far: fees 0.40",
		"binance_futures\tBTC/USDT\tlong\t0.010000\t100000\t101000\t",
		"\tmark\n",
		"\tlast trade\n",
		"source: the venue's mark",
		"Live session: funding is not in the live record",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "open at the end")
	assert.NotContains(t, out, "funding 0.00 ·")
}

// The live-bot reads against the real platform, as Claude Code calls them:
// the caller's live sessions, then the newest running one's page, orders and
// report. Reads only; uses ~/.kdraigo/config.yaml.
//
//	KDRAIGO_MCP_LIVE=1 KDRAIGO_MCP_LIVE_BOTS=1 go test ./internal/tools/ -run BotsProd -v
func TestLiveBotsProd(t *testing.T) {
	if os.Getenv("KDRAIGO_MCP_LIVE") == "" || os.Getenv("KDRAIGO_MCP_LIVE_BOTS") == "" {
		t.Skip("KDRAIGO_MCP_LIVE and KDRAIGO_MCP_LIVE_BOTS not set")
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	signer, err := auth.NewSigner(cfg.Auth.KeyID, cfg.Auth.PrivateKey)
	require.NoError(t, err)
	s := server.NewMCPServer("live", "0")
	Register(s, Deps{HTTP: client.NewHTTP(cfg.Endpoint, cfg.BacktesterEndpoint, signer)})

	out, isErr := call(t, s, "list_sessions", map[string]any{"session_type": "live"})
	t.Logf("list_sessions live:\n%s", out)
	require.False(t, isErr)
	running := regexp.MustCompile(`(?m)^([0-9a-f-]{36})\t[^\n]*\tRUNNING\t`).FindStringSubmatch(out)
	if running == nil {
		t.Skip("no running live session on this account")
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_session_detail", map[string]any{}},
		{"get_session_orders", map[string]any{"limit": float64(10)}},
		{"get_session_report", map[string]any{}},
	} {
		c.args["session_id"], c.args["session_type"] = running[1], "live"
		out, isErr := call(t, s, c.tool, c.args)
		t.Logf("%s:\n%s", c.tool, out)
		require.False(t, isErr, c.tool)
	}
}
