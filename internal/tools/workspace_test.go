package tools

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	wsID  = "aaaaaaaa-0000-0000-0000-000000000001"
	runID = "bbbbbbbb-0000-0000-0000-000000000002"
	keyID = "cccccccc-0000-0000-0000-000000000003"
)

func wsRoute(p string) string { return "/workspace/api/v1/dev" + p }

func TestWorkspaceCreate(t *testing.T) {
	var got map[string]string
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			require.NotEmpty(t, r.Header.Get("X-Key-ID"), "signed")
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
			jsonReply(map[string]any{
				"workspace": map[string]any{"id": wsID, "name": "rsi", "sdk_version": "v1.5.0", "template": "with-indicators"},
				"files":     []any{map[string]any{"path": "main.go", "size": 2600}},
			})(w, r)
		},
	})
	out, isErr := call(t, s, "workspace_create", map[string]any{"name": "rsi", "template": "with-indicators"})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]string{"name": "rsi", "template": "with-indicators", "sdk_version": ""}, got)
	assert.Contains(t, out, wsID)
	assert.Contains(t, out, "main.go")
	assert.Contains(t, out, "workspace_run")
}

// A run is followed until it ends; its env goes through, and the sessions its
// key made are listed with the tail of its output.
func TestWorkspaceRun_FollowsItAndFindsItsSessions(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	var started map[string]any
	run := func(status string, active bool) map[string]any {
		r := map[string]any{"id": runID, "workspace_id": wsID, "kind": "run", "status": status, "active": active,
			"key_id": keyID, "env_names": []string{"STRATEGY_RISK"}, "log_bytes": 30}
		if !active {
			r["exit_code"] = 0
			r["started_at"] = time.Now().Add(-9 * time.Second)
			r["finished_at"] = time.Now()
		}
		return r
	}
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/runs"): func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&started)
			w.WriteHeader(http.StatusCreated)
			jsonReply(run("queued", true))(w, r)
		},
		wsRoute("/runs/" + runID): func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			polls++
			n := polls
			mu.Unlock()
			if n < 2 {
				jsonReply(run("running", true))(w, r)
				return
			}
			jsonReply(run("succeeded", false))(w, r)
		},
		"/frontend-api/api/v1/dev/sessions": func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, keyID, r.URL.Query().Get("api_key_id"))
			jsonReply([]any{map[string]any{"id": sid, "name": "basic-template", "status": "DONE", "pair": "BTC/USDT", "timeframe": "1h"}})(w, r)
		},
		wsRoute("/runs/" + runID + "/log"): jsonReply(map[string]any{"offset": 0, "next": 30, "data": "session started\n[done] backtest finished\n"}),
	})
	out, isErr := call(t, s, "workspace_run", map[string]any{"workspace_id": wsID, "env": map[string]any{"STRATEGY_RISK": 0.02, "VERBOSE": true}, "wait": 30})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]any{"kind": "run", "env": map[string]any{"STRATEGY_RISK": "0.02", "VERBOSE": "true"}}, started)
	assert.Contains(t, out, "Run "+runID+": succeeded, exit 0")
	assert.Contains(t, out, sid)
	assert.Contains(t, out, "[done] backtest finished")
	assert.Contains(t, out, "get_session_report")
	assert.Contains(t, out, "STRATEGY_RISK")
}

func TestWorkspaceBuild_RefusedShowsTheProblems(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/runs"): func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			assert.JSONEq(t, `{"kind":"build"}`, string(body))
			w.WriteHeader(http.StatusCreated)
			jsonReply(map[string]any{"id": runID, "kind": "build", "status": "refused", "active": false, "exit_code": -1,
				"output": "x.go:2: import \"os/exec\": starting processes is not available in the cloud workspace"})(w, r)
		},
	})
	out, isErr := call(t, s, "workspace_build", map[string]any{"workspace_id": wsID})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Refused by the import check")
	assert.Contains(t, out, `x.go:2: import "os/exec"`)
	assert.Contains(t, out, "Fix the files")
}

// The service's own message is what the AI reads, not its JSON.
func TestWorkspaceErrorsPassThrough(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces"): func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"forbidden: the cloud workspace is in beta, open to admin accounts only"}`)
		},
	})
	out, isErr := call(t, s, "workspace_list", nil)
	assert.True(t, isErr)
	assert.Equal(t, "workspace_list: HTTP 403: forbidden: the cloud workspace is in beta, open to admin accounts only", out)
}

func TestWorkspaceRunStatus_StillGoing(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/runs/" + runID):           jsonReply(map[string]any{"id": runID, "kind": "run", "status": "running", "active": true, "key_id": keyID}),
		"/frontend-api/api/v1/dev/sessions": jsonReply([]any{}),
	})
	out, isErr := call(t, s, "workspace_run_status", map[string]any{"run_id": runID})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Run "+runID+": running")
	assert.Contains(t, out, "No backtest session yet")
	assert.Contains(t, out, "workspace_run_status run_id="+runID+" wait=120")
}

func TestWorkspaceLogs_Pages(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/runs/" + runID + "/log"): func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "100", r.URL.Query().Get("offset"))
			assert.Equal(t, "65536", r.URL.Query().Get("limit"), "capped")
			jsonReply(map[string]any{"offset": 100, "next": 110, "data": "0123456789", "more": true})(w, r)
		},
	})
	out, isErr := call(t, s, "workspace_logs", map[string]any{"run_id": runID, "offset": 100, "limit": 10 << 20})
	require.False(t, isErr, out)
	assert.Equal(t, "0123456789\n[bytes 100–110; more follows: offset=110]", out)
}

func TestEnvArg(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"env": map[string]any{"A": "x", "B": 1.5, "C": false}}
	env, err := envArg(req)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "x", "B": "1.5", "C": "false"}, env)

	req.Params.Arguments = map[string]any{"env": map[string]any{"A": []any{1}}}
	_, err = envArg(req)
	assert.Error(t, err)
	req.Params.Arguments = map[string]any{"env": "A=1"}
	_, err = envArg(req)
	assert.Error(t, err)
}
