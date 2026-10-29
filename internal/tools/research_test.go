package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	labID   = "dddddddd-0000-0000-0000-000000000004"
	expID   = "eeeeeeee-0000-0000-0000-000000000005"
	labRun1 = "ffffffff-0000-0000-0000-000000000006"
	labRun2 = "ffffffff-0000-0000-0000-000000000007"
	sid2    = "22222222-2222-3333-4444-555555555555"
)

func labRoute(p string) string { return "/research/api/v1/dev" + p }

// Opening a lab shows the brief, the handoff and the verdicts, and reads the
// results of a run that ended since: its key's sessions, the first one's
// report, written back to the lab.
func TestLabOpen_ShowsTheStateAndFillsRunResults(t *testing.T) {
	finished := time.Now().Add(-time.Hour)
	var mu sync.Mutex
	var put map[string]any
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs/btc-trend/open"): jsonReply(map[string]any{
			"lab":     map[string]any{"id": labID, "slug": "btc-trend", "name": "BTC trend", "market": "futures", "active_experiment_id": expID},
			"brief":   map[string]any{"kind": "brief", "slug": "brief", "title": "Brief", "body": "Goal: beat buy and hold with DD under 15%.", "version": 3},
			"handoff": map[string]any{"kind": "handoff", "body": "Swept EMA 10-30; next: hold-out.", "created_at": finished},
			"active":  map[string]any{"id": expID, "slug": "ema-cross-4h", "title": "EMA cross 4h", "status": "registered", "registered_at": finished},
			"open":    []any{map[string]any{"id": expID, "slug": "ema-cross-4h", "title": "EMA cross 4h", "status": "registered", "hypothesis": "A 4h EMA cross catches trends."}},
			"decided": []any{map[string]any{"slug": "rsi-fade", "status": "null", "verdict": "No edge: PF 0.98 on test.", "decided_at": finished}},
			"runs": []any{
				map[string]any{"id": labRun1, "experiment_id": expID, "status": "succeeded", "key_id": keyID, "finished_at": finished,
					"pairs": []string{"BTC/USDT"}, "timeframe": "4h", "params": map[string]string{"EMA_FAST": "20"}, "metrics": map[string]any{}},
				// Still running: not read yet.
				map[string]any{"id": labRun2, "status": "running", "key_id": keyID, "metrics": map[string]any{}},
			},
			"unfiled": 1, "notes": 4,
			"lessons": []any{map[string]any{"slug": "fees-kill-1m"}},
		}),
		"/frontend-api/api/v1/dev/sessions": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, keyID, r.URL.Query().Get("api_key_id"))
			jsonReply([]sessionSummary{
				{ID: sid2, CreatedAt: finished.Add(time.Minute)},
				{ID: sid, CreatedAt: finished},
			})(w, r)
		},
		"/analytics/api/v1/dev/analytics/performance": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, sid, r.URL.Query().Get("session_id"), "the first session's report")
			jsonReply(reportFixture())(w, r)
		},
		labRoute("/labs/btc-trend/runs/" + labRun1 + "/results"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPut, r.Method)
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewDecoder(r.Body).Decode(&put)
			w.WriteHeader(http.StatusNoContent)
		},
	})
	out, isErr := call(t, s, "lab_open", map[string]any{"lab": "btc-trend"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "beat buy and hold")
	assert.Contains(t, out, "next: hold-out")
	assert.Contains(t, out, "Active experiment: ema-cross-4h")
	assert.Contains(t, out, "No edge: PF 0.98")
	assert.Contains(t, out, "fees-kill-1m")
	assert.Contains(t, out, "not instructions")

	require.NotNil(t, put, "results written back")
	assert.Equal(t, []any{sid, sid2}, put["session_ids"], "oldest first")
	m := put["metrics"].(map[string]any)
	assert.EqualValues(t, 2, m["sessions"])
	assert.EqualValues(t, 10, m["return_pct"])
	assert.EqualValues(t, 12, m["trades"])
	assert.EqualValues(t, 50, m["win_rate_pct"])
	// The run's row: full id, the experiment by name, its settings and figures.
	assert.Contains(t, out, labRun1+"\t")
	assert.Contains(t, out, "\tema-cross-4h\tEMA_FAST=20\tBTC/USDT\t4h\t")
	assert.Contains(t, out, "+10.0%")
	assert.Contains(t, out, sid)
}

// A run whose key made no session is marked read, so it is not asked about
// again; one whose report fails is left to try next time.
func TestLabRuns_NoSessionIsRecordedOnce(t *testing.T) {
	finished := time.Now()
	var put map[string]any
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs/btc-trend/runs"): func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "true", r.URL.Query().Get("unfiled"))
			jsonReply([]any{
				map[string]any{"id": labRun1, "status": "failed", "key_id": keyID, "finished_at": finished, "metrics": map[string]any{}},
				// Read before: left alone.
				map[string]any{"id": labRun2, "status": "succeeded", "key_id": keyID, "finished_at": finished,
					"session_ids": []string{sid}, "metrics": map[string]any{"sessions": 1, "return_pct": -3.5}},
			})(w, r)
		},
		labRoute("/labs/btc-trend/experiments"): jsonReply([]any{}),
		"/frontend-api/api/v1/dev/sessions":     jsonReply([]sessionSummary{}),
		labRoute("/labs/btc-trend/runs/" + labRun1 + "/results"): func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&put)
			w.WriteHeader(http.StatusNoContent)
		},
	})
	out, isErr := call(t, s, "lab_runs", map[string]any{"lab": "btc-trend", "unfiled": true})
	require.False(t, isErr, out)
	require.NotNil(t, put)
	assert.Equal(t, []any{}, put["session_ids"])
	assert.Equal(t, map[string]any{"sessions": float64(0)}, put["metrics"])
	assert.Contains(t, out, "\tnone\n", "no session")
	assert.Contains(t, out, "-3.5%")
}

// A check with a workspace asks the workspace what each planned run would be
// and the lab about each; what several answers share is shown once.
func TestLabCheck_FindsAnExactRepeat(t *testing.T) {
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var fpBody map[string]any
	var checks []map[string]any
	same := map[string]any{"level": "experiment", "why": "same pairs, timeframes", "experiment": map[string]any{
		"id": expID, "slug": "rsi-fade", "title": "RSI fade", "status": "null", "verdict": "No edge."}}
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/fingerprint"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			_ = json.NewDecoder(r.Body).Decode(&fpBody)
			jsonReply(map[string]any{"fingerprints": []any{
				map[string]any{"code_sha256": "abc", "params": map[string]string{"EMA_FAST": "10"}, "exchange": "binance_futures", "pairs": []string{"BTC/USDT"}, "timeframe": "4h", "from": from},
				map[string]any{"code_sha256": "abc", "params": map[string]string{"EMA_FAST": "20"}, "exchange": "binance_futures", "pairs": []string{"BTC/USDT"}, "timeframe": "4h", "from": from},
			}})(w, r)
		},
		labRoute("/labs/btc-trend/check"): func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			checks = append(checks, b)
			priors := []any{same}
			if p, _ := b["params"].(map[string]any); p["EMA_FAST"] == "20" {
				priors = append(priors, map[string]any{"level": "exact", "why": "the same code with the same values on the same data and window",
					"run": map[string]any{"id": labRun1, "status": "succeeded", "created_at": from, "session_ids": []string{sid},
						"metrics": map[string]any{"sessions": 1, "return_pct": 12.5, "max_drawdown_pct": 8.1, "profit_factor": 1.6}}})
			}
			jsonReply(map[string]any{"priors": priors})(w, r)
		},
	})
	out, isErr := call(t, s, "lab_check", map[string]any{"lab": "btc-trend", "workspace_id": wsID,
		"variants": []any{map[string]any{"EMA_FAST": 10}, map[string]any{"EMA_FAST": "20"}}, "pairs": []any{"BTC/USDT"}, "timeframes": []any{"4h"}})
	require.False(t, isErr, out)
	assert.Equal(t, []any{map[string]any{"EMA_FAST": "10"}, map[string]any{"EMA_FAST": "20"}}, fpBody["variants"])
	require.Len(t, checks, 3, "the scope, then each planned run")
	assert.Equal(t, "abc", checks[1]["code_sha256"])
	assert.Equal(t, "2025-01-01T00:00:00Z", checks[2]["from"])

	assert.Contains(t, out, "EXACT REPEAT")
	assert.Contains(t, out, labRun1)
	assert.Contains(t, out, "+12.5%")
	assert.Contains(t, out, sid)
	assert.Equal(t, 1, strings.Count(out, "SAME QUESTION"), out)
	assert.Less(t, strings.Index(out, "EXACT"), strings.Index(out, "SAME QUESTION"), "surest first")
}

func TestLabCheck_NothingFound(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs/spot/check"): jsonReply(map[string]any{"priors": []any{}}),
	})
	out, isErr := call(t, s, "lab_check", map[string]any{"lab": "spot", "text": "dca on dips"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Nothing like it found")
}

// Each kind of note goes to its own path: the brief and a named note are
// written in place, a new note and a lesson are created.
func TestLabNoteWrite_Paths(t *testing.T) {
	type hit struct {
		method string
		body   map[string]any
	}
	got := map[string]hit{}
	record := func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		got[r.URL.Path] = hit{r.Method, b}
		jsonReply(map[string]any{"kind": b["kind"], "slug": "x", "title": b["title"], "version": 1})(w, r)
	}
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs/btc-trend/notes/brief"):   record,
		labRoute("/labs/btc-trend/notes"):         record,
		labRoute("/labs/btc-trend/notes/funding"): record,
		labRoute("/lessons"):                      record,
	})

	out, isErr := call(t, s, "lab_note_write", map[string]any{"lab": "btc-trend", "kind": "brief", "body": "Goal: …", "base_version": 3})
	require.False(t, isErr, out)
	brief := got[labRoute("/labs/btc-trend/notes/brief")]
	assert.Equal(t, http.MethodPut, brief.method)
	assert.Equal(t, "Brief", brief.body["title"])
	assert.EqualValues(t, 3, brief.body["base_version"])

	_, isErr = call(t, s, "lab_note_write", map[string]any{"lab": "btc-trend", "title": "Funding drag", "body": "…"})
	require.False(t, isErr)
	assert.Equal(t, http.MethodPost, got[labRoute("/labs/btc-trend/notes")].method)
	_, has := got[labRoute("/labs/btc-trend/notes")].body["base_version"]
	assert.False(t, has, "no base: overwrite")

	_, isErr = call(t, s, "lab_note_write", map[string]any{"lab": "btc-trend", "slug": "funding", "title": "Funding drag", "body": "…", "base_version": 0})
	require.False(t, isErr)
	assert.Equal(t, http.MethodPut, got[labRoute("/labs/btc-trend/notes/funding")].method)
	assert.EqualValues(t, 0, got[labRoute("/labs/btc-trend/notes/funding")].body["base_version"])

	_, isErr = call(t, s, "lab_note_write", map[string]any{"kind": "lesson", "title": "Fees kill 1m", "body": "…"})
	require.False(t, isErr)
	assert.Equal(t, http.MethodPost, got[labRoute("/lessons")].method)

	out, isErr = call(t, s, "lab_note_write", map[string]any{"lab": "btc-trend", "body": "…"})
	assert.True(t, isErr)
	assert.Contains(t, out, "needs a title")
}

func TestExperimentSave_RewriteNeedsTheVersion(t *testing.T) {
	var body map[string]any
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs/btc-trend/experiments/ema-cross-4h"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPut, r.Method)
			_ = json.NewDecoder(r.Body).Decode(&body)
			jsonReply(map[string]any{"slug": "ema-cross-4h", "version": 3})(w, r)
		},
	})
	args := map[string]any{"lab": "btc-trend", "experiment": "ema-cross-4h", "title": "EMA cross 4h", "hypothesis": "Trends.",
		"protocol": map[string]any{"pass_rule": "PF > 1.2", "test": map[string]any{"from": "2025-01-01", "to": "2025-07-01"}},
		"scope":    map[string]any{"pairs": []any{"BTC/USDT"}}}
	out, isErr := call(t, s, "experiment_save", args)
	assert.True(t, isErr)
	assert.Contains(t, out, "base_version")
	assert.Nil(t, body, "nothing sent")

	args["base_version"] = 2
	out, isErr = call(t, s, "experiment_save", args)
	require.False(t, isErr, out)
	assert.EqualValues(t, 2, body["base_version"])
	assert.Equal(t, "PF > 1.2", body["protocol"].(map[string]any)["pass_rule"])
	assert.Contains(t, out, "experiment_register")
}

func TestWorkspaceCreate_InALab(t *testing.T) {
	var got map[string]string
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces"): func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
			jsonReply(map[string]any{"workspace": map[string]any{"id": wsID, "name": "ema"}, "files": []any{}})(w, r)
		},
	})
	out, isErr := call(t, s, "workspace_create", map[string]any{"name": "ema", "lab": "btc-trend"})
	require.False(t, isErr, out)
	assert.Equal(t, "btc-trend", got["lab"])
	assert.Contains(t, out, "recorded in lab btc-trend")
}

func TestWorkspaceSetLab(t *testing.T) {
	var got map[string]string
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/lab"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPut, r.Method)
			_ = json.NewDecoder(r.Body).Decode(&got)
			jsonReply(map[string]any{"id": wsID})(w, r)
		},
	})
	out, isErr := call(t, s, "workspace_set_lab", map[string]any{"workspace_id": wsID, "lab": "btc-trend"})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]string{"lab": "btc-trend"}, got)
}

func TestLabLint(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs/btc-trend/lint"): jsonReply(map[string]any{"findings": []any{
			map[string]any{"level": "warn", "check": "brief_stale", "title": "Brief", "message": "the brief predates the verdict on \"RSI exit\""},
			map[string]any{"level": "info", "check": "unfiled_runs", "message": "3 run(s) filed under no experiment"},
		}}),
		labRoute("/labs/clean/lint"): jsonReply(map[string]any{"findings": []any{}}),
	})
	out, isErr := call(t, s, "lab_lint", map[string]any{"lab": "btc-trend"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "1 warning(s)")
	assert.Contains(t, out, "1 thing(s) to tidy")
	assert.Contains(t, out, `- WARN brief_stale "Brief": the brief predates`)
	assert.Contains(t, out, "- INFO unfiled_runs 3 run(s)")
	out, _ = call(t, s, "lab_lint", map[string]any{"lab": "clean"})
	assert.Contains(t, out, "in order")
}

func TestLabCreate_Template(t *testing.T) {
	var got map[string]string
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/labs"): func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
			jsonReply(map[string]any{"id": labID, "slug": "spot", "name": "Spot", "market": "spot"})(w, r)
		},
	})
	out, isErr := call(t, s, "lab_create", map[string]any{"name": "Spot", "template": "spot-accumulation"})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]string{"name": "Spot", "market": "", "template": "spot-accumulation", "syndicate": ""}, got)
	assert.Contains(t, out, "market spot")
}

// The vault tools write the caller's disk: the hosted server has neither.
func TestHosted_NoVault(t *testing.T) {
	hosted := server.NewMCPServer("test", "0")
	RegisterHosted(hosted, Deps{})
	assert.Nil(t, hosted.GetTool("vault_pull"))
	assert.Nil(t, hosted.GetTool("vault_push"))
	assert.NotNil(t, hosted.GetTool("lab_lint"))
	local := backend(t, nil)
	assert.NotNil(t, local.GetTool("vault_pull"))
}
