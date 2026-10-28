package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
// sandbox machine (the caller's own on admin and paid tiers, shared on free
// ones), so no local Go, keys or checkout are needed. All call workspace_service/api/v1/dev/* via the kdraigo.com gateway.
func RegisterWorkspace(s *server.MCPServer, d Deps) {
	addWorkspaceCreate(s, d)
	addWorkspaceList(s, d)
	addWorkspaceFiles(s, d)
	addWorkspaceReadFile(s, d)
	addWorkspaceWriteFile(s, d)
	addWorkspaceDeleteFile(s, d)
	addWorkspaceBuild(s, d)
	addWorkspaceParams(s, d)
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
	// workspaceFile: Version goes up with each write; UpdatedBy is api (an
	// API key: this server) or lab (the website's editor, where the user may
	// be editing the same files).
	workspaceFile struct {
		Path      string    `json:"path"`
		Size      int       `json:"size"`
		Version   int64     `json:"version"`
		UpdatedBy string    `json:"updated_by"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	workspaceDetail struct {
		Workspace workspace       `json:"workspace"`
		Files     []workspaceFile `json:"files"`
	}
	workspaceRun struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
		Kind        string `json:"kind"`
		Status      string `json:"status"`
		Active      bool   `json:"active"`
		// AlreadyRunning: the start matched a run already going with the same
		// source and settings (a retry), and this is that run.
		AlreadyRunning bool `json:"already_running"`
		// Pool is dedicated (a machine of the user's own) or shared;
		// QueuePosition is a queued shared run's place, from 1.
		Pool          string   `json:"pool"`
		QueuePosition int      `json:"queue_position"`
		SDKVersion    string   `json:"sdk_version"`
		EnvNames      []string `json:"env_names"`
		// Params are the values the run used for its declared parameters.
		Params   map[string]string `json:"params"`
		KeyID    string            `json:"key_id"`
		ExitCode *int              `json:"exit_code"`
		Error    *string           `json:"error"`
		Output   *string           `json:"output"`
		// Warnings: what the lint found, file:line: message per line.
		Warnings     *string    `json:"warnings"`
		LogBytes     int        `json:"log_bytes"`
		LogTruncated bool       `json:"log_truncated"`
		CreatedAt    time.Time  `json:"created_at"`
		StartedAt    *time.Time `json:"started_at"`
		FinishedAt   *time.Time `json:"finished_at"`
		// Variants are a sweep's variants in order; empty on a plain run.
		Variants []runVariant `json:"variants"`
	}
	// runVariant is one of a sweep's settings and how its run went: its
	// declared values, the other names it set (never their values), and the
	// key its sessions are found by.
	runVariant struct {
		Params   map[string]string `json:"params"`
		EnvNames []string          `json:"env_names"`
		Status   string            `json:"status"`
		ExitCode *int              `json:"exit_code"`
		Error    string            `json:"error"`
		KeyID    string            `json:"key_id"`
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

// waitArg reads the wait argument (seconds), capped at maxWait and the
// server's own WaitCap.
func (d Deps) waitArg(req mcp.CallToolRequest, def time.Duration) time.Duration {
	limit := maxWait
	if d.WaitCap > 0 {
		limit = min(limit, d.WaitCap)
	}
	w := time.Duration(req.GetInt("wait", int(def/time.Second))) * time.Second
	return min(max(w, 0), limit)
}

func addWorkspaceCreate(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_create",
		mcp.WithDescription("Create a cloud workspace: a strategy's .go files kept on the platform, built and run on a platform sandbox machine (no local Go or keys needed). "+
			"It starts from a template (basic, with-indicators, with-risk-manager, and from dev_sdk v1.8.1 with-atr-bracket: ATR stop and target as one bracket) on a dev_sdk version; the platform owns go.mod, so a workspace holds flat .go files in package main only. "+
			"Then edit with workspace_write_file and run with workspace_run. Read get_sdk_docs section=\"cloud workspace\" first."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Workspace name: starts with a letter; letters, digits, _ and -; unique per account")),
		mcp.WithString("template", mcp.Description("Starter strategy: basic, with-indicators, with-risk-manager or (dev_sdk v1.8.1 and later) with-atr-bracket"), mcp.DefaultString("basic")),
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
		version := render.Dash
		if f.Version > 0 {
			version = strconv.FormatInt(f.Version, 10)
		}
		rows = append(rows, []string{f.Path, render.Int(f.Size), version, writer(f.UpdatedBy), render.Time(f.UpdatedAt)})
	}
	doc.Table("", []string{"path", "bytes", "version", "by", "updated"}, rows, nil)
}

// writer names who wrote a file version: the user in the Lab, or an API key
// (this server, for the user's AI).
func writer(by string) string {
	switch by {
	case "lab":
		return "the Lab"
	case "api":
		return "API"
	default:
		return render.Dash
	}
}

func addWorkspaceReadFile(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_read_file",
		mcp.WithDescription("A workspace file's full content, exactly as stored, then a note with its version: pass that as base_version to workspace_write_file. "+
			"The user may be editing the same files in the website's Lab."),
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
			workspaceFile
		}
		if err := d.getJSON(ctx, "workspace_read_file", client.Workspace, wsPath(id)+"/files/"+url.PathEscape(path), nil, &f); err != nil {
			return toolErr(err), nil
		}
		if f.Version == 0 {
			return toolText(f.Content), nil
		}
		// The content stays a block of its own, exactly as stored.
		note := fmt.Sprintf("(%s: version %d, written by %s at %s. Change it with workspace_write_file base_version=%d.)",
			path, f.Version, writer(f.UpdatedBy), render.Time(f.UpdatedAt), f.Version)
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent(f.Content), mcp.NewTextContent(note)}}, nil
	})
}

func addWorkspaceWriteFile(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_write_file",
		mcp.WithDescription("Create or replace a workspace file with the whole new content. Files are flat .go files in package main (no folders, no go.mod: the platform owns it); at most 50 files, 256 KB each, 1 MB in all. "+
			"Imports are checked when the workspace is built: the standard library minus os/exec, net, unsafe, syscall and cgo, plus github.com/kdraigo/dev_sdk. "+
			"Pass base_version, the version workspace_read_file showed (0 for a new file): the user may be editing in the website's Lab, and a file that changed since you read it is then refused instead of overwritten."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File name, e.g. main.go or risk.go")),
		mcp.WithString("content", mcp.Required(), mcp.Description("The file's full new content")),
		mcp.WithNumber("base_version", mcp.Description("The version you read (workspace_read_file or workspace_files); 0 creates a new file only. Without it the file is replaced whatever it holds.")),
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
		body := map[string]any{"content": content}
		base, ok, err := versionArg(req, "base_version", 0)
		if err != nil {
			return toolErr(err), nil
		}
		if ok {
			body["base_version"] = base
		}
		resp, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodPut, client.Workspace, wsPath(id)+"/files/"+url.PathEscape(path), nil, body)
		if err != nil {
			return toolErr(fmt.Errorf("workspace_write_file: %w", err)), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(fileErr("workspace_write_file", "base_version", path, status, resp)), nil
		}
		var f workspaceFile
		if json.Unmarshal(resp, &f) != nil || f.Version == 0 {
			return toolText(fmt.Sprintf("Wrote %s (%d bytes).", path, len(content))), nil
		}
		return toolText(fmt.Sprintf("Wrote %s (%d bytes): now version %d.", path, len(content), f.Version)), nil
	})
}

func addWorkspaceDeleteFile(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_delete_file",
		mcp.WithDescription("Delete a file from a workspace. Pass version, the one you read, so a file the user changed in the Lab since is not deleted."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithString("path", mcp.Required(), mcp.Description("File name")),
		mcp.WithNumber("version", mcp.Description("Delete only if the file is still at this version")),
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
		var q url.Values
		version, ok, err := versionArg(req, "version", 1)
		if err != nil {
			return toolErr(err), nil
		}
		if ok {
			q = url.Values{"version": {strconv.FormatInt(version, 10)}}
		}
		resp, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodDelete, client.Workspace, wsPath(id)+"/files/"+url.PathEscape(path), q, nil)
		if err != nil {
			return toolErr(fmt.Errorf("workspace_delete_file: %w", err)), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(fileErr("workspace_delete_file", "version", path, status, resp)), nil
		}
		return toolText(fmt.Sprintf("Deleted %s.", path)), nil
	})
}

// versionArg reads an optional file version argument, a whole number no
// smaller than least; ok says whether it was given.
func versionArg(req mcp.CallToolRequest, name string, least int64) (int64, bool, error) {
	v, ok := req.GetArguments()[name]
	if !ok || v == nil {
		return 0, false, nil
	}
	var f float64
	isNum := true
	switch n := v.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	default:
		isNum = false
	}
	if !isNum || f != math.Trunc(f) || f < float64(least) {
		return 0, false, fmt.Errorf("%s must be a whole number of at least %d", name, least)
	}
	return int64(f), true, nil
}

// fileErr is a refused file write or delete. A 409 means the file moved on
// since it was read: the AI is told what to do, with arg (the tool's version
// argument), rather than given the JSON.
func fileErr(name, arg, path string, status int, body []byte) error {
	if status != http.StatusConflict {
		return httpErr(name, status, body)
	}
	var c struct {
		CurrentVersion int64  `json:"current_version"`
		UpdatedBy      string `json:"updated_by"`
	}
	if json.Unmarshal(body, &c) != nil {
		return httpErr(name, status, body)
	}
	if c.CurrentVersion == 0 {
		return fmt.Errorf("%s: %s was deleted since you read it; nothing was changed. To create it again, write it with base_version=0", name, path)
	}
	return fmt.Errorf("%s: %s changed since you read it (now version %d, written by %s); nothing was changed. "+
		"Read it again with workspace_read_file, keep that change, and retry with %s=%d", name, path, c.CurrentVersion, writer(c.UpdatedBy), arg, c.CurrentVersion)
}

func addWorkspaceBuild(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_build",
		mcp.WithDescription("Compile a workspace on a sandbox machine without running it: the fast check after an edit. Returns the import check's or the compiler's errors as file:line: message, and go vet and staticcheck warnings. A workspace with _test.go files has its tests run too (offline, 1 min at most); a failing test fails the build with go test's output. "+
			"A machine that has to start takes about 40 s; on a verified account a full shared pool queues the build."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithNumber("wait", mcp.Description("Seconds to wait for the result, at most 170"), mcp.DefaultNumber(defaultBuildWait.Seconds())),
		additive(false),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		return d.startRun(ctx, "workspace_build", id, map[string]any{"kind": "build"}, d.waitArg(req, defaultBuildWait))
	})
}

func addWorkspaceRun(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_run",
		mcp.WithDescription("Build a workspace and run it on a sandbox machine: the strategy opens its backtest session with a key minted for this run alone (revoked when it ends). "+
			"Waits up to `wait` seconds; a run still going returns its id for workspace_run_status. The result names the backtest sessions the run made: pass one to get_session_report. "+
			"`env` sets your own variables (upper-case names, not KDRAIGO_*), e.g. {\"STRATEGY_RISK\":\"0.02\"}. "+
			"`smoke` (dev_sdk v1.9.0 and later) first runs the window's first 7 days and starts the full run only if that exits cleanly: a panic or a refused order shows in seconds. "+
			"`variants` makes it a sweep: 2 or more settings, each over `env`, run one after another on one build; the result is one table comparing them (return, drawdown, trades, win rate, PF, fees). "+
			"Each variant counts as a run toward the day's runs, and all share the run's time."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		mcp.WithObject("env", mcp.Description("Environment variables for the strategy, name → value")),
		mcp.WithBoolean("smoke", mcp.Description("Run the first 7 days first, then the full run if that passed (dev_sdk v1.9.0 and later)")),
		mcp.WithArray("variants", mcp.Description("A sweep: 2 or more objects of settings (name → value), e.g. [{\"STOP_ATR\":\"1.5\"},{\"STOP_ATR\":\"2\"}]; at most 10 (verified), 20 (pro), 50 (admin)"),
			mcp.Items(map[string]any{"type": "object"})),
		mcp.WithNumber("wait", mcp.Description("Seconds to wait for the run to end, at most 170 (60 on the hosted server)"), mcp.DefaultNumber(defaultRunWait.Seconds())),
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
		if req.GetBool("smoke", false) {
			body["smoke"] = true
		}
		variants, err := variantsArg(req)
		if err != nil {
			return toolErr(err), nil
		}
		if len(variants) > 0 {
			body["variants"] = variants
		}
		return d.startRun(ctx, "workspace_run", id, body, d.waitArg(req, defaultRunWait))
	})
}

// variantsArg reads a sweep's variants: objects of settings, valued like env.
func variantsArg(req mcp.CallToolRequest) ([]map[string]string, error) {
	raw, ok := req.GetArguments()["variants"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("variants must be an array of objects of name → value")
	}
	out := make([]map[string]string, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("variant %d must be an object of name → value", i+1)
		}
		v, err := stringValues(obj, fmt.Sprintf("variant %d", i+1))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
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
	return stringValues(obj, "env")
}

// stringValues turns an object's values into strings: strings, numbers and
// booleans as written.
func stringValues(obj map[string]any, what string) (map[string]string, error) {
	env := make(map[string]string, len(obj))
	for k, v := range obj {
		switch v := v.(type) {
		case string:
			env[k] = v
		case float64:
			env[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case int:
			env[k] = strconv.Itoa(v)
		case bool:
			env[k] = strconv.FormatBool(v)
		default:
			return nil, fmt.Errorf("%s %s: a value must be a string, number or boolean", what, k)
		}
	}
	return env, nil
}

// startRun starts a build or run and reports it once it ends or wait passes.
// A start refused because the account's run is already going in this
// workspace (a retry after a cut call, most often) follows that run instead.
func (d Deps) startRun(ctx context.Context, name, workspaceID string, body map[string]any, wait time.Duration) (*mcp.CallToolResult, error) {
	resp, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodPost, client.Workspace, wsPath(workspaceID)+"/runs", nil, body)
	if err != nil {
		return toolErr(fmt.Errorf("%s: %w", name, err)), nil
	}
	if status == http.StatusTooManyRequests && strings.Contains(string(resp), "at a time") {
		if run, ok := d.activeRun(ctx, name, workspaceID); ok {
			run.AlreadyRunning = true
			return d.reportRun(ctx, name, run, wait)
		}
	}
	if status < 200 || status >= 300 {
		return toolErr(httpErr(name, status, resp)), nil
	}
	var run workspaceRun
	if err := json.Unmarshal(resp, &run); err != nil {
		return toolErr(fmt.Errorf("%s: decode response: %w", name, err)), nil
	}
	return d.reportRun(ctx, name, run, wait)
}

// activeRun is the workspace's newest run still going, if any.
func (d Deps) activeRun(ctx context.Context, name, workspaceID string) (workspaceRun, bool) {
	var runs []workspaceRun
	if err := d.getJSON(ctx, name, client.Workspace, wsPath(workspaceID)+"/runs", url.Values{"limit": {"5"}}, &runs); err != nil {
		return workspaceRun{}, false
	}
	for _, r := range runs {
		if r.Active {
			return r, true
		}
	}
	return workspaceRun{}, false
}

// reportRun waits for run (up to wait) and renders where it got to.
func (d Deps) reportRun(ctx context.Context, name string, run workspaceRun, wait time.Duration) (*mcp.CallToolResult, error) {
	r, err := d.waitRun(ctx, name, run, wait)
	if err != nil {
		return toolErr(err), nil
	}
	// The status reads while waiting do not carry it: only the start knew.
	r.AlreadyRunning = r.AlreadyRunning || run.AlreadyRunning
	doc := render.New()
	renderRunHead(doc, r)
	switch {
	case r.Kind == "run" && len(r.Variants) > 0:
		d.renderSweep(ctx, doc, r)
	case r.Kind == "run" && r.KeyID != "":
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
	if r.Status == "queued" && r.QueuePosition > 0 {
		head += fmt.Sprintf(", number %d in line for a shared machine", r.QueuePosition)
	}
	if r.ExitCode != nil && !r.Active {
		head += fmt.Sprintf(", exit %d", *r.ExitCode)
	}
	if r.StartedAt != nil && r.FinishedAt != nil {
		head += fmt.Sprintf(", %s", r.FinishedAt.Sub(*r.StartedAt).Round(100*time.Millisecond))
	}
	doc.Line("%s.", head)
	if r.AlreadyRunning {
		doc.Line("Already going: this %s was started before (the same start again, or the account's one run at a time), so it is followed rather than a second one started.", r.Kind)
	}
	if len(r.Params) > 0 {
		names := make([]string, 0, len(r.Params))
		for n := range r.Params {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = n + "=" + r.Params[n]
		}
		if len(r.Variants) > 0 {
			doc.Line("Base parameters, under each variant's own: %s.", strings.Join(parts, " "))
		} else {
			doc.Line("Parameters: %s.", strings.Join(parts, " "))
		}
	}
	if len(r.EnvNames) > 0 {
		doc.Line("Variables given: %s.", strings.Join(r.EnvNames, ", "))
	}
	if r.Error != nil && *r.Error != "" {
		doc.Line("Error: %s", *r.Error)
	}
	if r.Output != nil && strings.TrimSpace(*r.Output) != "" {
		what := "Build output"
		if r.Status == "refused" {
			what = "Refused before any machine started (import check, parameters, backtest settings)"
		}
		doc.Line("%s:", what)
		doc.Line("```text\n%s\n```", strings.TrimRight(*r.Output, "\n"))
	}
	if r.Warnings != nil && strings.TrimSpace(*r.Warnings) != "" {
		doc.Line("Warnings (the run goes ahead; each says what to change):")
		doc.Line("```text\n%s\n```", strings.TrimRight(*r.Warnings, "\n"))
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
		mcp.WithNumber("wait", mcp.Description("Seconds to wait for the run to end, at most 170 (60 on the hosted server)"), mcp.DefaultNumber(0)),
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
		return d.reportRun(ctx, "workspace_run_status", run, d.waitArg(req, 0))
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
