package tools

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func costsFixture(be float64) map[string]any {
	return map[string]any{"session_id": sid, "trades": 908, "net": 172.1, "gross": 227.3, "fees": 43.1, "funding": 12.1, "turnover": 95000, "fees_bps": 4.5,
		"break_even_fee_mult": 4.99, "break_even_slippage_bps": be, "cost_r": 0.07, "slippage_r_per_bp": 0.012,
		"grid": []map[string]any{{"fee_mult": 1, "slippage_bps": 0, "net": 172.1, "return_pct": 57.4, "win_rate": 0.2, "pf": 1.23, "avg_r": 0.225},
			{"fee_mult": 2, "slippage_bps": 10, "net": -41.6, "return_pct": -13.9, "win_rate": 0.18, "pf": 0.95, "avg_r": -0.02},
			{"fee_mult": 2, "slippage_bps": 20, "net": -300, "return_pct": -100, "win_rate": 0.1, "pf": 0.5, "avg_r": -0.3,
				"wiped_out_at": "2025-03-04T10:00:00Z", "wiped_out_trade": 140}},
		"notes": []string{"Every trade is kept where it was."}}
}

func entriesFixture(p float64) map[string]any {
	return map[string]any{"session_id": sid, "instruments": 19, "entries": 908, "notes": []string{},
		"horizons": []map[string]any{{"days": 1, "trades": 900, "mean_bps": 21.4, "random_mean_bps": 3.1, "random_sd_bps": 9.8, "hit_rate": 0.52, "random_hit_rate": 0.5, "p_value": p, "draws": 1000}}}
}

func TestCostStressAndEntryEdge(t *testing.T) {
	var q string
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/costs": func(w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			jsonReply(costsFixture(18.1))(w, r)
		},
		"/analytics/api/v1/dev/analytics/entries": jsonReply(entriesFixture(0.03)),
	})
	text, isErr := call(t, s, "get_cost_stress", map[string]any{"session_id": sid, "slippage_bps": "0,10"})
	require.False(t, isErr, text)
	assert.Contains(t, q, "slippage_bps=0%2C10")
	assert.Contains(t, text, "net reaches zero at 18.1 bp of slippage on every fill (at today's fees), or at fees ×4.99")
	assert.Contains(t, text, "×2\t10\t-41.60\t-13.90%")
	assert.Contains(t, text, "×2\t20\t-300.00\t-100.00% (account gone at trade 140 of", "the return stops at -100% and says where")
	assert.Contains(t, text, "fees and funding take 0.070R already")

	text, isErr = call(t, s, "get_entry_edge", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "908 entries on 19 instruments")
	assert.Contains(t, text, "1d\t900\t+21.4\t+3.1 ± 9.8\t52.0%\t50.0%\t0.030")
}

func TestCompareSessions(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/compare": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "live", r.URL.Query().Get("type_b"))
			jsonReply(map[string]any{"a": "A", "b": "B", "source": "mark_to_market", "from": "2025-01-01", "to": "2025-12-31", "days": 365,
				"sharpe_a": 1.0, "sharpe_b": 1.4, "diff": 0.4, "diff_p5": -0.3, "diff_p95": 1.1, "p_value": 0.31, "correlation": 0.8, "share_b_better": 0.81,
				"return_pct_a": 20, "return_pct_b": 30, "max_drawdown_pct_a": 10, "max_drawdown_pct_b": 12})(w, r)
		},
	})
	text, isErr := call(t, s, "compare_sessions", map[string]any{"a": "A", "b": "B", "type_b": "live"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "Sharpe A 1.00 · B 1.40 · difference +0.40 (5–95%: -0.30 to +1.10) · p 0.310 · B ahead in 81% of resamples")
	assert.Contains(t, text, "Verdict: no difference the days can tell apart (p 0.31)")
}

func TestLiveConeAndDrift(t *testing.T) {
	fan := []map[string]any{}
	for d := 1; d <= 40; d++ {
		fan = append(fan, map[string]any{"day": d, "p5": -5.0, "p25": -1.0, "p50": 1.0, "p75": 3.0, "p95": 6.0, "live": -6.5})
	}
	lr, br := 1.75, 2.5
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/live_cone": jsonReply(map[string]any{"live_session_id": "L", "backtest_session_id": "B", "scale_source": "risk",
			"live_risk_pct": lr, "backtest_risk_pct": br, "live_from": "2026-10-02", "days": 40, "paths": 4000, "scale": 0.7, "fan": fan,
			"live_pct": -6.5, "percentile": 0.02, "days_below_p5": 12, "days_above_p95": 0, "notes": []string{"Both are closed-trade daily equity."}}),
		"/analytics/api/v1/dev/analytics/drift": jsonReply(map[string]any{"from": at, "to": at.Add(40 * 24 * time.Hour), "tolerance_sec": 7200,
			"live_trades": 30, "backtest_trades": 33, "matched": 28, "missed": 5, "extra": 2, "entry_slip_median_bps": 3.2, "entry_slip_mean_bps": 4.1,
			"exit_slip_median_bps": 6.0, "exit_slip_mean_bps": 7.5, "live_avg_r": 0.05, "backtest_avg_r": 0.21, "live_net": 12.0, "backtest_net": 30.0,
			"missed_trades": []map[string]any{{"pair": "SOL/USDT", "side": "long", "entry_at": at}},
			"matches":       []map[string]any{{"pair": "BTC/USDT", "side": "long", "live_entry_at": at, "lag_sec": 3600, "entry_slip_bps": 3.2, "live_net_r": 1.0, "backtest_net_r": 1.2}}}),
	})
	text, isErr := call(t, s, "get_live_cone", map[string]any{"live_session_id": "L", "backtest_session_id": "B"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "scaled ×0.70 (live risks 1.75% per trade, the backtest 2.50%)")
	assert.Contains(t, text, "Live -6.50% sits at the 2nd percentile: BELOW the cone")
	assert.Contains(t, text, "Days below the 5th: 12")
	assert.Contains(t, text, "Flag, don't act")

	text, isErr = call(t, s, "get_live_drift", map[string]any{"live_session_id": "L", "backtest_session_id": "B", "tolerance_min": 90.0})
	require.False(t, isErr, text)
	assert.Contains(t, text, "28 matched · 5 missed by the bot · 2 extra")
	assert.Contains(t, text, "entry median 3.2 bp (mean 4.1) · exit median 6.0 bp (mean 7.5)")
	assert.Contains(t, text, "avg R live +0.050, backtest +0.210")
	assert.Contains(t, text, "BTC/USDT\tlong\t2026-10-02 09:00\t60m\t+3.2")
	assert.Contains(t, text, "Missed by the bot (5): SOL/USDT long")
}

func TestValidateStrategy_CostsAndEntries(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance":  jsonReply(perfFixture()),
		"/analytics/api/v1/dev/analytics/significance": jsonReply(sigFixture()),
		"/analytics/api/v1/dev/analytics/montecarlo":   jsonReply(mcFixture()),
		"/analytics/api/v1/dev/analytics/costs":        jsonReply(costsFixture(4.2)),
		"/analytics/api/v1/dev/analytics/entries":      jsonReply(entriesFixture(0.4)),
	})
	text, isErr := call(t, s, "validate_strategy", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "costs\tcaution\tbreak-even at 4.2 bp slippage or fees ×4.99 · fees + funding 24% of gross\ta few basis points of slippage end it")
	assert.Contains(t, text, "entries\tcaution\t1d after entry: +21.4 bp vs +3.1 random, p 0.400\tentries no better than random")
	assert.Less(t, indexOf(text, "entries\t"), indexOf(text, "overfitting\t"), "entries come before overfitting")
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestWorkspaceRunStress(t *testing.T) {
	var sent map[string]any
	s := backend(t, map[string]http.HandlerFunc{
		"/workspace/api/v1/dev/workspaces/ws-1/runs": func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
			jsonReply(map[string]any{"id": "run-9", "status": "queued", "active": true})(w, r)
		},
		"/workspace/api/v1/dev/runs/run-9": jsonReply(map[string]any{"id": "run-9", "status": "queued", "active": true}),
	})
	_, isErr := call(t, s, "workspace_run", map[string]any{"workspace_id": "ws-1", "stress": "standard", "wait": 0.0})
	require.False(t, isErr)
	vs := sent["variants"].([]any)
	require.Len(t, vs, 6)
	assert.Equal(t, map[string]any{"KDRAIGO_SIM_FEE_MULT": "2"}, vs[2])
	assert.Equal(t, map[string]any{"KDRAIGO_SIM_FUNDING": "off"}, vs[5])

	text, isErr := call(t, s, "workspace_run", map[string]any{"workspace_id": "ws-1", "stress": "standard", "variants": []any{map[string]any{"A": "1"}, map[string]any{"A": "2"}}})
	assert.True(t, isErr)
	assert.Contains(t, text, "not both")

	assert.Equal(t, "fees ×2", settingText("KDRAIGO_SIM_FEE_MULT", "2"))
	assert.Equal(t, "as is", settingText("KDRAIGO_SIM_FEE_MULT", "1"))
	assert.Equal(t, "slippage 5bp", settingText("KDRAIGO_SIM_SLIPPAGE_BPS", "5"))
	assert.Equal(t, "FAST=10", settingText("FAST", "10"))
}
