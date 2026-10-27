package tools

import (
	"context"
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
				"output":   "x.go:2: import \"os/exec\": starting processes is not available in the cloud workspace",
				"warnings": "x.go:9: go statement: the replay does not wait for a goroutine"})(w, r)
		},
	})
	out, isErr := call(t, s, "workspace_build", map[string]any{"workspace_id": wsID})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Refused before any machine started")
	assert.Contains(t, out, `x.go:2: import "os/exec"`)
	assert.Contains(t, out, "Warnings (the run goes ahead")
	assert.Contains(t, out, "x.go:9: go statement")
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

func TestWorkspaceRunStatus_Queued(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/runs/" + runID): jsonReply(map[string]any{"id": runID, "kind": "run", "status": "queued", "active": true,
			"pool": "shared", "queue_position": 3}),
		"/frontend-api/api/v1/dev/sessions": jsonReply([]any{}),
	})
	out, isErr := call(t, s, "workspace_run_status", map[string]any{"run_id": runID})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Run "+runID+": queued, number 3 in line for a shared machine.")
	assert.Contains(t, out, "Still queued: workspace_run_status run_id="+runID)
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

// A file read shows its content exactly, then its version for the next write.
func TestWorkspaceReadFile_ShowsTheVersion(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/files/main.go"): jsonReply(map[string]any{"path": "main.go", "content": "package main\n",
			"version": 6, "updated_by": "lab", "updated_at": time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC)}),
	})
	tool := s.GetTool("workspace_read_file")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"workspace_id": wsID, "path": "main.go"}
	res, err := tool.Handler(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, res.Content, 2)
	assert.Equal(t, "package main\n", res.Content[0].(mcp.TextContent).Text, "the content is untouched")
	note := res.Content[1].(mcp.TextContent).Text
	assert.Contains(t, note, "version 6, written by the Lab")
	assert.Contains(t, note, "base_version=6")
}

func TestWorkspaceWriteFile_Versions(t *testing.T) {
	var got map[string]any
	reply := func(status int, body map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			got = nil
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	route := wsRoute("/workspaces/" + wsID + "/files/main.go")
	ok := reply(http.StatusOK, map[string]any{"path": "main.go", "size": 13, "version": 7, "updated_by": "api"})
	handler := ok
	s := backend(t, map[string]http.HandlerFunc{route: func(w http.ResponseWriter, r *http.Request) { handler(w, r) }})
	args := func(extra map[string]any) map[string]any {
		a := map[string]any{"workspace_id": wsID, "path": "main.go", "content": "package main\n"}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}

	out, isErr := call(t, s, "workspace_write_file", args(map[string]any{"base_version": 6}))
	require.False(t, isErr, out)
	assert.Equal(t, float64(6), got["base_version"])
	assert.Equal(t, "Wrote main.go (13 bytes): now version 7.", out)

	_, isErr = call(t, s, "workspace_write_file", args(map[string]any{"base_version": 0}))
	require.False(t, isErr)
	assert.Equal(t, float64(0), got["base_version"], "0 is sent: create only")
	_, isErr = call(t, s, "workspace_write_file", args(nil))
	require.False(t, isErr)
	assert.NotContains(t, got, "base_version", "absent: replace whatever is there")

	handler = reply(http.StatusConflict, map[string]any{"error": "conflict: …", "current_version": 8, "updated_by": "lab"})
	out, isErr = call(t, s, "workspace_write_file", args(map[string]any{"base_version": 7}))
	assert.True(t, isErr)
	assert.Contains(t, out, "main.go changed since you read it (now version 8, written by the Lab); nothing was changed")
	assert.Contains(t, out, "retry with base_version=8")

	handler = reply(http.StatusConflict, map[string]any{"error": "conflict: …", "current_version": 0})
	out, _ = call(t, s, "workspace_write_file", args(map[string]any{"base_version": 7}))
	assert.Contains(t, out, "was deleted since you read it")

	for _, bad := range []any{1.5, -1, "7"} {
		out, isErr = call(t, s, "workspace_write_file", args(map[string]any{"base_version": bad}))
		assert.True(t, isErr, "%v", bad)
		assert.Contains(t, out, "base_version must be a whole number")
	}
}

func TestWorkspaceDeleteFile_Version(t *testing.T) {
	var query string
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/files/risk.go"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodDelete, r.Method)
			query = r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
		},
	})
	out, isErr := call(t, s, "workspace_delete_file", map[string]any{"workspace_id": wsID, "path": "risk.go", "version": 3})
	require.False(t, isErr, out)
	assert.Equal(t, "version=3", query)
	_, isErr = call(t, s, "workspace_delete_file", map[string]any{"workspace_id": wsID, "path": "risk.go"})
	require.False(t, isErr)
	assert.Empty(t, query)
	_, isErr = call(t, s, "workspace_delete_file", map[string]any{"workspace_id": wsID, "path": "risk.go", "version": 0})
	assert.True(t, isErr, "a delete names a version that exists")
}

func TestWorkspaceDeleteFile_Conflict(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/files/risk.go"): func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"conflict","current_version":4,"updated_by":"lab"}`)
		},
	})
	out, isErr := call(t, s, "workspace_delete_file", map[string]any{"workspace_id": wsID, "path": "risk.go", "version": 3})
	assert.True(t, isErr)
	assert.Contains(t, out, "now version 4, written by the Lab")
	assert.Contains(t, out, "retry with version=4", "the delete tool's own argument")
}

func TestWorkspaceFiles_ShowVersionAndWriter(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID): jsonReply(map[string]any{
			"workspace": map[string]any{"id": wsID, "name": "rsi", "sdk_version": "v1.5.0"},
			"files": []any{
				map[string]any{"path": "main.go", "size": 2600, "version": 4, "updated_by": "lab"},
				map[string]any{"path": "risk.go", "size": 300, "version": 1, "updated_by": "api"},
			},
		}),
	})
	out, isErr := call(t, s, "workspace_files", map[string]any{"workspace_id": wsID})
	require.False(t, isErr, out)
	assert.Contains(t, out, "version")
	assert.Contains(t, out, "the Lab")
	assert.Contains(t, out, "API")
}

// A start the service matched to a run already going (a retry) says so.
func TestWorkspaceRun_AlreadyRunning(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/runs"): func(w http.ResponseWriter, r *http.Request) {
			jsonReply(map[string]any{"id": runID, "kind": "run", "status": "running", "active": true, "already_running": true})(w, r)
		},
		wsRoute("/runs/" + runID):           jsonReply(map[string]any{"id": runID, "kind": "run", "status": "running", "active": true}),
		"/frontend-api/api/v1/dev/sessions": jsonReply([]any{}),
	})
	out, isErr := call(t, s, "workspace_run", map[string]any{"workspace_id": wsID, "wait": 0})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Already going: this run was started before")
}

// Refused because the workspace's run is already going (one at a time), the
// start follows that run instead of failing.
func TestWorkspaceRun_RefusedFollowsTheRunGoing(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/runs"): func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"limit reached: 1 run(s) at a time on your account; wait for one to end or stop it"}`))
				return
			}
			jsonReply([]any{
				map[string]any{"id": runID, "kind": "run", "status": "running", "active": true},
			})(w, r)
		},
		wsRoute("/runs/" + runID):           jsonReply(map[string]any{"id": runID, "kind": "run", "status": "running", "active": true}),
		"/frontend-api/api/v1/dev/sessions": jsonReply([]any{}),
	})
	out, isErr := call(t, s, "workspace_run", map[string]any{"workspace_id": wsID, "wait": 0})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Run "+runID+": running")
	assert.Contains(t, out, "Already going")
}

// The hosted server holds a call for at most HostedWaitCap.
func TestWaitArg_HostedCap(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"wait": 170}
	assert.Equal(t, 170*time.Second, Deps{}.waitArg(req, defaultRunWait))
	assert.Equal(t, HostedWaitCap, Deps{WaitCap: HostedWaitCap}.waitArg(req, defaultRunWait))
	req.Params.Arguments = map[string]any{}
	assert.Equal(t, HostedWaitCap, Deps{WaitCap: HostedWaitCap}.waitArg(req, defaultRunWait), "the default too")
}

// The declared parameters, as a table; problems listed; a run shows the
// values it used.
func TestWorkspaceParams(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{
		wsRoute("/workspaces/" + wsID + "/params"): jsonReply(map[string]any{
			"params": []any{
				map[string]any{"name": "FAST_PERIOD", "kind": "int", "default": "50", "min": 5, "max": 500, "help": "fast EMA period", "file": "main.go", "line": 8},
				map[string]any{"name": "SIDE", "kind": "choice", "default": "long", "choices": []string{"long", "short"}, "file": "main.go", "line": 9},
			},
			"problems": []any{map[string]any{"file": "x.go", "line": 3, "msg": "params.Int: the name, default and options must be literals"}},
		}),
		wsRoute("/runs/" + runID): jsonReply(map[string]any{"id": runID, "kind": "run", "status": "succeeded", "active": false,
			"params": map[string]string{"SIDE": "long", "FAST_PERIOD": "21"}}),
	})
	out, isErr := call(t, s, "workspace_params", map[string]any{"workspace_id": wsID})
	require.False(t, isErr, out)
	assert.Contains(t, out, "FAST_PERIOD\tint\t50\t5..500\tfast EMA period\tmain.go:8")
	assert.Contains(t, out, "SIDE\tchoice\tlong\tlong | short")
	assert.Contains(t, out, "- x.go:3: params.Int")

	out, _ = call(t, s, "workspace_run_status", map[string]any{"run_id": runID})
	assert.Contains(t, out, "Parameters: FAST_PERIOD=21 SIDE=long.")
}
