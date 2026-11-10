package tools

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startRoutes() map[string]http.HandlerFunc {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return map[string]http.HandlerFunc{
		"/workspace/api/v1/dev/account": jsonReply(map[string]any{
			"account": "verified", "tier": "verified", "google": false,
			"limits": map[string]any{"backtest_pairs": 5, "backtest_bars": 150000, "running_sessions": 2, "orders_per_session": 100000,
				"workspaces": 3, "sandbox_runs": 1, "sweep_variants": 10, "runs_per_day": 200, "research_labs": 10},
			"usage": map[string]any{"workspaces": 2, "runs_today": 7, "resets_at": now.Add(12 * time.Hour)},
		}),
		"/research/api/v1/dev/labs": jsonReply([]map[string]any{
			{"slug": "old", "name": "Old idea", "updated_at": now.AddDate(0, -1, 0)},
			{"slug": "trend", "ref": "trend", "name": "Futures trend", "updated_at": now},
		}),
		"/workspace/api/v1/dev/workspaces": jsonReply([]map[string]any{{"id": "w1"}, {"id": "w2"}}),
		"/live/api/v1/dev/live-sessions": jsonReply([]map[string]any{
			{"session_id": "a", "status": "RUNNING"}, {"session_id": "b", "status": "RUNNING"}, {"session_id": "c", "status": "DISCONNECTED"},
		}),
		"/research/api/v1/dev/boards":  jsonReply([]map[string]any{}),
		"/research/api/v1/dev/scripts": jsonReply([]map[string]any{{"ref": "ema"}}),
		"/research/api/v1/dev/alerts":  jsonReply([]map[string]any{{"id": "x", "status": "active"}, {"id": "y", "status": "expired"}}),
		"/frontend-api/api/v1/dev/sessions": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("limit") != "1" {
				http.Error(w, "want limit 1", http.StatusBadRequest)
				return
			}
			jsonReply([]map[string]any{{"id": sid, "name": "v1 ema", "status": "DONE", "created_at": now}})(w, r)
		},
		"/data/api/v1/metadata": jsonReply(map[string]any{
			"binance":         map[string]any{"symbols": map[string]any{"BTCUSDT": map[string]any{}, "NVDABUSDT": map[string]any{}}},
			"binance_futures": map[string]any{"symbols": map[string]any{"BTCUSDT": map[string]any{}, "ETHUSDT": map[string]any{}, "XAUUSDT": map[string]any{}}},
		}),
	}
}

func TestGetStarted(t *testing.T) {
	s := backend(t, startRoutes())
	text, isErr := call(t, s, "get_started", nil)
	require.False(t, isErr, text)
	for _, want := range []string{
		"Account: verified.",
		"Backtests: 5 pairs and 150000 bars per pair each",
		"Cloud workspace: 2 of 3 workspaces, 1 run(s) at a time on shared machines (a run may queue), sweeps of up to 10 variants, 7 of 200 runs used today",
		"- Labs: 2 · newest \"Futures trend\" (lab_open lab=trend",
		"- Workspaces: 2",
		"- Live bots: 3 (1 disconnected, 2 running)",
		"- Boards: none yet",
		"- Alerts: 2 (1 active)",
		"- Latest backtest: " + sid + " v1 ema · DONE",
		"5 markets: binance 2 · binance_futures 3.",
		"Is this result real?\tvalidate_strategy trials=N",
	} {
		assert.Contains(t, text, want)
	}
}

// A section that fails is shown as unavailable; the rest still answer.
func TestGetStarted_PartsMissing(t *testing.T) {
	routes := startRoutes()
	routes["/workspace/api/v1/dev/account"] = func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	routes["/live/api/v1/dev/live-sessions"] = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"down"}`, http.StatusServiceUnavailable)
	}
	s := backend(t, routes)
	text, isErr := call(t, s, "get_started", nil)
	require.False(t, isErr, text)
	assert.Contains(t, text, "## Your plan\nUnavailable (this platform does not report plans to the MCP yet)")
	assert.Contains(t, text, "- Live bots: unavailable (")
	assert.Contains(t, text, "- Labs: 2")
}
