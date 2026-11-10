package tools

import (
	"context"
	"encoding/json"
	"errors"
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

// analytics_service POST /analytics/overfitting: a sweep's variants on the
// same days, answered with PBO, walk-forward, the deflated Sharpe and
// parameter sensitivity.

const (
	maxSweepRuns     = 5
	maxSweepSessions = 100
)

type (
	overfitVariant struct {
		ID             string            `json:"id"`
		Params         map[string]string `json:"params"`
		Sharpe         *float64          `json:"sharpe"`
		ReturnPct      float64           `json:"return_pct"`
		MaxDrawdownPct float64           `json:"max_drawdown_pct"`
		PSR            *float64          `json:"psr_zero"`
		Rank           int               `json:"rank"`
	}
	overfitPBO struct {
		Blocks       int      `json:"blocks"`
		BlockDays    int      `json:"block_days"`
		DaysUsed     int      `json:"days_used"`
		Combinations int      `json:"combinations"`
		PBO          float64  `json:"pbo"`
		ProbOOSLoss  float64  `json:"prob_oos_loss"`
		Slope        *float64 `json:"slope"`
		MedianRank   float64  `json:"median_rank"`
		Logits       []struct {
			From  float64 `json:"from"`
			To    float64 `json:"to"`
			Count int     `json:"count"`
		} `json:"logits"`
	}
	overfitFold struct {
		ISFrom       int     `json:"is_from"`
		ISTo         int     `json:"is_to"`
		OOSFrom      int     `json:"oos_from"`
		OOSTo        int     `json:"oos_to"`
		Pick         int     `json:"pick"`
		PickID       string  `json:"pick_id"`
		ISAnnualPct  float64 `json:"is_annual_pct"`
		OOSAnnualPct float64 `json:"oos_annual_pct"`
		OOSReturnPct float64 `json:"oos_return_pct"`
	}
	overfitWF struct {
		Anchored               bool          `json:"anchored"`
		ISDays                 int           `json:"is_days"`
		OOSDays                int           `json:"oos_days"`
		Folds                  []overfitFold `json:"folds"`
		Days                   int           `json:"days"`
		OOSReturnPct           float64       `json:"oos_return_pct"`
		OOSSharpe              *float64      `json:"oos_sharpe"`
		OOSMaxDrawdownPct      float64       `json:"oos_max_drawdown_pct"`
		OOSPSR                 *float64      `json:"oos_psr_zero"`
		Efficiency             *float64      `json:"efficiency"`
		FoldsPositive          int           `json:"folds_positive"`
		PickChanges            int           `json:"pick_changes"`
		DistinctPicks          int           `json:"distinct_picks"`
		MedianVariantReturnPct float64       `json:"median_variant_return_pct"`
		BestInHindsightPct     float64       `json:"best_in_hindsight_return_pct"`
	}
	overfitDSR struct {
		BestID            string   `json:"best_id"`
		Sharpe            float64  `json:"sharpe"`
		Trials            int      `json:"trials"`
		EffectiveN        float64  `json:"effective_n"`
		TrialsUsed        float64  `json:"trials_used"`
		SharpeSD          float64  `json:"sharpe_sd"`
		ChanceSD          float64  `json:"chance_sd"`
		SharpeSDUsed      float64  `json:"sharpe_sd_used"`
		ExpectedMaxSharpe float64  `json:"expected_max_sharpe"`
		DSR               *float64 `json:"dsr"`
		PSR               *float64 `json:"psr_zero"`
	}
	overfitSensitivity struct {
		Varying []string `json:"varying"`
		SPP     struct {
			Median          float64 `json:"median"`
			P25             float64 `json:"p25"`
			P75             float64 `json:"p75"`
			Best            float64 `json:"best"`
			Worst           float64 `json:"worst"`
			SharePositive   float64 `json:"share_positive"`
			MedianReturnPct float64 `json:"median_return_pct"`
		} `json:"spp"`
		Marginals []struct {
			Param  string `json:"param"`
			Values []struct {
				Value        string  `json:"value"`
				Count        int     `json:"count"`
				MeanSharpe   float64 `json:"mean_sharpe"`
				MedianSharpe float64 `json:"median_sharpe"`
			} `json:"values"`
		} `json:"marginals"`
		BestID              string   `json:"best_id"`
		Neighbours          int      `json:"neighbours"`
		NeighbourMeanSharpe *float64 `json:"neighbour_mean_sharpe"`
		Plateau             *float64 `json:"plateau"`
		Grid                *struct {
			X       string       `json:"x"`
			Y       string       `json:"y"`
			XValues []string     `json:"x_values"`
			YValues []string     `json:"y_values"`
			Sharpe  [][]*float64 `json:"sharpe"`
		} `json:"grid"`
	}
	overfitResponse struct {
		Source   string `json:"source"`
		From     string `json:"from"`
		Sessions []struct {
			SessionID string   `json:"session_id"`
			Trades    int      `json:"trades"`
			High      []string `json:"high_warnings"`
		} `json:"sessions"`
		Days             int                 `json:"days"`
		Objective        string              `json:"objective"`
		Variants         []overfitVariant    `json:"variants"`
		PBO              *overfitPBO         `json:"pbo"`
		PBOError         string              `json:"pbo_error"`
		WalkForward      *overfitWF          `json:"walk_forward"`
		WalkForwardError string              `json:"walk_forward_error"`
		DSR              *overfitDSR         `json:"dsr"`
		Sensitivity      *overfitSensitivity `json:"sensitivity"`
		SensitivityError string              `json:"sensitivity_error"`
		Notes            []string            `json:"notes"`
	}
)

// sweep is a set of sessions to analyse together, and where they came from.
type sweep struct {
	ids    []string
	params []map[string]string
	// trials is what the source knows were tried (an experiment's count);
	// 0 when it knows only these.
	trials int
	what   string
	notes  []string
}

// sweepArgs are the ways to name a sweep, the same on every tool that takes
// one.
func sweepArgs() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("run_id", mcp.Description("A workspace sweep's run id (workspace_run with variants), or up to 5 comma-separated on the same build and window")),
		mcp.WithString("lab", mcp.Description("With experiment: the lab whose runs to take")),
		mcp.WithString("experiment", mcp.Description("With lab: an experiment's runs, those on the window most of them share")),
		mcp.WithArray("session_ids", mcp.Description("Or the variants' backtest sessions directly, 2–100, all over the same days"), mcp.WithStringItems()),
		mcp.WithArray("params", mcp.Description("With session_ids: each one's settings (name → value), in the same order"), mcp.Items(map[string]any{"type": "object"})),
		mcp.WithNumber("trials", mcp.Description("Every variant or idea tried to find the chosen one, these included; default: the experiment's count, or the variants")),
		mcp.WithString("objective", mcp.Description("Rank variants by sharpe (default) or return"), mcp.Enum("sharpe", "return")),
		mcp.WithNumber("folds", mcp.Description("Walk-forward folds, default 6")),
		mcp.WithNumber("is_ratio", mcp.Description("Walk-forward in-sample length over a fold's, default 4")),
		mcp.WithBoolean("anchored", mcp.Description("Walk-forward in-sample grows from the first day instead of rolling")),
	}
}

// hasSweep says whether a call names a sweep at all.
func hasSweep(req mcp.CallToolRequest) bool {
	a := req.GetArguments()
	return strings.TrimSpace(req.GetString("run_id", "")) != "" || strings.TrimSpace(req.GetString("experiment", "")) != "" || a["session_ids"] != nil
}

// resolveSweep turns a call's arguments into sessions with their settings.
func (d Deps) resolveSweep(ctx context.Context, req mcp.CallToolRequest) (*sweep, error) {
	runIDs := strings.TrimSpace(req.GetString("run_id", ""))
	exp := strings.TrimSpace(req.GetString("experiment", ""))
	ids := req.GetStringSlice("session_ids", nil)
	given := 0
	for _, b := range []bool{runIDs != "", exp != "", len(ids) > 0} {
		if b {
			given++
		}
	}
	if given != 1 {
		return nil, errors.New("name the sweep one way: run_id (a workspace sweep), lab + experiment, or session_ids")
	}
	switch {
	case runIDs != "":
		return d.sweepFromRuns(ctx, runIDs)
	case exp != "":
		lab := strings.TrimSpace(req.GetString("lab", ""))
		if lab == "" {
			return nil, errors.New("experiment needs its lab")
		}
		return d.sweepFromExperiment(ctx, lab, exp)
	}
	sw := &sweep{ids: ids, what: fmt.Sprintf("%d sessions", len(ids))}
	if raw, ok := req.GetArguments()["params"].([]any); ok && len(raw) > 0 {
		if len(raw) != len(ids) {
			return nil, errors.New("params has one object per session, in the same order")
		}
		for i, r := range raw {
			obj, ok := r.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("params[%d] must be an object of name → value", i)
			}
			p := map[string]string{}
			for k, v := range obj {
				p[k] = fmt.Sprint(v)
			}
			sw.params = append(sw.params, p)
		}
	}
	return sw, nil
}

func (d Deps) sweepFromRuns(ctx context.Context, list string) (*sweep, error) {
	var runIDs []string
	for _, id := range strings.Split(list, ",") {
		if id = strings.TrimSpace(id); id != "" {
			runIDs = append(runIDs, id)
		}
	}
	if len(runIDs) > maxSweepRuns {
		return nil, fmt.Errorf("at most %d sweeps at once", maxSweepRuns)
	}
	type pending struct {
		key    string
		params map[string]string
	}
	var todo []pending
	sw := &sweep{}
	variants := 0
	for _, id := range runIDs {
		var run workspaceRun
		if err := d.getJSON(ctx, "sweep run", client.Workspace, runPath(id), nil, &run); err != nil {
			return nil, err
		}
		if len(run.Variants) == 0 {
			return nil, fmt.Errorf("run %s is not a sweep (it has no variants): give a sweep's run_id, or session_ids", id)
		}
		variants += len(run.Variants)
		skipped := 0
		for _, v := range run.Variants {
			if v.Status != "succeeded" || v.KeyID == "" {
				skipped++
				continue
			}
			todo = append(todo, pending{v.KeyID, v.Params})
		}
		if skipped > 0 {
			sw.notes = append(sw.notes, fmt.Sprintf("Run %s: %d of %d variants did not succeed and are left out (they still count as trials).", render.Short(id), skipped, len(run.Variants)))
		}
	}
	sw.trials = variants
	if len(runIDs) == 1 {
		sw.what = fmt.Sprintf("sweep %s", render.Short(runIDs[0]))
	} else {
		sw.what = fmt.Sprintf("%d sweeps", len(runIDs))
	}
	ids := make([]string, len(todo))
	errs := make([]error, len(todo))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, p := range todo {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ids[i], errs[i] = d.keySession(ctx, p.key)
		})
	}
	wg.Wait()
	for i := range todo {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if ids[i] == "" {
			continue
		}
		sw.ids = append(sw.ids, ids[i])
		sw.params = append(sw.params, todo[i].params)
	}
	return sw, nil
}

// keySession is the first backtest session a run's key opened ("" for none).
func (d Deps) keySession(ctx context.Context, keyID string) (string, error) {
	q := url.Values{"api_key_id": {keyID}, "limit": {"5"}}
	var list []sessionSummary
	if err := d.getJSON(ctx, "a variant's session", client.FrontendAPI, "/api/v1/dev/sessions", q, &list); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	sort.Slice(list, func(a, b int) bool { return list[a].CreatedAt.Before(list[b].CreatedAt) })
	return list[0].ID, nil
}

func (d Deps) sweepFromExperiment(ctx context.Context, lab, exp string) (*sweep, error) {
	var runs []labRun
	q := url.Values{"experiment": {exp}, "limit": {"100"}}
	if err := d.getJSON(ctx, "experiment runs", client.Research, labPath(lab)+"/runs", q, &runs); err != nil {
		return nil, err
	}
	// fillRuns reads at most ten runs' sessions a call: go on until none is
	// left to read.
	for range 10 {
		left := 0
		for _, r := range runs {
			if r.KeyID != nil && r.FinishedAt != nil && r.Metrics.Sessions == nil {
				left++
			}
		}
		if left == 0 {
			break
		}
		runs = d.fillRuns(ctx, lab, runs)
	}
	var e experiment
	if err := d.getJSON(ctx, "experiment", client.Research, labPath(lab)+"/experiments/"+url.PathEscape(exp), nil, &e); err != nil {
		return nil, err
	}
	// The window most runs share; the rest cannot be compared day by day.
	groups := map[string][]labRun{}
	for _, r := range runs {
		if r.Status != "succeeded" || len(r.SessionIDs) == 0 || r.WindowStart == nil || r.WindowEnd == nil {
			continue
		}
		k := fmt.Sprintf("%s|%s|%s|%s|%s", r.WindowStart.UTC().Format(time.DateOnly), r.WindowEnd.UTC().Format(time.DateOnly),
			r.Exchange, r.Timeframe, strings.Join(r.Pairs, ","))
		groups[k] = append(groups[k], r)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("experiment %s has no finished runs with a session to compare", exp)
	}
	best := ""
	for k, g := range groups {
		if best == "" || len(g) > len(groups[best]) {
			best = k
		}
	}
	sw := &sweep{what: fmt.Sprintf("experiment %s", exp)}
	if e.Trials != nil {
		sw.trials = *e.Trials
	}
	seen := map[string]bool{}
	for _, r := range groups[best] {
		if id := r.SessionIDs[0]; !seen[id] {
			seen[id] = true
			sw.ids = append(sw.ids, id)
			sw.params = append(sw.params, r.Params)
		}
	}
	if left := len(runs) - len(groups[best]); left > 0 {
		parts := strings.Split(best, "|")
		sw.notes = append(sw.notes, fmt.Sprintf("Compared the %d runs on %s → %s; %d other run(s) of the experiment (other windows or data, or unfinished) are left out of the comparison but counted as trials.",
			len(groups[best]), parts[0], parts[1], left))
	}
	return sw, nil
}

// analyseSweep resolves the sweep and asks analytics_service about it.
func (d Deps) analyseSweep(ctx context.Context, req mcp.CallToolRequest) (*overfitResponse, *sweep, error) {
	sw, err := d.resolveSweep(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if len(sw.ids) < 2 {
		return nil, sw, fmt.Errorf("%s: %d finished variant(s) with a session; a sweep analysis needs at least 2", sw.what, len(sw.ids))
	}
	if len(sw.ids) > maxSweepSessions {
		return nil, sw, fmt.Errorf("%s: %d sessions, at most %d in one analysis", sw.what, len(sw.ids), maxSweepSessions)
	}
	trials := int(req.GetFloat("trials", 0))
	if trials == 0 {
		trials = sw.trials
	}
	body := map[string]any{"session_ids": sw.ids, "trials": trials}
	if len(sw.params) == len(sw.ids) {
		body["params"] = sw.params
	}
	if o := req.GetString("objective", ""); o != "" {
		body["objective"] = o
	}
	if f := req.GetFloat("folds", 0); f > 0 {
		body["folds"] = int(f)
	}
	if r := req.GetFloat("is_ratio", 0); r > 0 {
		body["is_ratio"] = r
	}
	if req.GetBool("anchored", false) {
		body["anchored"] = true
	}
	var out overfitResponse
	resp, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodPost, client.Analytics, "/api/v1/dev/analytics/overfitting", nil, body)
	if err != nil {
		return nil, sw, fmt.Errorf("sweep analysis: %w", err)
	}
	if status == http.StatusNotFound && !strings.Contains(string(resp), `"error"`) {
		return nil, sw, errors.New("sweep analysis: this platform's analytics_service does not have /analytics/overfitting yet")
	}
	if status < 200 || status >= 300 {
		return nil, sw, httpErr("sweep analysis", status, resp)
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, sw, fmt.Errorf("sweep analysis: decode: %w", err)
	}
	return &out, sw, nil
}

// RegisterOverfit adds get_overfitting_report, get_walk_forward and
// get_parameter_sensitivity: one analysis, named three ways, because an AI
// finds a capability by its name.
func RegisterOverfit(s *server.MCPServer, d Deps) {
	type part struct {
		name, desc string
		render     func(doc *render.Doc, r *overfitResponse, sw *sweep)
	}
	parts := []part{
		{"get_overfitting_report", "Is a sweep overfit? For a strategy's variants over the same days (a workspace sweep, an experiment's runs, or sessions): " +
			"the Probability of Backtest Overfitting by combinatorially symmetric cross-validation (how often the best variant on one half of the days ranks below the median on the other half), " +
			"the Deflated Sharpe Ratio of the best variant (its Sharpe against what that many trials reach by chance, with the variants' correlation making the count effective), " +
			"walk-forward selection stitched out of sample with its efficiency, and parameter sensitivity (the median over every setting tried, the best setting's neighbours: a plateau or a spike). " +
			"Grades each part. Run it after a sweep and before choosing a setting; it spends no runs.",
			renderOverfitReport},
		{"get_walk_forward", "Walk-forward analysis of a sweep: on each in-sample stretch pick the best variant, hold it over the next stretch, and stitch those out-of-sample stretches together: " +
			"their return, Sharpe and drawdown, walk-forward efficiency (out-of-sample annual return over in-sample), folds positive, how often the pick changed, against holding the median variant. " +
			"Uses the sweep's runs as they are (no re-runs): a switch at a fold boundary takes over the variant's running book.",
			renderWalkForwardOnly},
		{"get_parameter_sensitivity", "Parameter sensitivity of a sweep: how the result moves with the settings. The median Sharpe over every setting tried (System Parameter Permutation: the honest estimate, the best being the lucky bound), " +
			"the best setting's neighbours (one step away in every number: a plateau if they do nearly as well, a spike if not), each setting's marginal effect, and a grid for two numeric settings.",
			renderSensitivityOnly},
	}
	for _, p := range parts {
		opts := append([]mcp.ToolOption{mcp.WithDescription(p.desc)}, sweepArgs()...)
		opts = append(opts, readOnly())
		tool := mcp.NewTool(p.name, opts...)
		addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			r, sw, err := d.analyseSweep(ctx, req)
			if err != nil {
				return toolErr(err), nil
			}
			doc := render.New()
			p.render(doc, r, sw)
			return toolText(doc.String()), nil
		})
	}
}

func renderSweepHeader(doc *render.Doc, r *overfitResponse, sw *sweep) {
	src := "mark-to-market days"
	if r.Source == "closed_trades" {
		src = "closed-trade days"
	}
	doc.Line("Sweep analysis of %s: %d variants · %d %s from %s · ranked by %s.", sw.what, len(r.Variants), r.Days, src, r.From, r.Objective)
}

// variantLabel names a variant by its settings, or its session.
func variantLabel(r *overfitResponse, id string) string {
	for _, v := range r.Variants {
		if v.ID == id {
			if t := paramsText(v.Params); t != "" {
				return t
			}
		}
	}
	return render.Short(id)
}

func renderVariants(doc *render.Doc, r *overfitResponse, limit int) {
	vs := append([]overfitVariant(nil), r.Variants...)
	sort.Slice(vs, func(a, b int) bool { return vs[a].Rank < vs[b].Rank })
	trades := map[string]int{}
	for _, s := range r.Sessions {
		trades[s.SessionID] = s.Trades
	}
	rows := make([][]string, 0, min(len(vs), limit))
	for _, v := range vs[:min(len(vs), limit)] {
		rows = append(rows, []string{render.Int(v.Rank), orDash(paramsText(v.Params)), v.ID, render.Opt(v.Sharpe, 2),
			render.Signed(v.ReturnPct, 1) + "%", render.Num(v.MaxDrawdownPct, 1) + "%", probOpt(v.PSR), render.Int(trades[v.ID])})
	}
	doc.Table("variants, best first", []string{"rank", "settings", "session_id", "sharpe", "return", "max_dd", "p(sr>0)", "trades"}, rows, nil)
	if len(vs) > limit {
		doc.Line("… %d more variants.", len(vs)-limit)
	}
}

func renderPBO(doc *render.Doc, r *overfitResponse) {
	doc.Line("## overfitting (PBO, combinatorially symmetric cross-validation)")
	p := r.PBO
	if p == nil {
		doc.Line("Not computed: %s.", r.PBOError)
		return
	}
	doc.Line("PBO %s: in %s of %d splits (%d blocks of %d days, every half against the other) the in-sample best ranked below the median out of sample.",
		render.Num(p.PBO, 2), pct0(p.PBO), p.Combinations, p.Blocks, p.BlockDays)
	slope := render.Dash
	if p.Slope != nil {
		slope = render.Num(*p.Slope, 2)
	}
	doc.Line("The in-sample best lost out of sample in %s of splits · its median out-of-sample rank %s (0.5 = the median variant) · out-of-sample vs in-sample slope %s (at or below 0: the in-sample edge does not carry over).",
		pct0(p.ProbOOSLoss), render.Num(p.MedianRank, 2), slope)
	if nearCopies(r) {
		doc.Line("The variants are near copies (they count as %s of %d independent trials): PBO then says only that the choice between them is noise, not whether the strategy works. Read the out-of-sample loss rate, the deflated Sharpe and the walk-forward.",
			render.Num(r.DSR.EffectiveN, 1), len(r.Variants))
	}
	var bins []string
	for _, b := range p.Logits {
		bins = append(bins, fmt.Sprintf("%s..%s:%d", render.Num(b.From, -1), render.Num(b.To, -1), b.Count))
	}
	doc.Line("Logit of the out-of-sample rank (below 0 = below the median): %s", strings.Join(bins, " "))
}

func renderDSR(doc *render.Doc, r *overfitResponse) {
	doc.Line("## deflated Sharpe ratio")
	x := r.DSR
	if x == nil {
		return
	}
	outside := x.TrialsUsed - x.EffectiveN
	count := fmt.Sprintf("%d trials (the %d variants count as %s independent ones", x.Trials, len(r.Variants), render.Num(x.EffectiveN, 1))
	if outside > 0.5 {
		count += fmt.Sprintf(", plus %s tried outside this sweep", render.Num(outside, 0))
	}
	count += ")"
	spread := render.Num(x.SharpeSD, 2)
	if x.SharpeSDUsed > x.SharpeSD+1e-9 {
		spread = fmt.Sprintf("%s (the variants spread only %s; chance alone spreads trials %s over these days)", render.Num(x.SharpeSDUsed, 2), render.Num(x.SharpeSD, 2), render.Num(x.ChanceSD, 2))
	}
	doc.Line("Best variant (%s): Sharpe %s. After %s with Sharpes spread by %s, luck alone reaches %s: DSR %s = P(true Sharpe above that bar) · P(true Sharpe > 0) %s.",
		variantLabel(r, x.BestID), render.Num(x.Sharpe, 2), count, spread, render.Num(x.ExpectedMaxSharpe, 2), probOpt(x.DSR), probOpt(x.PSR))
}

func renderWalkForward(doc *render.Doc, r *overfitResponse) {
	w := r.WalkForward
	if w == nil {
		doc.Line("## walk-forward")
		doc.Line("Not computed: %s.", r.WalkForwardError)
		return
	}
	kind := "rolling"
	if w.Anchored {
		kind = "anchored"
	}
	doc.Line("## walk-forward (%s: %d folds of %d days, each after %d in-sample days)", kind, len(w.Folds), w.OOSDays, w.ISDays)
	rows := make([][]string, 0, len(w.Folds))
	for i, f := range w.Folds {
		rows = append(rows, []string{render.Int(i + 1), fmt.Sprintf("%s → %s", dayOf(r.From, f.ISFrom), dayOf(r.From, f.ISTo)),
			fmt.Sprintf("%s → %s", dayOf(r.From, f.OOSFrom), dayOf(r.From, f.OOSTo)), variantLabel(r, f.PickID),
			render.Signed(f.ISAnnualPct, 1) + "%", render.Signed(f.OOSAnnualPct, 1) + "%", render.Signed(f.OOSReturnPct, 1) + "%"})
	}
	doc.Table("", []string{"fold", "in-sample", "out-of-sample", "pick", "is_annual", "oos_annual", "oos_return"}, rows, nil)
	eff := render.Dash
	if w.Efficiency != nil {
		eff = render.Num(*w.Efficiency*100, 0) + "%"
	}
	doc.Line("Stitched out of sample (%d days): return %s · Sharpe %s · max DD %s · P(Sharpe > 0) %s · efficiency %s · %d of %d folds positive · the pick changed %d× (%d settings).",
		w.Days, render.Signed(w.OOSReturnPct, 1)+"%", render.Opt(w.OOSSharpe, 2), render.Num(w.OOSMaxDrawdownPct, 1)+"%", probOpt(w.OOSPSR),
		eff, w.FoldsPositive, len(w.Folds), w.PickChanges, w.DistinctPicks)
	doc.Line("Over the same days: holding the median variant %s, the best variant in hindsight %s.",
		render.Signed(w.MedianVariantReturnPct, 1)+"%", render.Signed(w.BestInHindsightPct, 1)+"%")
}

func renderSensitivity(doc *render.Doc, r *overfitResponse) {
	doc.Line("## parameter sensitivity")
	s := r.Sensitivity
	if s == nil {
		doc.Line("Not computed: %s.", r.SensitivityError)
		return
	}
	p := s.SPP
	doc.Line("Over all %d settings (System Parameter Permutation): median Sharpe %s (p25 %s, p75 %s) · best %s · worst %s · %s positive · median return %s. The median is the honest estimate; the best is the lucky bound.",
		len(r.Variants), render.Num(p.Median, 2), render.Num(p.P25, 2), render.Num(p.P75, 2), render.Num(p.Best, 2), render.Num(p.Worst, 2),
		pct0(p.SharePositive), render.Signed(p.MedianReturnPct, 1)+"%")
	switch {
	case s.Neighbours == 0:
		doc.Line("The best setting (%s) has no neighbour in the sweep: no way to tell a plateau from a spike. Sweep the values next to it.", variantLabel(r, s.BestID))
	case s.Plateau == nil:
		doc.Line("The best setting (%s) has %d neighbours with mean Sharpe %s.", variantLabel(r, s.BestID), s.Neighbours, render.Opt(s.NeighbourMeanSharpe, 2))
	default:
		shape := "a plateau"
		switch {
		case *s.Plateau < 0.4:
			shape = "a spike: the neighbours have little of it"
		case *s.Plateau < 0.7:
			shape = "a slope: the neighbours keep some of it"
		}
		doc.Line("The best setting (%s) against its %d neighbours (one step in every number): their mean Sharpe %s, plateau %s, %s.",
			variantLabel(r, s.BestID), s.Neighbours, render.Opt(s.NeighbourMeanSharpe, 2), render.Num(*s.Plateau, 2), shape)
	}
	var rows [][]string
	for _, m := range s.Marginals {
		for _, v := range m.Values {
			rows = append(rows, []string{m.Param, v.Value, render.Int(v.Count), render.Num(v.MeanSharpe, 2), render.Num(v.MedianSharpe, 2)})
		}
	}
	doc.Table("each setting's values, over the variants that had it", []string{"setting", "value", "variants", "mean_sharpe", "median_sharpe"}, rows, nil)
	if g := s.Grid; g != nil {
		cols := append([]string{g.Y + " \\ " + g.X}, g.XValues...)
		grid := make([][]string, 0, len(g.YValues))
		for yi, y := range g.YValues {
			row := []string{y}
			for xi := range g.XValues {
				row = append(row, render.Opt(g.Sharpe[yi][xi], 2))
			}
			grid = append(grid, row)
		}
		doc.Table("Sharpe by "+g.X+" and "+g.Y, cols, grid, nil)
	}
}

// dayOf is the date `offset` days after from.
func dayOf(from string, offset int) string {
	t, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return fmt.Sprintf("day %d", offset)
	}
	return t.AddDate(0, 0, offset).Format(time.DateOnly)
}

func pct0(v float64) string { return render.Num(v*100, 0) + "%" }

// sweepGrades are the sweep's parts graded, for the report's verdict and
// validate_strategy's overfitting line.
func sweepGrades(r *overfitResponse) []scoreLine {
	var out []scoreLine
	if p := r.PBO; p != nil {
		l := scoreLine{area: "PBO", figures: fmt.Sprintf("%s of splits below the median out of sample, %s lose", pct0(p.PBO), pct0(p.ProbOOSLoss))}
		switch {
		case nearCopies(r):
			l.grade, l.why = gradeNone, fmt.Sprintf("the variants are near copies (%s effective of %d): the choice between them is noise, which says nothing of the edge", render.Num(r.DSR.EffectiveN, 1), len(r.Variants))
		case p.PBO <= 0.2:
			l.grade, l.why = gradeGreen, "PBO ≤ 0.2"
		case p.PBO <= 0.5:
			l.grade, l.why = gradeAmber, "PBO 0.2–0.5: the in-sample best often fails out of sample"
		case p.ProbOOSLoss < 0.05:
			l.grade, l.why = gradeAmber, "PBO > 0.5 but the in-sample best rarely loses out of sample: the edge is common to the variants, the choice among them is noise"
		default:
			l.grade, l.why = gradeRed, "PBO > 0.5: choosing the best of this sweep is worse than a coin flip"
		}
		out = append(out, l)
	} else {
		out = append(out, scoreLine{area: "PBO", grade: gradeNone, figures: render.Dash, why: r.PBOError})
	}
	if x := r.DSR; x != nil && x.DSR != nil {
		l := scoreLine{area: "deflated Sharpe", figures: fmt.Sprintf("DSR %s: Sharpe %s vs %s by chance from %d trials", render.Num(*x.DSR, 3), render.Num(x.Sharpe, 2), render.Num(x.ExpectedMaxSharpe, 2), x.Trials)}
		switch {
		case *x.DSR >= 0.95:
			l.grade, l.why = gradeGreen, "DSR ≥ 0.95 (Bailey & López de Prado)"
		case *x.DSR >= 0.90:
			l.grade, l.why = gradeAmber, "DSR 0.90–0.95"
		default:
			l.grade, l.why = gradeRed, "DSR < 0.90: the best variant's Sharpe is within what the search finds by luck"
		}
		out = append(out, l)
	}
	if w := r.WalkForward; w != nil {
		l := scoreLine{area: "walk-forward", figures: fmt.Sprintf("%d of %d folds positive, out-of-sample Sharpe %s", w.FoldsPositive, len(w.Folds), render.Opt(w.OOSSharpe, 2))}
		switch {
		case w.Efficiency == nil || *w.Efficiency <= 0 || w.OOSReturnPct <= 0:
			l.grade, l.why = gradeRed, "the picks lose out of sample"
		case *w.Efficiency >= 0.5:
			l.grade, l.why = gradeGreen, fmt.Sprintf("efficiency %s ≥ 50%% (Pardo)", pct0(*w.Efficiency))
		default:
			l.grade, l.why = gradeAmber, fmt.Sprintf("efficiency %s: under half the in-sample pace carries over", pct0(*w.Efficiency))
		}
		out = append(out, l)
	}
	if s := r.Sensitivity; s != nil {
		l := scoreLine{area: "sensitivity", figures: fmt.Sprintf("median Sharpe %s over %d settings, best %s", render.Num(s.SPP.Median, 2), len(r.Variants), render.Num(s.SPP.Best, 2))}
		switch {
		case s.SPP.Median <= 0:
			l.grade, l.why = gradeRed, "the median setting loses: the edge is in a few settings"
		case s.Plateau != nil && *s.Plateau < 0.4:
			l.grade, l.why = gradeRed, fmt.Sprintf("plateau %s: the best setting is a spike", render.Num(*s.Plateau, 2))
		case s.Plateau != nil && *s.Plateau < 0.7:
			l.grade, l.why = gradeAmber, fmt.Sprintf("plateau %s: the neighbours keep part of it", render.Num(*s.Plateau, 2))
		case s.Plateau == nil:
			l.grade, l.why = gradeAmber, "no neighbours of the best setting in the sweep"
		default:
			l.grade, l.why = gradeGreen, fmt.Sprintf("plateau %s ≥ 0.7, median setting positive", render.Num(*s.Plateau, 2))
		}
		out = append(out, l)
	}
	return out
}

// nearCopies is a sweep whose variants count as under 1.5 independent trials.
func nearCopies(r *overfitResponse) bool {
	return r.DSR != nil && r.DSR.EffectiveN < 1.5 && len(r.Variants) >= 3
}

// worstGrade is the lowest grade measured.
func worstGrade(lines []scoreLine) string {
	rank := map[string]int{gradeRed: 0, gradeAmber: 1, gradeGreen: 2}
	worst := ""
	for _, l := range lines {
		if r, ok := rank[l.grade]; ok && (worst == "" || r < rank[worst]) {
			worst = l.grade
		}
	}
	if worst == "" {
		return gradeNone
	}
	return worst
}

func renderGrades(doc *render.Doc, lines []scoreLine) {
	rows := make([][]string, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, []string{l.area, l.grade, l.figures, l.why})
	}
	doc.Table("verdict", []string{"part", "grade", "figures", "why"}, rows, nil)
}

func renderSweepNotes(doc *render.Doc, r *overfitResponse, sw *sweep) {
	for _, n := range sw.notes {
		doc.Line("Note: %s", n)
	}
	for _, n := range r.Notes {
		doc.Line("Note: %s", n)
	}
}

func renderOverfitReport(doc *render.Doc, r *overfitResponse, sw *sweep) {
	renderSweepHeader(doc, r, sw)
	renderVariants(doc, r, 30)
	doc.Blank()
	renderPBO(doc, r)
	doc.Blank()
	renderDSR(doc, r)
	doc.Blank()
	renderWalkForward(doc, r)
	doc.Blank()
	renderSensitivity(doc, r)
	doc.Blank()
	renderGrades(doc, sweepGrades(r))
	renderSweepNotes(doc, r, sw)
	doc.Line("Choose by the median and the plateau, not the best; then run the chosen setting alone on the test window. validate_strategy on its session checks the rest.")
}

func renderWalkForwardOnly(doc *render.Doc, r *overfitResponse, sw *sweep) {
	renderSweepHeader(doc, r, sw)
	renderWalkForward(doc, r)
	renderGrades(doc, sweepGrades(r))
	renderSweepNotes(doc, r, sw)
	doc.Line("The full analysis (PBO, deflated Sharpe, sensitivity, variants): get_overfitting_report with the same arguments.")
}

func renderSensitivityOnly(doc *render.Doc, r *overfitResponse, sw *sweep) {
	renderSweepHeader(doc, r, sw)
	renderSensitivity(doc, r)
	doc.Blank()
	renderVariants(doc, r, 30)
	renderGrades(doc, sweepGrades(r))
	renderSweepNotes(doc, r, sw)
	doc.Line("The full analysis (PBO, deflated Sharpe, walk-forward): get_overfitting_report with the same arguments.")
}

// sweepLine is validate_strategy's overfitting line from a sweep.
func sweepLine(r *overfitResponse) scoreLine {
	grades := sweepGrades(r)
	var figs []string
	if r.PBO != nil {
		figs = append(figs, "PBO "+render.Num(r.PBO.PBO, 2))
	}
	if r.DSR != nil && r.DSR.DSR != nil {
		figs = append(figs, fmt.Sprintf("DSR %s (%d trials)", render.Num(*r.DSR.DSR, 2), r.DSR.Trials))
	}
	if w := r.WalkForward; w != nil && w.Efficiency != nil {
		figs = append(figs, "WFE "+pct0(*w.Efficiency))
	}
	if s := r.Sensitivity; s != nil {
		if s.Plateau != nil {
			figs = append(figs, "plateau "+render.Num(*s.Plateau, 2))
		}
		figs = append(figs, "median setting Sharpe "+render.Num(s.SPP.Median, 2))
	}
	why := "all parts ok"
	var weak []string
	for _, g := range grades {
		if g.grade == gradeRed || g.grade == gradeAmber {
			weak = append(weak, g.area+": "+g.why)
		}
	}
	if len(weak) > 0 {
		why = strings.Join(weak, "; ")
	}
	if len(figs) == 0 {
		figs = []string{render.Dash}
	}
	return scoreLine{area: "overfitting", grade: worstGrade(grades), figures: strings.Join(figs, " · "), why: why}
}
