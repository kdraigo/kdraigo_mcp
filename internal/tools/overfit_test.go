package tools

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kdraigo/kdraigo_mcp/internal/render"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overfitFixture is analytics_service's answer for three variants of FAST.
func overfitFixture(ids []string) map[string]any {
	v := func(i int, fast string, sharpe, ret float64, rank int) map[string]any {
		return map[string]any{"id": ids[i], "params": map[string]string{"FAST": fast}, "sharpe": sharpe, "return_pct": ret, "max_drawdown_pct": 12.0, "psr_zero": 0.9, "rank": rank}
	}
	return map[string]any{
		"source": "mark_to_market", "from": "2025-01-01", "days": 365, "objective": "sharpe",
		"sessions": []map[string]any{{"session_id": ids[0], "trades": 120}, {"session_id": ids[1], "trades": 130}, {"session_id": ids[2], "trades": 110}},
		"variants": []map[string]any{v(0, "10", 0.4, 6, 3), v(1, "20", 1.6, 28, 1), v(2, "30", 1.1, 19, 2)},
		"pbo": map[string]any{"blocks": 16, "block_days": 22, "days_used": 352, "combinations": 12870, "pbo": 0.31, "prob_oos_loss": 0.12,
			"slope": 0.2, "median_rank": 0.62, "logits": []map[string]any{{"from": -3, "to": 0, "count": 3990}, {"from": 0, "to": 3, "count": 8880}}},
		"dsr": map[string]any{"best_id": ids[1], "sharpe": 1.6, "trials": 4, "effective_n": 2.4, "trials_used": 3.4, "sharpe_sd": 0.6,
			"expected_max_sharpe": 0.62, "dsr": 0.97, "psr_zero": 0.99},
		"walk_forward": map[string]any{"anchored": false, "is_days": 146, "oos_days": 36, "days": 219, "oos_return_pct": 14.2, "oos_sharpe": 1.2,
			"oos_max_drawdown_pct": 8.5, "oos_psr_zero": 0.93, "efficiency": 0.64, "folds_positive": 5, "pick_changes": 2, "distinct_picks": 2,
			"median_variant_return_pct": 9.1, "best_in_hindsight_return_pct": 18.0,
			"folds": []map[string]any{{"is_from": 0, "is_to": 146, "oos_from": 146, "oos_to": 182, "pick": 1, "pick_id": ids[1], "is_annual_pct": 40, "oos_annual_pct": 22, "oos_return_pct": 2.0}}},
		"sensitivity": map[string]any{"varying": []string{"FAST"}, "best_id": ids[1], "neighbours": 2, "neighbour_mean_sharpe": 0.75, "plateau": 0.47,
			"spp":       map[string]any{"median": 1.1, "p25": 0.75, "p75": 1.35, "best": 1.6, "worst": 0.4, "share_positive": 1, "median_return_pct": 19},
			"marginals": []map[string]any{{"param": "FAST", "values": []map[string]any{{"value": "10", "count": 1, "mean_sharpe": 0.4, "median_sharpe": 0.4}}}}},
		"notes": []string{"3 variants: PBO and the deflated Sharpe are coarse below about 8."},
	}
}

func TestOverfittingReport_FromASweep(t *testing.T) {
	ids := []string{"aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002", "aaaaaaaa-0000-0000-0000-000000000003"}
	var body struct {
		SessionIDs []string            `json:"session_ids"`
		Params     []map[string]string `json:"params"`
		Trials     int                 `json:"trials"`
		Folds      int                 `json:"folds"`
	}
	s := backend(t, map[string]http.HandlerFunc{
		"/workspace/api/v1/dev/runs/run-1": jsonReply(map[string]any{"id": "run-1", "status": "succeeded", "variants": []map[string]any{
			{"params": map[string]string{"FAST": "10"}, "status": "succeeded", "key_id": "k1"},
			{"params": map[string]string{"FAST": "20"}, "status": "succeeded", "key_id": "k2"},
			{"params": map[string]string{"FAST": "30"}, "status": "succeeded", "key_id": "k3"},
			{"params": map[string]string{"FAST": "40"}, "status": "failed", "key_id": "k4"},
		}}),
		"/frontend-api/api/v1/dev/sessions": func(w http.ResponseWriter, r *http.Request) {
			id := map[string]string{"k1": ids[0], "k2": ids[1], "k3": ids[2]}[r.URL.Query().Get("api_key_id")]
			jsonReply([]map[string]any{{"id": id, "created_at": time.Now()}})(w, r)
		},
		"/analytics/api/v1/dev/analytics/overfitting": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			jsonReply(overfitFixture(ids))(w, r)
		},
	})
	text, isErr := call(t, s, "get_overfitting_report", map[string]any{"run_id": "run-1", "folds": 5.0})
	require.False(t, isErr, text)
	assert.Equal(t, ids, body.SessionIDs, "the failed variant has no session to compare")
	assert.Equal(t, "20", body.Params[1]["FAST"])
	assert.Equal(t, 4, body.Trials, "every variant of the sweep is a trial, the failed one too")
	assert.Equal(t, 5, body.Folds)
	for _, want := range []string{
		"Sweep analysis of sweep run-1: 3 variants · 365 mark-to-market days from 2025-01-01 · ranked by sharpe.",
		"1\tFAST=20\t" + ids[1] + "\t1.60\t+28.0%",
		"PBO 0.31: in 31% of 12870 splits (16 blocks of 22 days",
		"Best variant (FAST=20): Sharpe 1.60. After 4 trials (the 3 variants count as 2.4 independent ones, plus 1 tried outside this sweep)",
		"luck alone reaches 0.62: DSR 0.970",
		"2025-01-01 → 2025-05-27\t2025-05-27 → 2025-07-02\tFAST=20",
		"efficiency 64% · 5 of 1 folds positive",
		"median Sharpe 1.10 (p25 0.75, p75 1.35)",
		"plateau 0.47, a slope: the neighbours keep some of it",
		"PBO\tcaution", "deflated Sharpe\tok", "walk-forward\tok", "sensitivity\tcaution",
		"Note: Run run-1: 1 of 4 variants did not succeed",
		"Note: 3 variants: PBO and the deflated Sharpe are coarse",
	} {
		assert.Contains(t, text, want)
	}

	wf, _ := call(t, s, "get_walk_forward", map[string]any{"run_id": "run-1"})
	assert.Contains(t, wf, "## walk-forward (rolling: 1 folds of 36 days, each after 146 in-sample days)")
	assert.NotContains(t, wf, "## parameter sensitivity")
	sens, _ := call(t, s, "get_parameter_sensitivity", map[string]any{"run_id": "run-1"})
	assert.Contains(t, sens, "## parameter sensitivity")
	assert.NotContains(t, sens, "## walk-forward")
}

func TestOverfittingReport_FromAnExperiment(t *testing.T) {
	ids := []string{"bbbbbbbb-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002", "bbbbbbbb-0000-0000-0000-000000000003"}
	a0, a1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	b1 := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	run := func(id string, fast string, from, to time.Time, status string) map[string]any {
		r := map[string]any{"id": "r-" + fast, "status": status, "params": map[string]string{"FAST": fast}, "exchange": "binance_futures",
			"pairs": []string{"BTC/USDT"}, "timeframe": "1h", "window_start": from, "window_end": to, "metrics": map[string]any{"sessions": 1}}
		if id != "" {
			r["session_ids"] = []string{id}
		}
		return r
	}
	var body struct {
		SessionIDs []string `json:"session_ids"`
		Trials     int      `json:"trials"`
	}
	s := backend(t, map[string]http.HandlerFunc{
		"/research/api/v1/dev/labs/trend/runs": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "ema", r.URL.Query().Get("experiment"))
			jsonReply([]map[string]any{
				run(ids[0], "10", a0, a1, "succeeded"), run(ids[1], "20", a0, a1, "succeeded"), run(ids[2], "30", a0, a1, "succeeded"),
				run("cccccccc-0000-0000-0000-000000000009", "20", a1, b1, "succeeded"), run("", "40", a0, a1, "failed"),
			})(w, r)
		},
		"/research/api/v1/dev/labs/trend/experiments/ema": jsonReply(map[string]any{"slug": "ema", "status": "registered", "trials": 12}),
		"/analytics/api/v1/dev/analytics/overfitting": func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			jsonReply(overfitFixture(ids))(w, r)
		},
	})
	text, isErr := call(t, s, "get_overfitting_report", map[string]any{"lab": "trend", "experiment": "ema"})
	require.False(t, isErr, text)
	assert.ElementsMatch(t, ids, body.SessionIDs, "the window most runs share")
	assert.Equal(t, 12, body.Trials, "the experiment's own count")
	assert.Contains(t, text, "Sweep analysis of experiment ema")
	assert.Contains(t, text, "Compared the 3 runs on 2025-01-01 → 2025-07-01; 2 other run(s)")
}

func TestOverfittingReport_Refusals(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		"/workspace/api/v1/dev/runs/plain": jsonReply(map[string]any{"id": "plain", "status": "succeeded"}),
		"/analytics/api/v1/dev/analytics/overfitting": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	})
	text, isErr := call(t, s, "get_overfitting_report", map[string]any{})
	assert.True(t, isErr)
	assert.Contains(t, text, "name the sweep one way")
	text, _ = call(t, s, "get_overfitting_report", map[string]any{"run_id": "x", "experiment": "y", "lab": "z"})
	assert.Contains(t, text, "name the sweep one way")
	text, _ = call(t, s, "get_overfitting_report", map[string]any{"experiment": "y"})
	assert.Contains(t, text, "experiment needs its lab")
	text, _ = call(t, s, "get_overfitting_report", map[string]any{"run_id": "plain"})
	assert.Contains(t, text, "is not a sweep")
	text, _ = call(t, s, "get_overfitting_report", map[string]any{"session_ids": []any{sid}})
	assert.Contains(t, text, "needs at least 2")
	text, _ = call(t, s, "get_overfitting_report", map[string]any{"session_ids": []any{sid, sid + "x"}, "params": []any{map[string]any{"A": 1}}})
	assert.Contains(t, text, "one object per session")
	text, _ = call(t, s, "get_overfitting_report", map[string]any{"session_ids": []any{sid, "22222222-2222-3333-4444-555555555555"}})
	assert.Contains(t, text, "does not have /analytics/overfitting yet")
}

// With the sweep it came from, validate_strategy grades overfitting too, and
// takes its trials from the sweep.
func TestValidateStrategy_WithItsSweep(t *testing.T) {
	ids := []string{sid, "aaaaaaaa-0000-0000-0000-000000000002", "aaaaaaaa-0000-0000-0000-000000000003"}
	var sigTrials string
	s := backend(t, map[string]http.HandlerFunc{
		"/analytics/api/v1/dev/analytics/performance": jsonReply(perfFixture()),
		"/analytics/api/v1/dev/analytics/significance": func(w http.ResponseWriter, r *http.Request) {
			sigTrials = r.URL.Query().Get("trials")
			jsonReply(sigFixture())(w, r)
		},
		"/analytics/api/v1/dev/analytics/montecarlo":  jsonReply(mcFixture()),
		"/analytics/api/v1/dev/analytics/overfitting": jsonReply(overfitFixture(ids)),
		"/workspace/api/v1/dev/runs/run-1": jsonReply(map[string]any{"id": "run-1", "variants": []map[string]any{
			{"params": map[string]string{"FAST": "10"}, "status": "succeeded", "key_id": "k1"},
			{"params": map[string]string{"FAST": "20"}, "status": "succeeded", "key_id": "k2"},
			{"params": map[string]string{"FAST": "30"}, "status": "succeeded", "key_id": "k3"},
			{"params": map[string]string{"FAST": "40"}, "status": "failed", "key_id": "k4"},
		}}),
		"/frontend-api/api/v1/dev/sessions": func(w http.ResponseWriter, r *http.Request) {
			id := map[string]string{"k1": ids[0], "k2": ids[1], "k3": ids[2]}[r.URL.Query().Get("api_key_id")]
			jsonReply([]map[string]any{{"id": id, "created_at": time.Now()}})(w, r)
		},
	})
	text, isErr := call(t, s, "validate_strategy", map[string]any{"session_id": sid, "run_id": "run-1"})
	require.False(t, isErr, text)
	assert.Equal(t, "4", sigTrials, "the sweep's trials when none were given")
	assert.Contains(t, text, "overfitting\tcaution\tPBO 0.31 · DSR 0.97 (4 trials) · WFE 64% · plateau 0.47 · median setting Sharpe 1.10 · this session ranks 3 of 3")
	assert.Contains(t, text, "PBO: PBO 0.2–0.5")
	assert.Contains(t, text, "Next: get_overfitting_report")
}

// Near copies: PBO is not a verdict on the edge, and the deflation says it
// used chance's spread.
func TestSweepGrades_NearCopies(t *testing.T) {
	ids := []string{"a", "b", "c"}
	fx := overfitFixture(ids)
	fx["pbo"].(map[string]any)["pbo"] = 0.53
	fx["pbo"].(map[string]any)["prob_oos_loss"] = 0.003
	d := fx["dsr"].(map[string]any)
	d["effective_n"], d["sharpe_sd"], d["chance_sd"], d["sharpe_sd_used"] = 1.09, 0.10, 0.88, 0.88
	b, _ := json.Marshal(fx)
	var r overfitResponse
	require.NoError(t, json.Unmarshal(b, &r))
	g := sweepGrades(&r)
	assert.Equal(t, gradeNone, g[0].grade)
	assert.Contains(t, g[0].why, "near copies (1.1 effective of 3)")

	doc := render.New()
	renderPBO(doc, &r)
	renderDSR(doc, &r)
	assert.Contains(t, doc.String(), "PBO then says only that the choice between them is noise")
	assert.Contains(t, doc.String(), "spread by 0.88 (the variants spread only 0.10; chance alone spreads trials 0.88 over these days)")

	// Not copies, PBO high, but the pick rarely loses: caution, not weak.
	d["effective_n"] = 2.4
	b, _ = json.Marshal(fx)
	require.NoError(t, json.Unmarshal(b, &r))
	g = sweepGrades(&r)
	assert.Equal(t, gradeAmber, g[0].grade)
	assert.Contains(t, g[0].why, "rarely loses")
}

func TestHoldoutText(t *testing.T) {
	n, e, z := 3, 1, 0
	assert.Equal(t, "—", holdoutText(experiment{}))
	assert.Equal(t, "3 (1 early)", holdoutText(experiment{HoldoutRuns: &n, HoldoutRunsEarly: &e}))
	assert.Equal(t, "3", holdoutText(experiment{HoldoutRuns: &n, HoldoutRunsEarly: &z}))
	assert.Equal(t, "3", trialsText(experiment{Trials: &n}))
}
