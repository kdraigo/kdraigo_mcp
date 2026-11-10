package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// analytics_service /analytics/significance and /analytics/montecarlo.

type marketFit struct {
	Beta             *float64 `json:"beta"`
	Correlation      *float64 `json:"correlation"`
	AlphaPct         *float64 `json:"alpha_pct"`
	InformationRatio *float64 `json:"information_ratio"`
	BenchmarkSharpe  *float64 `json:"benchmark_sharpe"`
}

type significanceResponse struct {
	SessionID        string          `json:"session_id"`
	Source           string          `json:"source"`
	From             string          `json:"from"`
	Days             int             `json:"days"`
	Years            float64         `json:"years"`
	MeanDailyPct     *float64        `json:"mean_daily_pct"`
	VolDailyPct      *float64        `json:"vol_daily_pct"`
	Skew             *float64        `json:"skew"`
	Kurtosis         *float64        `json:"kurtosis"`
	Autocorr1        *float64        `json:"autocorr_lag1"`
	Sharpe           *float64        `json:"sharpe"`
	SharpeLo         *float64        `json:"sharpe_autocorr_adjusted"`
	SharpeLoLags     int             `json:"sharpe_autocorr_lags"`
	SharpeP5         *float64        `json:"sharpe_p5"`
	SharpeP95        *float64        `json:"sharpe_p95"`
	BlockDays        float64         `json:"block_days"`
	PSRZero          *float64        `json:"psr_zero"`
	TargetSharpe     *float64        `json:"target_sharpe"`
	PSRTarget        *float64        `json:"psr_target"`
	MinTRLDaysZero   *float64        `json:"min_trl_days_zero"`
	MinTRLDaysTarget *float64        `json:"min_trl_days_target"`
	CVaR95DailyPct   *float64        `json:"cvar95_daily_pct"`
	WorstDayPct      *float64        `json:"worst_day_pct"`
	UlcerIndex       *float64        `json:"ulcer_index"`
	Market           *marketFit      `json:"market"`
	Trials           int             `json:"trials"`
	ChanceMaxSharpe  *float64        `json:"chance_max_sharpe"`
	MinBacktestYears *float64        `json:"min_backtest_years"`
	Notes            []string        `json:"notes"`
	Warnings         []reportWarning `json:"warnings"`
}

type mcDist struct {
	P5   float64 `json:"p5"`
	P25  float64 `json:"p25"`
	P50  float64 `json:"p50"`
	P75  float64 `json:"p75"`
	P95  float64 `json:"p95"`
	Mean float64 `json:"mean"`
}

type monteCarloResponse struct {
	SessionID      string   `json:"session_id"`
	Method         string   `json:"method"`
	Basis          string   `json:"basis"`
	Sample         int      `json:"sample"`
	HorizonDays    *float64 `json:"horizon_days"`
	RiskPct        *float64 `json:"risk_pct"`
	SessionRiskPct *float64 `json:"session_risk_pct"`
	Concurrency    *float64 `json:"concurrency"`
	Actual         *struct {
		ReturnPct          *float64 `json:"return_pct"`
		MaxDrawdownPct     *float64 `json:"max_drawdown_pct"`
		DrawdownPercentile *float64 `json:"drawdown_percentile"`
	} `json:"actual"`
	Result struct {
		Method         string   `json:"method"`
		Paths          int      `json:"paths"`
		Length         int      `json:"length"`
		BlockDays      *float64 `json:"block_days"`
		FinalReturnPct mcDist   `json:"final_return_pct"`
		MaxDrawdownPct mcDist   `json:"max_drawdown_pct"`
		LongestRun     mcDist   `json:"longest_run"`
		Sharpe         *mcDist  `json:"sharpe"`
		Drawdown       []struct {
			AtLeastPct float64 `json:"at_least_pct"`
			P          float64 `json:"p"`
		} `json:"drawdown_probability"`
		ProbLoss float64 `json:"prob_loss"`
		ProbRuin float64 `json:"prob_ruin"`
		RuinPct  float64 `json:"ruin_pct"`
	} `json:"result"`
	Notes    []string        `json:"notes"`
	Warnings []reportWarning `json:"warnings"`
}

// RegisterValidation adds the tools that say how far a result can be
// trusted: get_monte_carlo, get_significance and validate_strategy.
func RegisterValidation(s *server.MCPServer, d Deps) {
	addGetMonteCarlo(s, d)
	addGetSignificance(s, d)
	addValidateStrategy(s, d)
}

func sessionArgs(req mcp.CallToolRequest) (url.Values, error) {
	id, err := req.RequireString("session_id")
	if err != nil {
		return nil, err
	}
	kind := req.GetString("session_type", "backtest")
	if kind != "backtest" && kind != "live" {
		return nil, fmt.Errorf("session_type must be backtest or live")
	}
	return url.Values{"session_id": {id}, "session_type": {kind}}, nil
}

// setNum copies a number argument into the query when it was given.
func setNum(q url.Values, req mcp.CallToolRequest, name string) {
	if v, ok := req.GetArguments()[name]; ok && v != nil {
		if f := req.GetFloat(name, math.NaN()); !math.IsNaN(f) {
			q.Set(name, strconv.FormatFloat(f, 'f', -1, 64))
		}
	}
}

func addGetMonteCarlo(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_monte_carlo",
		mcp.WithDescription("Monte Carlo of a session: its closed trades (or its days) drawn again thousands of times, for the range of drawdown, return and losing runs the same edge could have produced. "+
			"One backtest is one path; this shows the others. Answers: how deep can the drawdown get (p95, not just the one the backtest happened to have), how likely is a 20% drawdown, what if each trade risked 2% instead, how likely is it to end down or fall by half. "+
			"method=auto draws days in blocks when the session has 90 days or more (blocks keep losing stretches together; trades drawn independently break them up and understate drawdowns), and trades otherwise. "+
			"risk_pct re-sizes every trade to that % of equity risked to its initial stop (needs R: a stop on most trades). horizon_days runs each path for that long at the session's trade rate. "+
			"Seeded: the same question gives the same numbers."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live")),
		mcp.WithString("method", mcp.Description("auto (default), trades or days"), mcp.Enum("auto", "trades", "days")),
		mcp.WithString("resample", mcp.Description("trades only: bootstrap (default: draw with replacement) or reshuffle (every trade once, in a new order: same end, different road)"), mcp.Enum("bootstrap", "reshuffle")),
		mcp.WithNumber("horizon_days", mcp.Description("Days each path runs; default the session's own length")),
		mcp.WithNumber("risk_pct", mcp.Description("What-if: risk this % of equity on every trade (to its initial stop), e.g. 1.75")),
		mcp.WithNumber("dd_pct", mcp.Description("One more drawdown level to give the probability of, e.g. 17 for a bot's stop rule")),
		mcp.WithNumber("ruin_pct", mcp.Description("The fall from the start that counts as ruin, in %; default 50")),
		mcp.WithNumber("paths", mcp.Description("Paths to draw, 500–20000; default 5000")),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := sessionArgs(req)
		if err != nil {
			return toolErr(err), nil
		}
		for _, k := range []string{"method", "resample"} {
			if v := strings.TrimSpace(req.GetString(k, "")); v != "" {
				q.Set(k, v)
			}
		}
		for _, k := range []string{"horizon_days", "risk_pct", "dd_pct", "ruin_pct", "paths"} {
			setNum(q, req, k)
		}
		var mc monteCarloResponse
		if err := d.getValidation(ctx, "get_monte_carlo", "/api/v1/dev/analytics/montecarlo", q, &mc); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		renderMonteCarlo(doc, &mc)
		doc.Line("Next: get_significance session_id=%s (is the Sharpe more than luck?) · validate_strategy for the whole check.", mc.SessionID)
		return toolText(doc.String()), nil
	})
}

func addGetSignificance(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_significance",
		mcp.WithDescription("Is a session's result more than luck? From its daily returns (mark-to-market where the engine recorded the days): "+
			"the Probabilistic Sharpe Ratio (the probability the true Sharpe is above zero, or above target_sharpe), the track record in days each needs to reach 95% (MinTRL), "+
			"the Sharpe corrected for autocorrelation (trend-following equity inflates the usual √365 figure) and a bootstrap 5–95% range, skew, kurtosis, CVaR and Ulcer index, "+
			"and beta, correlation, alpha and information ratio against buy and hold of the same instruments (a strategy that is mostly the market shows here). "+
			"With trials (how many variants, settings or ideas were tried to find this one): the Sharpe the best of that many skill-less variants reaches by chance over this window, and the window needed before chance stays below this one. "+
			"Works on live sessions too: how many more days before a live bot's record says anything."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live")),
		mcp.WithNumber("trials", mcp.Description("Variants tried to find this one (sweep variants plus earlier attempts); be honest, more is safer")),
		mcp.WithNumber("target_sharpe", mcp.Description("An annual Sharpe to test against beside zero, e.g. 1")),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := sessionArgs(req)
		if err != nil {
			return toolErr(err), nil
		}
		setNum(q, req, "trials")
		setNum(q, req, "target_sharpe")
		var sig significanceResponse
		if err := d.getValidation(ctx, "get_significance", "/api/v1/dev/analytics/significance", q, &sig); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		renderSignificance(doc, &sig)
		doc.Line("Next: get_monte_carlo session_id=%s (the drawdown range) · validate_strategy for the whole check.", sig.SessionID)
		return toolText(doc.String()), nil
	})
}

// getValidation is getResearch for the validation endpoints, which an older
// analytics_service does not have.
func (d Deps) getValidation(ctx context.Context, name, path string, q url.Values, out any) error {
	err := d.getResearch(ctx, name, path, q, out)
	if err != nil && strings.Contains(err.Error(), "has no ") {
		return fmt.Errorf("%s: this platform's analytics_service does not have %s yet; get_session_report still works", name, strings.TrimPrefix(path, "/api/v1/dev"))
	}
	return err
}

func pctOpt(v *float64, dp int) string {
	if v == nil {
		return render.Dash
	}
	return render.Num(*v, dp) + "%"
}

func prob(p float64) string {
	switch {
	case p == 0:
		return "0%"
	case p < 0.001:
		return "<0.1%"
	}
	return render.Num(p*100, 1) + "%"
}

func renderMonteCarlo(doc *render.Doc, mc *monteCarloResponse) {
	r := &mc.Result
	what := "closed trades"
	switch mc.Basis {
	case "net_fraction":
		what = "closed trades, each its net over the equity it opened against"
	case "r_at_risk":
		what = fmt.Sprintf("closed trades re-sized to %s risked to the initial stop (R × risk)", pctOpt(mc.RiskPct, 2))
	case "mark_to_market":
		what = "days, mark-to-market (open positions valued at each close)"
	case "closed_trades":
		what = "days of closed-trade equity"
	}
	how := map[string]string{"bootstrap": "drawn with replacement", "reshuffle": "reshuffled (each once)", "block_bootstrap": "drawn in blocks"}[r.Method]
	if r.BlockDays != nil {
		how += fmt.Sprintf(" (mean block %s days)", render.Num(*r.BlockDays, 0))
	}
	unit := "trades"
	if mc.Method == "days" {
		unit = "days"
	}
	doc.Line("Monte Carlo of session %s: %d paths × %d %s, from %d %s, %s.", mc.SessionID, r.Paths, r.Length, unit, mc.Sample, what, how)
	var ctxBits []string
	if mc.SessionRiskPct != nil {
		ctxBits = append(ctxBits, "the session risked a median "+pctOpt(mc.SessionRiskPct, 2)+" per trade")
	}
	if mc.Concurrency != nil && *mc.Concurrency > 0 {
		ctxBits = append(ctxBits, render.Num(*mc.Concurrency, 2)+" positions open at once on average")
	}
	if mc.HorizonDays != nil {
		ctxBits = append(ctxBits, "horizon "+render.Num(*mc.HorizonDays, 0)+" days")
	}
	if len(ctxBits) > 0 {
		doc.Line("%s.", capitalize(strings.Join(ctxBits, " · ")))
	}
	run := "longest losing streak (trades)"
	if mc.Method == "days" {
		run = "longest under a previous peak (days)"
	}
	rows := [][]string{
		distRow("final return %", r.FinalReturnPct, 1),
		distRow("max drawdown %", r.MaxDrawdownPct, 1),
		distRow(run, r.LongestRun, 0),
	}
	if r.Sharpe != nil {
		rows = append(rows, distRow("Sharpe (annual)", *r.Sharpe, 2))
	}
	doc.Table("spread over the paths", []string{"measure", "p5", "p25", "p50", "p75", "p95", "mean"}, rows, nil)
	var levels []string
	for _, l := range r.Drawdown {
		levels = append(levels, fmt.Sprintf("≥%s%% %s", render.Num(l.AtLeastPct, -1), prob(l.P)))
	}
	doc.Line("Chance of a drawdown: %s.", strings.Join(levels, " · "))
	doc.Line("Chance of ending down %s · of falling %s%% below the start at some point %s.", prob(r.ProbLoss), render.Num(r.RuinPct, -1), prob(r.ProbRuin))
	if a := mc.Actual; a != nil && a.MaxDrawdownPct != nil && a.DrawdownPercentile != nil {
		luck := "about typical"
		switch p := *a.DrawdownPercentile; {
		case p < 0.25:
			luck = "milder than most paths: the backtest was lucky in its order"
		case p > 0.75:
			luck = "deeper than most paths"
		}
		doc.Line("The session's own max drawdown %s sits at the %s percentile of the paths: %s. Plan for p95 (%s), not for it.",
			pctOpt(a.MaxDrawdownPct, 1), ordinal(int(math.Round(*a.DrawdownPercentile*100))), luck, render.Num(r.MaxDrawdownPct.P95, 1)+"%")
	}
	for _, n := range mc.Notes {
		doc.Line("Note: %s", n)
	}
	highWarnings(doc, mc.Warnings)
}

// ordinal is 1st, 2nd, 3rd, 4th … 11th, 12th, 13th, 21st.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

func distRow(name string, d mcDist, dp int) []string {
	return []string{name, render.Num(d.P5, dp), render.Num(d.P25, dp), render.Num(d.P50, dp), render.Num(d.P75, dp), render.Num(d.P95, dp), render.Num(d.Mean, dp)}
}

func highWarnings(doc *render.Doc, ws []reportWarning) {
	for _, w := range ws {
		if w.Severity == "high" {
			doc.Line("[high] %s", w.Message)
		}
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// significanceLines are the significance figures in two or three lines, for
// get_significance and the session report.
func significanceLines(sig *significanceResponse) []string {
	src := "mark-to-market days"
	if sig.Source == "closed_trades" {
		src = "closed-trade days"
	}
	if sig.Sharpe == nil {
		return []string{fmt.Sprintf("Significance: %d %s, too few or too flat to test a Sharpe ratio.", sig.Days, src)}
	}
	trl := trlText(sig.MinTRLDaysZero) + fmt.Sprintf(" (have %d)", sig.Days)
	lines := []string{fmt.Sprintf("Significance over %d %s: Sharpe %s (autocorrelation-adjusted %s, bootstrap 5–95%% %s to %s) · P(true Sharpe > 0) %s · track record needed for 95%% %s",
		sig.Days, src, render.Opt(sig.Sharpe, 2), render.Opt(sig.SharpeLo, 2), render.Opt(sig.SharpeP5, 2), render.Opt(sig.SharpeP95, 2),
		probOpt(sig.PSRZero), trl)}
	tails := fmt.Sprintf("Tails: skew %s · kurtosis %s (normal 3) · worst day %s · CVaR95 %s a day · Ulcer index %s",
		render.Opt(sig.Skew, 2), render.Opt(sig.Kurtosis, 1), pctOpt(sig.WorstDayPct, 2), pctOpt(sig.CVaR95DailyPct, 2), render.Opt(sig.UlcerIndex, 1))
	if sig.TargetSharpe != nil {
		need := trlText(sig.MinTRLDaysTarget) + " for 95%"
		tails += fmt.Sprintf(" · P(true Sharpe > %s) %s, %s", render.Num(*sig.TargetSharpe, -1), probOpt(sig.PSRTarget), need)
	}
	lines = append(lines, tails)
	if m := sig.Market; m != nil && m.Beta != nil {
		lines = append(lines, fmt.Sprintf("Against buy and hold of the same instruments: beta %s · correlation %s · alpha %s a year · information ratio %s · buy-and-hold Sharpe %s",
			render.Opt(m.Beta, 2), render.Opt(m.Correlation, 2), pctOpt(m.AlphaPct, 1), render.Opt(m.InformationRatio, 2), render.Opt(m.BenchmarkSharpe, 2)))
	}
	if sig.Trials >= 2 && sig.ChanceMaxSharpe != nil {
		line := fmt.Sprintf("Chance: the best of %d skill-less variants reaches a Sharpe of %s over %s years by luck alone", sig.Trials, render.Num(*sig.ChanceMaxSharpe, 2), render.Num(sig.Years, 1))
		if sig.MinBacktestYears != nil {
			line += fmt.Sprintf("; for %d trials this Sharpe needs %s years of data before luck stays below it", sig.Trials, render.Num(*sig.MinBacktestYears, 1))
		}
		lines = append(lines, line)
	}
	return lines
}

// trlText is a track record needed: days, or how far off it is.
func trlText(days *float64) string {
	switch {
	case days == nil:
		return "never at this Sharpe"
	case *days > 36500:
		return "over 100 years"
	}
	return render.Num(*days, 0) + " days"
}

func probOpt(p *float64) string {
	if p == nil {
		return render.Dash
	}
	return render.Num(*p, 3)
}

func renderSignificance(doc *render.Doc, sig *significanceResponse) {
	doc.Line("Session %s · daily returns from %s · %d days (%s years)", sig.SessionID, sig.From, sig.Days, render.Num(sig.Years, 2))
	for _, l := range significanceLines(sig) {
		doc.Line("%s", l)
	}
	if sig.Sharpe != nil {
		doc.Line("Read it as: P(true Sharpe > 0) under 0.95 is not yet an edge; a track record needed above the days you have means wait or test longer; a Sharpe near the chance figure is what the search alone would find.")
	}
	for _, n := range sig.Notes {
		doc.Line("Note: %s", n)
	}
	highWarnings(doc, sig.Warnings)
}

// Scorecard grades. A grade is a convention from the literature and practice,
// named on its line, never a verdict on its own.
const (
	gradeGreen = "ok"
	gradeAmber = "caution"
	gradeRed   = "weak"
	gradeNone  = "not measured"
)

type scoreLine struct{ area, grade, figures, why string }

func addValidateStrategy(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("validate_strategy",
		mcp.WithDescription("Is this backtest result real? One call runs the checks a professional would and gives a scorecard, each line with its figures, a grade (ok / caution / weak) and why: "+
			"sample size, statistical significance (Probabilistic Sharpe Ratio, track record needed, the Sharpe the variants tried reach by chance), Monte Carlo drawdown and the chance of ending down, "+
			"how much rests on a few trades or one instrument, whether it is mostly the market (beta), and costs. "+
			"Overfitting across settings (PBO, walk-forward, parameter sensitivity) needs a sweep: with workspace_id it proposes the variants to run with workspace_run. "+
			"Run it before calling a result an edge, before experiment_decide, and before a strategy goes live. It spends no runs."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live")),
		mcp.WithNumber("trials", mcp.Description("Variants, settings or ideas tried to find this one (sweep variants plus earlier attempts); be honest, more is safer")),
		mcp.WithString("workspace_id", mcp.Description("The strategy's cloud workspace: proposes a sensitivity sweep of its declared settings")),
		mcp.WithString("sweep_params", mcp.Description("Comma-separated settings to vary in the proposed sweep; default the first three numeric ones")),
		mcp.WithNumber("max_variants", mcp.Description("Most variants to propose (your account's sweep cap: 10 verified, 20 pro); default 10")),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := sessionArgs(req)
		if err != nil {
			return toolErr(err), nil
		}
		trials := int(req.GetFloat("trials", 0))
		var (
			wg               sync.WaitGroup
			perf             performanceReport
			sig              significanceResponse
			mc               monteCarloResponse
			params           workspaceParams
			perfErr, sigErr  error
			mcErr, paramsErr error
			wsID             = strings.TrimSpace(req.GetString("workspace_id", ""))
		)
		wg.Go(func() {
			perfErr = d.getJSON(ctx, "validate_strategy", client.Analytics, "/api/v1/dev/analytics/performance", q, &perf)
		})
		wg.Go(func() {
			sq := cloneValues(q)
			if trials > 0 {
				sq.Set("trials", strconv.Itoa(trials))
			}
			sigErr = d.getValidation(ctx, "significance", "/api/v1/dev/analytics/significance", sq, &sig)
		})
		wg.Go(func() {
			mcErr = d.getValidation(ctx, "Monte Carlo", "/api/v1/dev/analytics/montecarlo", q, &mc)
		})
		if wsID != "" {
			wg.Go(func() {
				paramsErr = d.getJSON(ctx, "workspace params", client.Workspace, wsPath(wsID)+"/params", nil, &params)
			})
		}
		wg.Wait()
		if perfErr != nil {
			return toolErr(perfErr), nil
		}
		doc := render.New()
		lines := scorecard(&perf, &sig, sigErr, &mc, mcErr, trials)
		doc.Line("Validation of session %s (%s). Grades are conventions, named on each line; read the figures, not just the grade.", q.Get("session_id"), q.Get("session_type"))
		highWarnings(doc, perf.Warnings)
		rows := make([][]string, 0, len(lines))
		weak, caution := 0, 0
		for _, l := range lines {
			rows = append(rows, []string{l.area, l.grade, l.figures, l.why})
			switch l.grade {
			case gradeRed:
				weak++
			case gradeAmber:
				caution++
			}
		}
		doc.Table("scorecard", []string{"area", "grade", "figures", "why"}, rows, nil)
		switch {
		case weak > 0:
			doc.Line("Verdict: not an edge yet: %d weak, %d caution. Fix or explain the weak lines before trusting this result.", weak, caution)
		case caution > 0:
			doc.Line("Verdict: promising, not proven: %d caution. Overfitting across settings is still unmeasured until a sweep is checked.", caution)
		default:
			doc.Line("Verdict: passes the single-session checks. Overfitting across settings is still unmeasured until a sweep is checked; a forward period after the model's training data is the final test.")
		}
		if wsID != "" {
			doc.Blank()
			if paramsErr != nil {
				doc.Line("Sensitivity sweep: could not read the workspace's settings (%v).", paramsErr)
			} else {
				proposeSweep(doc, params.Params, req.GetString("sweep_params", ""), int(req.GetFloat("max_variants", 10)))
			}
		} else {
			doc.Line("Next: give workspace_id to get a sensitivity sweep of the strategy's settings · get_monte_carlo risk_pct=… for another size per trade · get_trades sort=net for the trades that carry it.")
		}
		return toolText(doc.String()), nil
	})
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// scorecard grades what one session can say about itself.
func scorecard(p *performanceReport, sig *significanceResponse, sigErr error, mc *monteCarloResponse, mcErr error, trials int) []scoreLine {
	t := &p.Total
	var out []scoreLine

	// Sample.
	sample := scoreLine{area: "sample", figures: fmt.Sprintf("%d closed trades over %d days", t.Trades, p.Ratios.Days)}
	switch {
	case t.Trades >= 100:
		sample.grade, sample.why = gradeGreen, "100+ trades"
	case t.Trades >= minTrades:
		sample.grade, sample.why = gradeAmber, "30–99 trades: rates and averages are still wide"
	default:
		sample.grade, sample.why = gradeRed, "under 30 trades: win rate and PF are noise"
	}
	out = append(out, sample)

	// Significance.
	stat := scoreLine{area: "significance"}
	switch {
	case sigErr != nil:
		stat.grade, stat.figures, stat.why = gradeNone, render.Dash, sigErr.Error()
	case sig.Sharpe == nil || sig.PSRZero == nil:
		stat.grade, stat.figures, stat.why = gradeNone, fmt.Sprintf("%d days", sig.Days), "too few or too flat days to test"
	default:
		psr := *sig.PSRZero
		stat.figures = fmt.Sprintf("Sharpe %s (adj %s) · P(>0) %s", render.Opt(sig.Sharpe, 2), render.Opt(sig.SharpeLo, 2), render.Num(psr, 3))
		stat.figures += fmt.Sprintf(" · needs %s, has %d days", trlText(sig.MinTRLDaysZero), sig.Days)
		switch {
		case psr >= 0.95:
			stat.grade, stat.why = gradeGreen, "PSR ≥ 0.95 (Bailey & López de Prado)"
		case psr >= 0.90:
			stat.grade, stat.why = gradeAmber, "PSR 0.90–0.95: not yet distinguishable from zero at 95%"
		default:
			stat.grade, stat.why = gradeRed, "PSR < 0.90: the Sharpe could well be zero"
		}
	}
	out = append(out, stat)

	// Chance from the search.
	chance := scoreLine{area: "search luck"}
	switch {
	case trials < 2:
		chance.grade, chance.figures, chance.why = gradeNone, render.Dash, "say how many variants were tried (trials) to compare with chance"
	case sigErr != nil || sig.ChanceMaxSharpe == nil || sig.Sharpe == nil:
		chance.grade, chance.figures, chance.why = gradeNone, fmt.Sprintf("%d trials", trials), "no Sharpe to compare"
	default:
		ratio := *sig.Sharpe / *sig.ChanceMaxSharpe
		chance.figures = fmt.Sprintf("Sharpe %s vs %s by chance from %d trials", render.Num(*sig.Sharpe, 2), render.Num(*sig.ChanceMaxSharpe, 2), trials)
		switch {
		case ratio >= 1.5:
			chance.grade, chance.why = gradeGreen, "well above what the search alone finds"
		case ratio >= 1:
			chance.grade, chance.why = gradeAmber, "above, not far above, the best of skill-less variants (False Strategy Theorem)"
		default:
			chance.grade, chance.why = gradeRed, "within what the best of that many skill-less variants shows"
		}
	}
	out = append(out, chance)

	// Monte Carlo.
	risk := scoreLine{area: "Monte Carlo"}
	if mcErr != nil {
		risk.grade, risk.figures, risk.why = gradeNone, render.Dash, mcErr.Error()
	} else {
		r := &mc.Result
		risk.figures = fmt.Sprintf("DD p50 %s / p95 %s · ends down %s", render.Num(r.MaxDrawdownPct.P50, 1)+"%", render.Num(r.MaxDrawdownPct.P95, 1)+"%", prob(r.ProbLoss))
		if a := mc.Actual; a != nil && a.MaxDrawdownPct != nil {
			risk.figures += " · backtest DD " + pctOpt(a.MaxDrawdownPct, 1)
		}
		switch {
		case r.ProbLoss <= 0.05:
			risk.grade, risk.why = gradeGreen, "under 5% of paths end down"
		case r.ProbLoss <= 0.20:
			risk.grade, risk.why = gradeAmber, "5–20% of paths end down"
		default:
			risk.grade, risk.why = gradeRed, "over 20% of paths end down"
		}
		if r.ProbRuin > 0.01 {
			risk.grade, risk.why = gradeRed, fmt.Sprintf("%s of paths fall %s%% below the start", prob(r.ProbRuin), render.Num(r.RuinPct, -1))
		}
	}
	out = append(out, risk)

	// Concentration.
	conc := scoreLine{area: "concentration"}
	var bits []string
	grade, why := gradeGreen, "spread over many trades and instruments"
	if t.NetWithoutTop5 != nil {
		bits = append(bits, "net without top 5 "+render.Signed(*t.NetWithoutTop5, 2))
		if *t.NetWithoutTop5 < 0 {
			grade, why = gradeRed, "the five best trades carry the whole result"
		} else if t.Top5Share != nil && *t.Top5Share > 0.5 {
			grade, why = gradeAmber, "the five best trades carry over half"
		}
	}
	if n := len(p.Instruments); n >= 3 {
		share := float64(p.InstrumentsPositive) / float64(n)
		bits = append(bits, fmt.Sprintf("%d of %d instruments positive", p.InstrumentsPositive, n))
		if share < 0.4 {
			grade, why = gradeRed, "most instruments lose: a result of a few coins"
		} else if share < 0.6 && grade == gradeGreen {
			grade, why = gradeAmber, "about half the instruments lose"
		}
	}
	if t.Months > 0 {
		bits = append(bits, fmt.Sprintf("%d of %d months losing", t.LosingMonths, t.Months))
	}
	conc.grade, conc.why, conc.figures = grade, why, strings.Join(bits, " · ")
	if conc.figures == "" {
		conc.grade, conc.figures, conc.why = gradeNone, render.Dash, "needs report version 2"
	}
	out = append(out, conc)

	// Market.
	mkt := scoreLine{area: "market"}
	if sigErr == nil && sig.Market != nil && sig.Market.Beta != nil && sig.Market.Correlation != nil {
		m := sig.Market
		mkt.figures = fmt.Sprintf("beta %s · corr %s · alpha %s/yr", render.Num(*m.Beta, 2), render.Num(*m.Correlation, 2), pctOpt(m.AlphaPct, 1))
		switch {
		case *m.Beta >= 0.7 && *m.Correlation >= 0.7:
			mkt.grade, mkt.why = gradeRed, "mostly the market: holding the coins does much the same"
		case math.Abs(*m.Beta) >= 0.4 || math.Abs(*m.Correlation) >= 0.5:
			mkt.grade, mkt.why = gradeAmber, "a large share is market exposure"
		default:
			mkt.grade, mkt.why = gradeGreen, "low market exposure"
		}
		if m.AlphaPct != nil && *m.AlphaPct <= 0 {
			mkt.grade, mkt.why = gradeRed, "no return left after the market's share"
		}
	} else {
		mkt.grade, mkt.figures, mkt.why = gradeNone, render.Dash, "no daily record of buy and hold for this session"
	}
	out = append(out, mkt)

	// Costs.
	cost := scoreLine{area: "costs"}
	if t.GrossPnL > 0 {
		share := (t.Fees + t.Funding) / t.GrossPnL
		cost.figures = fmt.Sprintf("fees + funding %s of gross", render.Num(share*100, 0)+"%")
		switch {
		case share > 0.5:
			cost.grade, cost.why = gradeRed, "costs take over half the edge: slippage or a fee change ends it"
		case share > 0.3:
			cost.grade, cost.why = gradeAmber, "costs take a third or more of the edge"
		default:
			cost.grade, cost.why = gradeGreen, "costs leave most of the edge"
		}
		cost.why += "; the backtest has no slippage"
	} else {
		cost.grade, cost.figures, cost.why = gradeRed, fmt.Sprintf("gross %s", render.Signed(t.GrossPnL, 2)), "no edge before costs"
	}
	out = append(out, cost)

	out = append(out, scoreLine{area: "overfitting", grade: gradeNone, figures: render.Dash,
		why: "PBO, walk-forward and parameter sensitivity need a sweep of the settings: run one (workspace_id proposes it) and judge the median variant, not the best"})
	return out
}

// proposeSweep suggests a one-at-a-time sensitivity sweep of the declared
// numeric settings: each at −20%, −10%, +10% and +20% of its default, inside
// its declared range, plus the defaults themselves.
func proposeSweep(doc *render.Doc, ps []workspaceParam, only string, maxVariants int) {
	if maxVariants < 2 {
		maxVariants = 10
	}
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[strings.ToUpper(n)] = true
		}
	}
	var picked []workspaceParam
	for _, p := range ps {
		if p.Kind != "int" && p.Kind != "float" {
			continue
		}
		if len(want) > 0 && !want[strings.ToUpper(p.Name)] {
			continue
		}
		if len(want) == 0 && sweepSkip(p.Name) {
			continue
		}
		picked = append(picked, p)
		if len(want) == 0 && len(picked) == 3 {
			break
		}
	}
	if len(picked) == 0 {
		doc.Line("Sensitivity sweep: no numeric settings to vary. Declare them with dev_sdk/params (get_sdk_docs section=\"parameters\"), with a Range.")
		return
	}
	variants := []map[string]string{}
	base := map[string]string{}
	for _, p := range picked {
		base[p.Name] = p.Default
	}
	variants = append(variants, base)
	for _, p := range picked {
		def, err := strconv.ParseFloat(p.Default, 64)
		if err != nil || def == 0 {
			continue
		}
		seen := map[string]bool{p.Default: true}
		for _, f := range []float64{0.8, 0.9, 1.1, 1.2} {
			v := def * f
			if p.Min != nil {
				v = math.Max(v, *p.Min)
			}
			if p.Max != nil {
				v = math.Min(v, *p.Max)
			}
			s := strconv.FormatFloat(v, 'f', -1, 64)
			if p.Kind == "int" {
				s = strconv.FormatInt(int64(math.Round(v)), 10)
			} else {
				s = strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
			}
			if seen[s] {
				continue
			}
			seen[s] = true
			vv := map[string]string{}
			for k, x := range base {
				vv[k] = x
			}
			vv[p.Name] = s
			variants = append(variants, vv)
		}
	}
	if len(variants) > maxVariants {
		variants = variants[:maxVariants]
	}
	names := make([]string, len(picked))
	for i, p := range picked {
		names[i] = p.Name
	}
	sort.Strings(names)
	b, _ := json.Marshal(variants)
	doc.Line("Sensitivity sweep of %s, one setting at a time at −20/−10/+10/+20%% of its default, inside its range (%d variants, the first is the defaults):", strings.Join(names, ", "), len(variants))
	doc.Line("%s", string(b))
	doc.Line("Run it with workspace_run variants=<that> on the same window (lab_check first). A robust edge degrades gently around the defaults; a spike that only one value has is a fit. Judge the median variant, not the best, and count every variant in trials.")
}

// sweepSkip are settings that size or place the backtest rather than shape
// the strategy.
func sweepSkip(name string) bool {
	n := strings.ToUpper(name)
	for _, s := range []string{"CAPITAL", "BALANCE", "WALLET", "START", "END", "DAYS", "YEAR", "SEED"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}
