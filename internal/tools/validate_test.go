package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

// sigFixture is a 471-day session with a modest, significant Sharpe that
// is partly the market's.
func sigFixture() map[string]any {
	return map[string]any{
		"session_id": sid, "source": "mark_to_market", "from": "2025-01-01", "days": 471, "years": 1.29,
		"sharpe": 1.01, "sharpe_autocorr_adjusted": 0.88, "sharpe_autocorr_lags": 10, "sharpe_p5": 0.12, "sharpe_p95": 1.93,
		"psr_zero": 0.968, "min_trl_days_zero": 410.0, "skew": -0.31, "kurtosis": 7.1, "worst_day_pct": -4.2,
		"cvar95_daily_pct": -2.1, "ulcer_index": 6.3, "trials": 23, "chance_max_sharpe": 1.74, "min_backtest_years": 3.8,
		"market": map[string]any{"beta": 0.42, "correlation": 0.55, "alpha_pct": 12.3, "information_ratio": 0.4, "benchmark_sharpe": 0.9},
		"notes":  []string{}, "warnings": []any{},
	}
}

func mcFixture() map[string]any {
	d := func(p5, p50, p95 float64) map[string]any {
		return map[string]any{"p5": p5, "p25": p50 * 0.8, "p50": p50, "p75": p50 * 1.2, "p95": p95, "mean": p50}
	}
	return map[string]any{
		"session_id": sid, "method": "trades", "basis": "net_fraction", "sample": 908, "session_risk_pct": 2.4, "concurrency": 1.05,
		"actual": map[string]any{"return_pct": 17.2, "max_drawdown_pct": 13.1, "drawdown_percentile": 0.18},
		"result": map[string]any{
			"method": "bootstrap", "paths": 5000, "length": 908,
			"final_return_pct": d(-6, 17, 45), "max_drawdown_pct": d(9, 16.5, 27.8), "longest_run": d(30, 48, 80),
			"drawdown_probability": []map[string]any{{"at_least_pct": 10, "p": 0.93}, {"at_least_pct": 20, "p": 0.31}, {"at_least_pct": 50, "p": 0.0}},
			"prob_loss":            0.08, "prob_ruin": 0, "ruin_pct": 50,
		},
		"notes": []string{}, "warnings": []any{},
	}
}

func perfFixture() map[string]any {
	return map[string]any{
		"session_id": sid, "quote": "USDT", "initial_capital": 1000, "final_equity": 1172, "report_version": 3,
		"instruments_positive": 8, "ratios": map[string]any{"days": 471, "sharpe": 1.01},
		"total": map[string]any{"net_pnl": 172, "gross_pnl": 227.3, "fees": 43.1, "funding": 12.1, "trades": 908,
			"net_without_top5": 40.0, "top5_share": 0.77, "months": 16, "losing_months": 5},
		"instruments": make([]map[string]any, 12),
		"warnings":    []any{},
	}
}

func TestGetMonteCarlo(t *testing.T) {
	var got string
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/montecarlo": func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.RawQuery
			jsonReply(mcFixture())(w, r)
		},
	})
	text, isErr := call(t, s, "get_monte_carlo", map[string]any{"session_id": sid, "risk_pct": 1.75, "dd_pct": 17.0, "method": "trades"})
	require.False(t, isErr, text)
	for _, q := range []string{"risk_pct=1.75", "dd_pct=17", "method=trades", "session_type=backtest"} {
		assert.Contains(t, got, q)
	}
	assert.NotContains(t, got, "paths=", "only what was asked is sent")
	assert.Contains(t, text, "5000 paths × 908 trades")
	assert.Contains(t, text, "median 2.40% per trade")
	assert.Contains(t, text, "max drawdown %\t9.0\t13.2\t16.5\t19.8\t27.8")
	assert.Contains(t, text, "≥20% 31.0%")
	assert.Contains(t, text, "at the 18th percentile")
	assert.Equal(t, []string{"1st", "2nd", "3rd", "4th", "11th", "12th", "13th", "21st", "95th"}, []string{ordinal(1), ordinal(2), ordinal(3), ordinal(4), ordinal(11), ordinal(12), ordinal(13), ordinal(21), ordinal(95)})
	assert.Contains(t, text, "lucky")
	assert.Contains(t, text, "Plan for p95 (27.8%)")
}

func TestGetSignificance(t *testing.T) {
	var got string
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/significance": func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.RawQuery
			jsonReply(sigFixture())(w, r)
		},
	})
	text, isErr := call(t, s, "get_significance", map[string]any{"session_id": sid, "trials": 23.0, "session_type": "live"})
	require.False(t, isErr, text)
	assert.Contains(t, got, "trials=23")
	assert.Contains(t, got, "session_type=live")
	assert.Contains(t, text, "P(true Sharpe > 0) 0.968")
	assert.Contains(t, text, "410 days (have 471)")
	assert.Equal(t, "over 100 years", trlText(f64(3_219_951)))
	assert.Equal(t, "never at this Sharpe", trlText(nil))
	assert.Contains(t, text, "beta 0.42")
	assert.Contains(t, text, "best of 23 skill-less variants reaches a Sharpe of 1.74")
}

func TestValidationOnAnOlderPlatform(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/montecarlo": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
	})
	text, isErr := call(t, s, "get_monte_carlo", map[string]any{"session_id": sid})
	assert.True(t, isErr)
	assert.Contains(t, text, "does not have /analytics/montecarlo yet")
}

func TestValidateStrategy(t *testing.T) {
	lo, hi := 5.0, 500.0
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance":  jsonReply(perfFixture()),
		"/analytics/api/v1/dev/analytics/significance": jsonReply(sigFixture()),
		"/analytics/api/v1/dev/analytics/montecarlo":   jsonReply(mcFixture()),
		"/workspace/api/v1/dev/workspaces/ws-1/params": jsonReply(map[string]any{"params": []map[string]any{
			{"name": "FAST", "kind": "int", "default": "50", "min": lo, "max": hi},
			{"name": "STOP_ATR", "kind": "float", "default": "1.5"},
			{"name": "CAPITAL", "kind": "float", "default": "1000"},
			{"name": "PAIRS", "kind": "list", "default": "BTC/USDT"},
		}}),
	})
	text, isErr := call(t, s, "validate_strategy", map[string]any{"session_id": sid, "trials": 23.0, "workspace_id": "ws-1"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "sample\tok\t908 closed trades over 471 days")
	assert.Contains(t, text, "significance\tok\tSharpe 1.01 (adj 0.88) · P(>0) 0.968 · needs 410 days, has 471 days")
	assert.Contains(t, text, "search luck\tweak\tSharpe 1.01 vs 1.74 by chance from 23 trials", "a Sharpe under the chance figure is weak")
	assert.Contains(t, text, "Monte Carlo\tcaution\tDD p50 16.5% / p95 27.8% · ends down 8.0% · backtest DD 13.1%")
	assert.Contains(t, text, "concentration\tcaution", "top 5 carry 77%")
	assert.Contains(t, text, "market\tcaution", "beta 0.42 is a large share")
	assert.Contains(t, text, "costs\tok", "fees + funding are 24% of gross")
	assert.Contains(t, text, "overfitting\tnot measured")
	assert.Contains(t, text, "Verdict: not an edge yet: 1 weak")
	// The sweep varies the strategy's numeric settings, not the capital or the pairs.
	assert.Contains(t, text, "Sensitivity sweep of FAST, STOP_ATR")
	assert.Contains(t, text, `{"FAST":"50","STOP_ATR":"1.5"}`)
	assert.Contains(t, text, `{"FAST":"40","STOP_ATR":"1.5"}`)
	assert.Contains(t, text, `{"FAST":"50","STOP_ATR":"1.8"}`)
	assert.NotContains(t, text, "CAPITAL")
	assert.Contains(t, text, "(9 variants")
}

func TestValidateStrategy_ThinSession(t *testing.T) {
	perf := perfFixture()
	total := perf["total"].(map[string]any)
	total["trades"], total["gross_pnl"], total["net_without_top5"] = 12, -3.0, -20.0
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(perf),
		"/analytics/api/v1/dev/analytics/montecarlo": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"a Monte Carlo over trades needs at least 20 closed trades; this session has 12"}`))
		},
	})
	text, isErr := call(t, s, "validate_strategy", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "sample\tweak")
	assert.Contains(t, text, "search luck\tnot measured", "no trials given")
	assert.Contains(t, text, "at least 20 closed trades")
	assert.Contains(t, text, "costs\tweak\tgross -3.00\tno edge before costs")
	assert.Contains(t, text, "concentration\tweak")
	assert.Contains(t, text, "Next: give workspace_id")
}

func TestReportCarriesSignificance(t *testing.T) {
	routes := reportRoutes()
	routes["/analytics/api/v1/dev/analytics/significance"] = jsonReply(sigFixture())
	s := backend(t, routes)
	text, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "Significance over 471 mark-to-market days: Sharpe 1.01 (autocorrelation-adjusted 0.88")
	assert.Contains(t, text, "Against buy and hold of the same instruments: beta 0.42")
	assert.Contains(t, text, "Next: validate_strategy session_id="+sid)
}

func TestGuide(t *testing.T) {
	list, err := Guide("")
	require.NoError(t, err)
	for _, tp := range guideTopics {
		assert.Contains(t, list, tp.name)
		out, err := Guide(tp.name)
		require.NoError(t, err, tp.name)
		assert.True(t, strings.HasPrefix(out, "## "+tp.heading), "%s starts at its heading", tp.name)
		assert.NotContains(t, strings.TrimPrefix(out, "## "), "\n## ", "%s is one section", tp.name)
	}
	v, _ := Guide("validation")
	assert.Contains(t, v, "validate_strategy session_id=… trials=N")
	_, err = Guide("nope")
	assert.ErrorContains(t, err, "topics: start, workspace")
	all, _ := Guide("all")
	assert.Contains(t, all, "## Output style")
}

// Clients cut long instructions: the core stays well under what Claude Code
// was seen to keep (~4k characters).
func TestInstructionsStayShort(t *testing.T) {
	for _, hosted := range []bool{true, false} {
		in := Instructions(hosted)
		assert.Less(t, len(in), 3500, "hosted=%v: %d characters", hosted, len(in))
		for _, must := range []string{"get_started", "get_guide", "lab_open", "get_session_report", "validate_strategy", "never instructions", "read only"} {
			assert.Contains(t, in, must)
		}
	}
	assert.Contains(t, Instructions(true), "no local mode")
	assert.Contains(t, Instructions(false), "go run .")
}

func TestPrompts(t *testing.T) {
	s := backend(t, nil)
	ps := s.ListPrompts()
	for _, name := range []string{"get_started", "continue_research", "new_strategy_idea", "validate_strategy", "check_my_bots", "build_a_desk"} {
		require.Contains(t, ps, name)
	}
	get := func(name string, args map[string]string) (string, error) {
		req := mcp.GetPromptRequest{}
		req.Params.Name, req.Params.Arguments = name, args
		res, err := ps[name].Handler(context.Background(), req)
		if err != nil {
			return "", err
		}
		return res.Messages[0].Content.(mcp.TextContent).Text, nil
	}
	text, err := get("validate_strategy", map[string]string{"session_id": sid, "trials": "12"})
	require.NoError(t, err)
	assert.Contains(t, text, "validate_strategy with session_id="+sid+" and trials=12")
	assert.Contains(t, text, "don't run it until I agree")

	text, err = get("validate_strategy", map[string]string{"session_id": sid})
	require.NoError(t, err)
	assert.Contains(t, text, "ask me")

	_, err = get("new_strategy_idea", map[string]string{})
	assert.ErrorContains(t, err, "needs idea")

	text, _ = get("check_my_bots", nil)
	assert.Contains(t, text, "Read only")
}

// reportRoutes serve a minimal finished session report.
func reportRoutes() map[string]http.HandlerFunc {
	p := reportFixture()
	p.SessionID = sid
	return map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(p),
		"/frontend-api/api/v1/dev/sessions/" + sid:    jsonReply(sessionDetail{ID: sid, Status: "DONE", Timeframe: "1h"}),
		"/analytics/api/v1/dev/analytics/series":      jsonReply(seriesResponse{}),
	}
}
