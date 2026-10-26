package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// RegisterWorkspace adds the cloud workspace tools. A workspace is a strategy's
// .go files kept on the platform; workspace_service builds and runs them on a
// sandbox machine of the caller's own, so no local Go, keys or checkout are
// needed. All call workspace_service/api/v1/dev/* via the kdraigo.com gateway.
func RegisterWorkspace(s *server.MCPServer, d Deps) {
	addWorkspaceCreate(s, d)
	addWorkspaceList(s, d)
	addWorkspaceFiles(s, d)
	addWorkspaceReadFile(s, d)
	addWorkspaceWriteFile(s, d)
	addWorkspaceDeleteFile(s, d)
	addWorkspaceBuild(s, d)
	addWorkspaceRun(s, d)
	addWorkspaceRunStatus(s, d)
	addWorkspaceLogs(s, d)
	addWorkspaceStop(s, d)
	addWorkspaceRuns(s, d)
	addWorkspaceDelete(s, d)
}

// What workspace_service answers.
type (
	workspace struct {
		ID         string    `json:"id"`
		Name       string    `json:"name"`
		SDKVersion string    `json:"sdk_version"`
		Template   string    `json:"template"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	}
	workspaceFile struct {
		Path      string    `json:"path"`
		Size      int       `json:"size"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	workspaceDetail struct {
		Workspace workspace       `json:"workspace"`
		Files     []workspaceFile `json:"files"`
	}
	workspaceRun struct {
		ID           string     `json:"id"`
		WorkspaceID  string     `json:"workspace_id"`
		Kind         string     `json:"kind"`
		Status       string     `json:"status"`
		Active       bool       `json:"active"`
		SDKVersion   string     `json:"sdk_version"`
		EnvNames     []string   `json:"env_names"`
		KeyID        string     `json:"key_id"`
		ExitCode     *int       `json:"exit_code"`
		Error        *string    `json:"error"`
		Output       *string    `json:"output"`
		LogBytes     int        `json:"log_bytes"`
		LogTruncated bool       `json:"log_truncated"`
		CreatedAt    time.Time  `json:"created_at"`
		StartedAt    *time.Time `json:"started_at"`
		FinishedAt   *time.Time `json:"finished_at"`
	}
	workspaceLog struct {
		Offset    int    `json:"offset"`
		Next      int    `json:"next"`
		Data      string `json:"data"`
		More      bool   `json:"more"`
		Active    bool   `json:"active"`
		Truncated bool   `json:"truncated"`
	}
)

const (
	// maxWait keeps a waiting tool inside heavyTimeout.
	maxWait = 170 * time.Second
	// defaultBuildWait covers a machine's first boot (about 40 s) and the build.
	defaultBuildWait = 150 * time.Second
	defaultRunWait   = 150 * time.Second
	// tailBytes and tailLines are how much of a run's output a status shows.
	tailBytes = 8 << 10
	tailLines = 30
	// logPage is the default and the most one workspace_logs call returns.
	logPage = 64 << 10
)

func wsPath(id string) string  { return "/api/v1/dev/workspaces/" + url.PathEscape(id) }
func runPath(id string) string { return "/api/v1/dev/runs/" + url.PathEscape(id) }

// waitArg reads the wait argument (seconds), capped at maxWait.
func waitArg(req mcp.CallToolRequest, def time.Duration) time.Duration {
	w := time.Duration(req.GetInt("wait", int(def/time.Second))) * time.Second
	return min(max(w, 0), maxWait)
}

func addWorkspaceCreate(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_create",
		mcp.WithDescription("Create a cloud workspace: a strategy's .go files kept on the platform, built and run on a sandbox machine of your own (no local Go or keys needed). "+
			"It starts from a template (basic, with-indicators, with-risk-manager) on a dev_sdk version; the platform owns go.mod, so a workspace holds flat .go files in package main only. "+
			"Then edit with workspace_write_file and run with workspace_run. Read get_sdk_docs section=\"cloud workspace\" first."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workspace name: starts with a letter; letters, digits, _ and -; unique per account")),
		mcp.WithString("template", mcp.Description("Starter strategy: basic, with-indicators or with-risk-manager"), mcp.DefaultString("basic")),
		mcp.WithString("sdk_version", mcp.Description("dev_sdk version; empty is the platform's default")),
		additive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return toolErr(err), nil
		}
		body := map[string]string{"name": name, "template": req.GetString("template", "basic"), "sdk_version": req.GetString("sdk_version", "")}
		var det workspaceDetail
		if err := d.sendJSON(ctx, "workspace_create", http.MethodPost, client.Workspace, "/api/v1/dev/workspaces", body, &det); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		w := det.Workspace
		doc.Line("Created workspace %q (id %s): dev_sdk %s, template %s.", w.Name, w.ID, w.SDKVersion, w.Template)
		renderFiles(doc, det.Files)
		doc.Line("Next: read main.go with workspace_read_file, change it with workspace_write_file, then workspace_run.")
		return toolText(doc.String()), nil
	})
}

func addWorkspaceList(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_list",
		mcp.WithDescription("Your cloud workspaces, most recently changed first: id, name, dev_sdk version, template."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var list []workspace
		if err := d.getJSON(ctx, "workspace_list", client.Workspace, "/api/v1/dev/workspaces", nil, &list); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		if len(list) == 0 {
			doc.Line("No workspaces yet: workspace_create starts one.")
			return toolText(doc.String()), nil
		}
		rows := make([][]string, 0, len(list))
		for _, w := range list {
			rows = append(rows, []string{w.ID, w.Name, w.SDKVersion, w.Template, render.Time(w.UpdatedAt)})
		}
		doc.Table("", []string{"id", "name", "sdk", "template", "updated"}, rows, nil)
		return toolText(doc.String()), nil
	})
}

func addWorkspaceFiles(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_files",
		mcp.WithDescription("A workspace's files: path, size, last change."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		var det workspaceDetail
		if err := d.getJSON(ctx, "workspace_files", client.Workspace, wsPath(id), nil, &det); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		doc.Line("Workspace %q (dev_sdk %s).", det.Workspace.Name, det.Workspace.SDKVersion)
		renderFiles(doc, det.Files)
		return toolText(doc.String()), nil
	})
}

func renderFiles(doc *render.Doc, files []workspaceFile) {
	if len(files) == 0 {
		doc.Line("No files.")
		return
	}
	rows := make([][]string, 0, len(files))
	for _, f := range files {
		rows = append(rows, []string{f.Path, render.Int(f.Size), render.Time(f.UpdatedAt)})
	}
	doc.Table("", []string{"path", "bytes", "updated"}, rows, nil)
}

func addWorkspaceReadFile(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_read_file",
		mcp.WithDescription("A workspace file's full content, exactly as stored."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File name, e.g. main.go")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		path, err := req.RequireString("path")
		if err != nil {
			return toolErr(err), nil
		}
		var f struct {
			Content string `json:"content"`
		}
		if err := d.getJSON(ctx, "workspace_read_file", client.Workspace, wsPath(id)+"/files/"+url.PathEscape(path), nil, &f); err != nil {
			return toolErr(err), nil
		}
		return toolText(f.Content), nil
	})
}

func addWorkspaceWriteFile(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_write_file",
		mcp.WithDescription("Create or replace a workspace file with the whole new content. Files are flat .go files in package main (no folders, no go.mod: the platform owns it); at most 50 files, 256 KB each, 1 MB in all. "+
			"Imports are checked when the workspace is built: the standard library minus os/exec, net, unsafe, syscall and cgo, plus github.com/kdraigo/dev_sdk."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File name, e.g. main.go or risk.go")),
		mcp.WithString("content", mcp.Required(), mcp.Description("The file's full new content")),
		destructive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		path, err := req.RequireString("path")
		if err != nil {
			return toolErr(err), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return toolErr(err), nil
		}
		if err := d.sendJSON(ctx, "workspace_write_file", http.MethodPut, client.Workspace, wsPath(id)+"/files/"+url.PathEscape(path), map[string]string{"content": content}, nil); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Wrote %s (%d bytes).", path, len(content))), nil
	})
}

func addWorkspaceDeleteFile(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_delete_file",
		mcp.WithDescription("Delete a file from a workspace."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File name")),
		destructive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		path, err := req.RequireString("path")
		if err != nil {
			return toolErr(err), nil
		}
		if err := d.sendJSON(ctx, "workspace_delete_file", http.MethodDelete, client.Workspace, wsPath(id)+"/files/"+url.PathEscape(path), nil, nil); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Deleted %s.", path)), nil
	})
}

func addWorkspaceBuild(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_build",
		mcp.WithDescription("Compile a workspace on your sandbox machine without running it: the fast check after an edit. Returns the import check's or the compiler's errors as file:line: message. "+
			"The first build after a while starts your machine (about 40 s)."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithNumber("wait", mcp.Description("Seconds to wait for the result, at most 170"), mcp.DefaultNumber(defaultBuildWait.Seconds())),
		additive(false),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		return d.startRun(ctx, "workspace_build", id, map[string]any{"kind": "build"}, waitArg(req, defaultBuildWait))
	})
}

func addWorkspaceRun(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_run",
		mcp.WithDescription("Build a workspace and run it on your sandbox machine: the strategy opens its backtest session with a key minted for this run alone (revoked when it ends). "+
			"Waits up to `wait` seconds; a run still going returns its id for workspace_run_status. The result names the backtest sessions the run made: pass one to get_session_report. "+
			"`env` sets your own variables (upper-case names, not KDRAIGO_*), e.g. {\"STRATEGY_RISK\":\"0.02\"}."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithObject("env", mcp.Description("Environment variables for the strategy, name → value")),
		mcp.WithNumber("wait", mcp.Description("Seconds to wait for the run to end, at most 170"), mcp.DefaultNumber(defaultRunWait.Seconds())),
		additive(false),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		env, err := envArg(req)
		if err != nil {
			return toolErr(err), nil
		}
		body := map[string]any{"kind": "run"}
		if len(env) > 0 {
			body["env"] = env
		}
		return d.startRun(ctx, "workspace_run", id, body, waitArg(req, defaultRunWait))
	})
}

// envArg reads the env object; values may be strings, numbers or booleans.
func envArg(req mcp.CallToolRequest) (map[string]string, error) {
	raw, ok := req.GetArguments()["env"]
	if !ok || raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("env must be an object of name → value")
	}
	env := make(map[string]string, len(obj))
	for k, v := range obj {
		switch v := v.(type) {
		case string:
			env[k] = v
		case float64:
			env[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			env[k] = strconv.FormatBool(v)
		default:
			return nil, fmt.Errorf("env %s: a value must be a string, number or boolean", k)
		}
	}
	return env, nil
}

// startRun starts a build or run and reports it once it ends or wait passes.
func (d Deps) startRun(ctx context.Context, name, workspaceID string, body map[string]any, wait time.Duration) (*mcp.CallToolResult, error) {
	var run workspaceRun
	if err := d.sendJSON(ctx, name, http.MethodPost, client.Workspace, wsPath(workspaceID)+"/runs", body, &run); err != nil {
		return toolErr(err), nil
	}
	return d.reportRun(ctx, name, run, wait)
}

// reportRun waits for run (up to wait) and renders where it got to.
func (d Deps) reportRun(ctx context.Context, name string, run workspaceRun, wait time.Duration) (*mcp.CallToolResult, error) {
	r, err := d.waitRun(ctx, name, run, wait)
	if err != nil {
		return toolErr(err), nil
	}
	doc := render.New()
	renderRunHead(doc, r)
	if r.Kind == "run" && r.KeyID != "" {
		d.renderRunSessions(ctx, doc, r)
	}
	if r.Kind == "run" && r.LogBytes > 0 {
		d.renderTail(ctx, doc, r)
	}
	renderNext(doc, r)
	return toolText(doc.String()), nil
}

// waitRun polls until the run ends, wait passes or ctx ends.
func (d Deps) waitRun(ctx context.Context, name string, run workspaceRun, wait time.Duration) (*workspaceRun, error) {
	deadline := time.Now().Add(wait)
	delay := time.Second
	r := run
	for r.Active && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return &r, nil
		case <-time.After(min(delay, time.Until(deadline))):
		}
		delay = min(delay*2, 5*time.Second)
		var next workspaceRun
		if err := d.getJSON(ctx, name, client.Workspace, runPath(run.ID), nil, &next); err != nil {
			if ctx.Err() != nil {
				return &r, nil
			}
			return nil, err
		}
		r = next
	}
	return &r, nil
}

func renderRunHead(doc *render.Doc, r *workspaceRun) {
	kind := r.Kind
	if kind != "" {
		kind = strings.ToUpper(kind[:1]) + kind[1:]
	}
	head := fmt.Sprintf("%s %s: %s", kind, r.ID, r.Status)
	if r.ExitCode != nil && !r.Active {
		head += fmt.Sprintf(", exit %d", *r.ExitCode)
	}
	if r.StartedAt != nil && r.FinishedAt != nil {
		head += fmt.Sprintf(", %s", r.FinishedAt.Sub(*r.StartedAt).Round(100*time.Millisecond))
	}
	doc.Line("%s.", head)
	if len(r.EnvNames) > 0 {
		doc.Line("Variables given: %s.", strings.Join(r.EnvNames, ", "))
	}
	if r.Error != nil && *r.Error != "" {
		doc.Line("Error: %s", *r.Error)
	}
	if r.Output != nil && strings.TrimSpace(*r.Output) != "" {
		what := "Build output"
		if r.Status == "refused" {
			what = "Refused by the import check"
		}
		doc.Line("%s:", what)
		doc.Line("```text\n%s\n```", strings.TrimRight(*r.Output, "\n"))
	}
}

// renderRunSessions lists the backtest sessions the run's key created.
func (d Deps) renderRunSessions(ctx context.Context, doc *render.Doc, r *workspaceRun) {
	q := url.Values{"api_key_id": {r.KeyID}, "limit": {"20"}}
	var list []sessionSummary
	if err := d.getJSON(ctx, "sessions of the run", client.FrontendAPI, "/api/v1/dev/sessions", q, &list); err != nil {
		doc.Line("Sessions: could not list them (%v).", err)
		return
	}
	if len(list) == 0 {
		if r.Active {
			doc.Line("No backtest session yet.")
		} else {
			doc.Line("The run made no backtest session.")
		}
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		progress := render.Dash
		if s.Progress != nil {
			progress = render.Pct(s.Progress.Percent, 0)
		}
		rows = append(rows, []string{s.ID, render.Clip(s.Name, 40), s.Status, progress, s.Pair, s.Timeframe})
	}
	doc.Table("Backtest sessions of this run", []string{"session_id", "name", "status", "progress", "first_pair", "tf"}, rows, nil)
}

// renderTail shows the last lines of the run's output.
func (d Deps) renderTail(ctx context.Context, doc *render.Doc, r *workspaceRun) {
	offset := max(r.LogBytes-tailBytes, 0)
	q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(tailBytes)}}
	var page workspaceLog
	if err := d.getJSON(ctx, "run output", client.Workspace, runPath(r.ID)+"/log", q, &page); err != nil {
		doc.Line("Output: could not read it (%v).", err)
		return
	}
	lines := strings.Split(strings.TrimRight(page.Data, "\n"), "\n")
	if offset > 0 && len(lines) > 1 {
		lines = lines[1:] // the first is cut mid-line
	}
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	note := ""
	if r.LogTruncated {
		note = ", capped: the rest was dropped"
	}
	doc.Line("Output: %d bytes%s. Last %d lines (workspace_logs for all):", r.LogBytes, note, len(lines))
	doc.Line("```text\n%s\n```", strings.Join(lines, "\n"))
}

func renderNext(doc *render.Doc, r *workspaceRun) {
	switch {
	case r.Active:
		doc.Line("Still %s: workspace_run_status run_id=%s wait=120 to follow it, workspace_stop to end it.", r.Status, r.ID)
	case r.Status == "refused" || (r.Status == "failed" && r.Kind == "build"):
		doc.Line("Fix the files and build again.")
	case r.Kind == "run" && r.Status == "succeeded":
		doc.Line("Next: get_session_report with a session_id above.")
	}
}

func addWorkspaceRunStatus(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_run_status",
		mcp.WithDescription("Where a build or run is: status, exit code, error, its backtest sessions and the last lines of its output. `wait` follows a run still going for up to that many seconds."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Run UUID")),
		mcp.WithNumber("wait", mcp.Description("Seconds to wait for the run to end, at most 170"), mcp.DefaultNumber(0)),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("run_id")
		if err != nil {
			return toolErr(err), nil
		}
		var run workspaceRun
		if err := d.getJSON(ctx, "workspace_run_status", client.Workspace, runPath(id), nil, &run); err != nil {
			return toolErr(err), nil
		}
		return d.reportRun(ctx, "workspace_run_status", run, waitArg(req, 0))
	})
}

func addWorkspaceLogs(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_logs",
		mcp.WithDescription("A page of a run's output (stdout and stderr), from `offset` bytes. The answer says the offset to ask for next. A run's output is capped at 1 MB."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Run UUID")),
		mcp.WithNumber("offset", mcp.Description("Byte offset to read from"), mcp.DefaultNumber(0)),
		mcp.WithNumber("limit", mcp.Description("Bytes to read, at most 65536"), mcp.DefaultNumber(logPage)),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("run_id")
		if err != nil {
			return toolErr(err), nil
		}
		offset := max(req.GetInt("offset", 0), 0)
		limit := min(max(req.GetInt("limit", logPage), 1), logPage)
		q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}
		var page workspaceLog
		if err := d.getJSON(ctx, "workspace_logs", client.Workspace, runPath(id)+"/log", q, &page); err != nil {
			return toolErr(err), nil
		}
		var b strings.Builder
		b.WriteString(page.Data)
		if !strings.HasSuffix(page.Data, "\n") && page.Data != "" {
			b.WriteString("\n")
		}
		switch {
		case page.More:
			fmt.Fprintf(&b, "[bytes %d–%d; more follows: offset=%d]", page.Offset, page.Next, page.Next)
		case page.Truncated:
			fmt.Fprintf(&b, "[end at byte %d; output was capped, the rest dropped]", page.Next)
		default:
			fmt.Fprintf(&b, "[end at byte %d]", page.Next)
		}
		return toolText(b.String()), nil
	})
}

func addWorkspaceStop(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_stop",
		mcp.WithDescription("Stop a build or run that is still going. Its key is revoked; a backtest session it opened stays as far as it got."),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Run UUID")),
		destructive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("run_id")
		if err != nil {
			return toolErr(err), nil
		}
		if err := d.sendJSON(ctx, "workspace_stop", http.MethodPost, client.Workspace, runPath(id)+"/stop", nil, nil); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Stopping %s: workspace_run_status shows when it has ended.", id)), nil
	})
}

func addWorkspaceRuns(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_runs",
		mcp.WithDescription("A workspace's builds and runs, newest first: id, kind, status, exit code, when, and the key its backtest sessions are listed under."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithNumber("limit", mcp.Description("Rows to return, at most 100"), mcp.DefaultNumber(20)),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		limit, _ := pageArgs(req, 20, 100)
		var runs []workspaceRun
		if err := d.getJSON(ctx, "workspace_runs", client.Workspace, wsPath(id)+"/runs", url.Values{"limit": {strconv.Itoa(limit)}}, &runs); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		if len(runs) == 0 {
			doc.Line("No builds or runs yet.")
			return toolText(doc.String()), nil
		}
		rows := make([][]string, 0, len(runs))
		for _, r := range runs {
			exit := render.Dash
			if r.ExitCode != nil {
				exit = render.Int(*r.ExitCode)
			}
			key := render.Dash
			if r.KeyID != "" {
				key = r.KeyID
			}
			rows = append(rows, []string{r.ID, r.Kind, r.Status, exit, render.Time(r.CreatedAt), key})
		}
		doc.Table("", []string{"run_id", "kind", "status", "exit", "created", "key_id"}, rows, nil)
		return toolText(doc.String()), nil
	})
}

func addWorkspaceDelete(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_delete",
		mcp.WithDescription("Delete a workspace with its files and its run history. Its backtest sessions stay. Refused while a run is still going."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		destructive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		if err := d.sendJSON(ctx, "workspace_delete", http.MethodDelete, client.Workspace, wsPath(id), nil, nil); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Deleted workspace %s.", id)), nil
	})
}
