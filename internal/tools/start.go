package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// startBudget bounds get_started: every section is read in parallel, and one
// that has not answered by then is shown as unavailable.
const startBudget = 10 * time.Second

// accountView is workspace_service /api/v1/dev/account.
type accountView struct {
	Account string `json:"account"`
	Tier    string `json:"tier"`
	Google  bool   `json:"google"`
	Limits  struct {
		BacktestPairs        int   `json:"backtest_pairs"`
		BacktestBars         int   `json:"backtest_bars"`
		RunningSessions      int   `json:"running_sessions"`
		OrdersPerSession     int   `json:"orders_per_session"`
		SessionBudgetSeconds int64 `json:"session_budget_seconds"`
		Workspaces           int   `json:"workspaces"`
		SandboxRuns          int   `json:"sandbox_runs"`
		SandboxHoursPerDay   int   `json:"sandbox_hours_per_day"`
		SandboxRunSeconds    int64 `json:"sandbox_run_seconds"`
		SandboxDedicated     bool  `json:"sandbox_dedicated"`
		ResearchLabs         int   `json:"research_labs"`
		SweepVariants        int   `json:"sweep_variants"`
		RunsPerDay           int   `json:"runs_per_day"`
	} `json:"limits"`
	Usage struct {
		Workspaces  int       `json:"workspaces"`
		RunsToday   int       `json:"runs_today"`
		ActiveRuns  int       `json:"active_runs"`
		UsedSeconds int64     `json:"used_seconds"`
		ResetsAt    time.Time `json:"resets_at"`
	} `json:"usage"`
}

// startIntents are the tasks a user most often brings, each with the call
// that begins it.
var startIntents = [][2]string{
	{"Carry on earlier research", "lab_list, then lab_open (brief, last handoff, experiments, runs)"},
	{"Try a new strategy idea", "library_search (has it been tried?), lab_create, then workspace_create lab=…"},
	{"Write and backtest a strategy", "get_guide topic=workspace, workspace_create, workspace_write_file, workspace_run"},
	{"Read a backtest's results", "get_session_report, then get_trades / get_breakdown"},
	{"Is this result real?", "validate_strategy trials=N (Monte Carlo, significance, luck from the search)"},
	{"Compare settings", "workspace_run variants=[…] (a sweep), then validate the median variant, not the best"},
	{"How deep can the drawdown get / what if I risk less?", "get_monte_carlo (dd_pct=…, risk_pct=…)"},
	{"How are my live bots doing?", "list_live_sessions, get_live_session, get_session_report session_type=live"},
	{"A desk of charts and data", "widget_types, board_create, widget_add"},
	{"A custom indicator or a Telegram alert", "script_library_search, script_reference, script_save, alert_create"},
	{"Market data", "get_data_coverage (what exists), get_candles, get_flow, get_positioning"},
}

// RegisterStart adds get_started.
func RegisterStart(s *server.MCPServer, d Deps) { addGetStarted(s, d) }

func addGetStarted(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_started",
		mcp.WithDescription("Start here when the user is new, asks what kdraigo can do, or what their plan allows. One call: the account's plan and limits "+
			"(pairs and bars per backtest, sweep size, runs a day, workspaces) and today's use, what the account already has (labs and where they stopped, workspaces, live bots, boards, scripts, alerts, the latest backtest), "+
			"what market data exists, and which tool each kind of task starts with. Reads only."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return toolText(d.startPage(ctx)), nil
	})
}

func (d Deps) startPage(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, startBudget)
	defer cancel()
	var (
		wg                                       sync.WaitGroup
		acct                                     accountView
		labs                                     []lab
		workspaces                               []workspace
		live                                     []liveSession
		boards                                   []board
		scripts                                  []script
		alerts                                   []scriptAlert
		latest                                   []sessionSummary
		markets                                  map[string]int
		acctErr, labsErr, wsErr, liveErr         error
		boardsErr, scriptsErr, alertsErr, bktErr error
		dataErr                                  error
	)
	wg.Go(func() {
		acctErr = d.getJSON(ctx, "account", client.Workspace, "/api/v1/dev/account", nil, &acct)
	})
	wg.Go(func() { labsErr = d.getJSON(ctx, "labs", client.Research, labsAPI+"/labs", nil, &labs) })
	wg.Go(func() {
		wsErr = d.getJSON(ctx, "workspaces", client.Workspace, "/api/v1/dev/workspaces", nil, &workspaces)
	})
	wg.Go(func() { live, liveErr = d.liveSessions(ctx) })
	wg.Go(func() { boardsErr = d.getJSON(ctx, "boards", client.Research, labsAPI+"/boards", nil, &boards) })
	wg.Go(func() { scriptsErr = d.getJSON(ctx, "scripts", client.Research, labsAPI+"/scripts", nil, &scripts) })
	wg.Go(func() { alertsErr = d.getJSON(ctx, "alerts", client.Research, labsAPI+"/alerts", nil, &alerts) })
	wg.Go(func() {
		q := url.Values{"limit": {"1"}, "offset": {"0"}}
		bktErr = d.getJSON(ctx, "backtests", client.FrontendAPI, "/api/v1/dev/sessions", q, &latest)
	})
	wg.Go(func() { markets, dataErr = d.marketCounts(ctx) })
	wg.Wait()

	doc := render.New()
	if d.Version != "" {
		doc.Line("kdraigo MCP %s · get_guide topic=<name> for how to do each task (no topic lists them).", d.Version)
	} else {
		doc.Line("kdraigo · get_guide topic=<name> for how to do each task (no topic lists them).")
	}

	doc.Blank()
	doc.Line("## Your plan")
	if acctErr != nil {
		why := shortErr(acctErr)
		if strings.Contains(why, "HTTP 404") {
			why = "this platform does not report plans to the MCP yet"
		}
		doc.Line("Unavailable (%s). Limits still show where a run is refused: the refusal names the limit.", why)
	} else {
		l := &acct.Limits
		doc.Line("Account: %s%s.", acct.Account, googleNote(acct.Google))
		doc.Line("Backtests: %s pairs and %s bars per pair each (bars of the session's timeframe: 1m bars go 60× faster than 1h), %d running at once, %s orders each.",
			capNum(l.BacktestPairs), capNum(l.BacktestBars), l.RunningSessions, capNum(l.OrdersPerSession))
		if l.SandboxRuns > 0 {
			runs := "no daily cap"
			if l.RunsPerDay > 0 {
				runs = fmt.Sprintf("%d of %d runs used today (UTC, resets %s)", acct.Usage.RunsToday, l.RunsPerDay, render.Time(acct.Usage.ResetsAt))
			}
			machine := "shared machines (a run may queue)"
			if l.SandboxDedicated {
				machine = "its own machine"
			}
			doc.Line("Cloud workspace: %d of %d workspaces, %d run(s) at a time on %s, sweeps of up to %d variants, %s.",
				acct.Usage.Workspaces, l.Workspaces, l.SandboxRuns, machine, l.SweepVariants, runs)
		} else {
			doc.Line("Cloud workspace: not on this plan; strategies run locally (scaffold_strategy, go run .). Linking Telegram or signing in with Google opens it.")
		}
		if l.ResearchLabs > 0 {
			doc.Line("Research labs: up to %d.", l.ResearchLabs)
		}
	}

	doc.Blank()
	doc.Line("## What you have")
	section := func(name string, err error, n int, detail string) {
		switch {
		case err != nil:
			doc.Line("- %s: unavailable (%s)", name, shortErr(err))
		case n == 0:
			doc.Line("- %s: none yet%s", name, detail)
		default:
			doc.Line("- %s: %d%s", name, n, detail)
		}
	}
	section("Labs", labsErr, len(labs), labsDetail(labs))
	section("Workspaces", wsErr, len(workspaces), "")
	section("Live bots", liveErr, len(live), liveDetail(live))
	section("Boards", boardsErr, len(boards), "")
	section("Chart scripts", scriptsErr, len(scripts), "")
	section("Alerts", alertsErr, len(alerts), alertsDetail(alerts))
	switch {
	case bktErr != nil:
		doc.Line("- Latest backtest: unavailable (%s)", shortErr(bktErr))
	case len(latest) == 0:
		doc.Line("- Latest backtest: none yet")
	default:
		b := latest[0]
		name := ""
		if b.Name != "" {
			name = " " + b.Name
		}
		doc.Line("- Latest backtest: %s%s · %s · %s (get_session_report session_id=%s)", b.ID, name, b.Status, render.Date(b.CreatedAt), b.ID)
	}
	doc.Line("Each account sees only its own: if something is missing, it may be on the user's other account (wallet or Google).")

	doc.Blank()
	doc.Line("## Market data")
	if dataErr != nil {
		doc.Line("Unavailable (%s); get_data_coverage lists it.", shortErr(dataErr))
	} else {
		ex := make([]string, 0, len(markets))
		for e := range markets {
			ex = append(ex, e)
		}
		sort.Strings(ex)
		parts := make([]string, 0, len(ex))
		total := 0
		for _, e := range ex {
			parts = append(parts, fmt.Sprintf("%s %d", e, markets[e]))
			total += markets[e]
		}
		doc.Line("%d markets: %s. Crypto plus Binance TradFi (stocks, ETFs, metals) on perpetuals and spot; 1m history, trade flow, open interest and long/short ratios. get_data_coverage gives each market's first bar and what it is.", total, strings.Join(parts, " · "))
	}

	doc.Blank()
	rows := make([][]string, 0, len(startIntents))
	for _, it := range startIntents {
		rows = append(rows, []string{it[0], it[1]})
	}
	doc.Table("What you can do, and where it starts", []string{"task", "start with"}, rows, nil)
	return doc.String()
}

// marketCounts counts the markets data_provider holds, per exchange.
func (d Deps) marketCounts(ctx context.Context) (map[string]int, error) {
	body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/metadata", nil, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, httpErr("market data", status, body)
	}
	var meta map[string]struct {
		Symbols map[string]json.RawMessage `json:"symbols"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("market data: decode: %w", err)
	}
	out := make(map[string]int, len(meta))
	for ex, m := range meta {
		out[ex] = len(m.Symbols)
	}
	return out, nil
}

func googleNote(google bool) string {
	if google {
		return ", signed in with Google"
	}
	return ""
}

func capNum(n int) string {
	if n <= 0 {
		return "no cap on"
	}
	return render.Int(n)
}

func labsDetail(labs []lab) string {
	if len(labs) == 0 {
		return " (lab_create starts one; templates futures-trend, spot-accumulation)"
	}
	newest := labs[0]
	for _, l := range labs[1:] {
		if l.UpdatedAt.After(newest.UpdatedAt) {
			newest = l
		}
	}
	ref := newest.Ref
	if ref == "" {
		ref = newest.Slug
	}
	return fmt.Sprintf(" · newest %q (lab_open lab=%s, updated %s)", newest.Name, ref, render.Date(newest.UpdatedAt))
}

func liveDetail(live []liveSession) string {
	if len(live) == 0 {
		return " (a bot appears once it sends telemetry)"
	}
	counts := map[string]int{}
	for _, s := range live {
		counts[strings.ToUpper(s.Status)]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", counts[k], strings.ToLower(k))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func alertsDetail(alerts []scriptAlert) string {
	active := 0
	for _, a := range alerts {
		if strings.EqualFold(a.Status, "active") {
			active++
		}
	}
	if len(alerts) == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d active)", active)
}

// shortErr is an error's message without the call name, kept short.
func shortErr(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && i < 30 {
		msg = msg[i+2:]
	}
	return render.Clip(msg, 120)
}
