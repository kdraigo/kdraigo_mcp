package tools

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tradesFor(n int) []trade {
	out := make([]trade, n)
	entry := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range out {
		exit := entry.Add(time.Duration(i+1) * time.Hour)
		out[i] = trade{ID: i + 1, Exchange: "binance_futures", Pair: "BTC/USDT", Side: "long", EntryAt: entry, ExitAt: &exit,
			HoldSec: int64(i+1) * 3600, EntryPx: 100, ExitPx: ptr(110), QtyEntry: 1, QtyPeak: 2, Adds: 1,
			Gross: 10, Fees: 0.2, Funding: 0.1, Net: 9.7, StopPx: ptr(95), Risk: ptr(5), NetR: ptr(1.94), RSource: "stop_order",
			ExitKind: "take_profit", Reconciled: ptr2(i != 1)}
	}
	return out
}

func ptr2(b bool) *bool { return &b }

func TestTradesPage(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/trades": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			assert.Equal(t, "net_r", q.Get("sort"))
			assert.Equal(t, "stop_loss", q.Get("exit_kind"))
			assert.Equal(t, "1700000000000", q.Get("from"))
			assert.Equal(t, "100", q.Get("limit"))
			jsonReply(tradesPage{ReportVersion: 2, Total: 340, Limit: 100, Trades: tradesFor(3), Warnings: []reportWarning{
				{Code: "incomplete_stream", Severity: "high", Message: "80 of 100 candles replayed"},
				{Code: "few_trades", Severity: "medium", Message: "Only 3 closed trades"},
			}})(w, r)
		},
	})
	out, isErr := call(t, s, "get_trades", map[string]any{"session_id": sid, "sort": "net_r", "exit_kind": "stop_loss", "from": float64(1700000000000)})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Trades 1–3 of 340, by net_r")
	assert.Contains(t, out, "[high] 80 of 100 candles replayed\n1 more warning(s) in get_session_report.")
	assert.Contains(t, out, "1\t2026-01-01 00:00\t2026-01-01 01:00\tbinance_futures BTC/USDT\tlong\t100\t110\t2\t1/0\t+10.00\t0.20\t0.10\t+9.70\t95\t+1.94\ttake_profit\t1.0h\t\n")
	assert.Contains(t, out, "\tunrec\n", "the second trade disagrees with its position")
	assert.Contains(t, out, "More: use offset=3")
}

func TestTradesNeedTheNewServer(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/trades": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "404 page not found", http.StatusNotFound) // gin's own, no JSON
		},
		"/analytics/api/v1/dev/analytics/breakdown": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"session not found"}`))
		},
	})
	out, isErr := call(t, s, "get_trades", map[string]any{"session_id": sid})
	assert.True(t, isErr)
	assert.Contains(t, out, "has no /analytics/trades yet")
	out, isErr = call(t, s, "get_breakdown", map[string]any{"session_id": sid, "by": "month"})
	assert.True(t, isErr)
	assert.Contains(t, out, "session not found", "a missing session is not a missing route")
}

func TestTradesSaveTo(t *testing.T) {
	all := tradesFor(1500)
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/trades": func(w http.ResponseWriter, r *http.Request) {
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			jsonReply(tradesPage{ReportVersion: 2, Total: len(all), Limit: lim, Offset: off, Trades: all[off:min(off+lim, len(all))]})(w, r)
		},
	})
	path := filepath.Join(t.TempDir(), "trades.csv")
	out, isErr := call(t, s, "get_trades", map[string]any{"session_id": sid, "save_to": path})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Saved 1500 of 1500 trades")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1501)
	assert.Equal(t, strings.Join(tradeCSVHeader, ","), lines[0])
	assert.Equal(t, "1,binance_futures,BTC/USDT,long,2026-01-01T00:00:00Z,2026-01-01T01:00:00Z,3600,100,110,1,2,1,0,10,0.2,0.1,9.7,95,,5,1.94,,stop_order,take_profit,false,false,false,false,true,,,", lines[1])
}

func TestBreakdownTable(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/breakdown": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			assert.Equal(t, "weekday", q.Get("by"))
			assert.Equal(t, "entry", q.Get("at"))
			jsonReply(breakdownResponse{By: "weekday", At: "entry", Buckets: []bucket{
				{Key: "1-Mon", Trades: 4, Wins: 1, WinRate: ptr(0.25), Net: -3.5, Gross: -2, Fees: 1, Funding: 0.5, ProfitFactor: ptr(0.6), AvgR: ptr(-0.4), RSE: ptr(0.3), SumR: ptr(-1.6), RTrades: 4},
				{Key: "2-Tue"},
			}, Total: bucket{Key: "total", Trades: 4, Net: -3.5}})(w, r)
		},
	})
	out, isErr := call(t, s, "get_breakdown", map[string]any{"session_id": sid, "by": "weekday", "at": "entry"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "each placed by its opening time")
	assert.Contains(t, out, "1-Mon\t4\t25%\t-3.50\t-2.00\t1.00\t0.50\t0.60\t-0.40\t0.30\t-1.6\t4\n")
	assert.Contains(t, out, "2-Tue\t0\t—\t0.00\t0.00\t0.00\t0.00\t—\t—\t—\t—\t0\n")
	assert.Contains(t, out, "total\t4\t")
}

// A version 2 report brings its own figures and warnings.
func TestSessionReportV2(t *testing.T) {
	rep := reportFixture()
	rep.ReportVersion, rep.InstrumentsPositive = 2, 2
	rep.Total.AvgR, rep.Total.RSE, rep.Total.SumR, rep.Total.RTrades, rep.Total.WinnersOver5R = ptr(0.345), ptr(0.262), ptr(313.4), 12, 1
	rep.Total.NetWithoutTop5, rep.Total.Top5Share, rep.Total.LongestLosingStreak = ptr(-43.74), ptr(1.254), 54
	rep.Total.LongestUnderwaterDays, rep.Total.ExposurePct, rep.Total.AvgHoldSec = ptr(240.8), ptr(98.8), ptr(6*3600)
	rep.Total.Months, rep.Total.LosingMonths, rep.Total.WorstMonth = 16, 7, &monthNet{Key: "2026-03", Net: -41.86}
	rep.Instruments[0].ReturnToDD, rep.Instruments[0].AvgR = ptr(3.2), ptr(0.5)
	rep.Ratios = ratios{Days: 471, Sharpe: ptr(1.013), Sortino: ptr(3.714), CAGR: ptr(42.11), Calmar: ptr(1.302)}
	rep.Warnings = []reportWarning{{Code: "concentration", Severity: "medium", Message: "The 5 best trades are 125% of net P&L."}}
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(rep),
		"/frontend-api/api/v1/dev/sessions/" + sid:    jsonReply(sessionDetail{ID: sid, Status: "DONE"}),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
	})
	out, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, out)
	for _, want := range []string{
		"R (net, per trade with an initial stop): avg +0.345 ± 0.262 (std err) over 12 of 12 trades · sum +313.4 · winners over 5R 1",
		"Without the 5 best trades net is -43.74 (they are 125% of net) · longest losing streak 54 · longest underwater 240.8 days · exposure 99% · avg hold 6.0h",
		"Daily ratios over 471 days: Sharpe 1.01 · Sortino 3.71 · CAGR +42.1% · Calmar 1.30 · 16 months, 7 losing, worst 2026-03 -41.86 · instruments positive 2 of 2",
		"## warnings\n- [medium] The 5 best trades are 125% of net P&L.\n\n",
		"\t3.20\t+0.50\t",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "Only 12 closed trades", "the server's warnings replace the tool's")
}
