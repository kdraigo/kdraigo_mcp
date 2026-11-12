package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// Execution and market checks on one session (cost stress, entry edge), two
// sessions against each other (compare_sessions), and live bots against their
// backtests (get_live_cone, get_live_drift).

type (
	costCell struct {
		FeeMult     float64  `json:"fee_mult"`
		SlippageBps float64  `json:"slippage_bps"`
		Net         float64  `json:"net"`
		ReturnPct   float64  `json:"return_pct"`
		WinRate     float64  `json:"win_rate"`
		PF          *float64 `json:"pf"`
		AvgR        *float64 `json:"avg_r"`
		// WipedOutAt: these costs take the account to zero at that
		// trade's close (WipedOutTrade, in close order); the return
		// stops at -100%.
		WipedOutAt    *time.Time `json:"wiped_out_at"`
		WipedOutTrade int        `json:"wiped_out_trade"`
	}
	costsResponse struct {
		SessionID            string          `json:"session_id"`
		Trades               int             `json:"trades"`
		Net                  float64         `json:"net"`
		Gross                float64         `json:"gross"`
		Fees                 float64         `json:"fees"`
		Funding              float64         `json:"funding"`
		Turnover             float64         `json:"turnover"`
		FeesBps              float64         `json:"fees_bps"`
		BreakEvenFeeMult     *float64        `json:"break_even_fee_mult"`
		BreakEvenSlippageBps *float64        `json:"break_even_slippage_bps"`
		CostR                *float64        `json:"cost_r"`
		SlipR                *float64        `json:"slippage_r_per_bp"`
		Grid                 []costCell      `json:"grid"`
		Notes                []string        `json:"notes"`
		Warnings             []reportWarning `json:"warnings"`
	}
	edgeHorizon struct {
		Days          int     `json:"days"`
		Trades        int     `json:"trades"`
		MeanBps       float64 `json:"mean_bps"`
		RandomMeanBps float64 `json:"random_mean_bps"`
		RandomSDBps   float64 `json:"random_sd_bps"`
		HitRate       float64 `json:"hit_rate"`
		RandomHitRate float64 `json:"random_hit_rate"`
		PValue        float64 `json:"p_value"`
		Draws         int     `json:"draws"`
	}
	entriesResponse struct {
		SessionID   string        `json:"session_id"`
		Instruments int           `json:"instruments"`
		Entries     int           `json:"entries"`
		Horizons    []edgeHorizon `json:"horizons"`
		Notes       []string      `json:"notes"`
	}
	compareResponse struct {
		A            string   `json:"a"`
		B            string   `json:"b"`
		Source       string   `json:"source"`
		From         string   `json:"from"`
		To           string   `json:"to"`
		Days         int      `json:"days"`
		SharpeA      float64  `json:"sharpe_a"`
		SharpeB      float64  `json:"sharpe_b"`
		Diff         float64  `json:"diff"`
		DiffP5       float64  `json:"diff_p5"`
		DiffP95      float64  `json:"diff_p95"`
		PValue       float64  `json:"p_value"`
		Correlation  float64  `json:"correlation"`
		ReturnPctA   float64  `json:"return_pct_a"`
		ReturnPctB   float64  `json:"return_pct_b"`
		MaxDDPctA    float64  `json:"max_drawdown_pct_a"`
		MaxDDPctB    float64  `json:"max_drawdown_pct_b"`
		ShareBBetter float64  `json:"share_b_better"`
		Notes        []string `json:"notes"`
	}
	fanDay struct {
		Day  int     `json:"day"`
		P5   float64 `json:"p5"`
		P25  float64 `json:"p25"`
		P50  float64 `json:"p50"`
		P75  float64 `json:"p75"`
		P95  float64 `json:"p95"`
		Live float64 `json:"live"`
	}
	coneResponse struct {
		LiveSessionID     string   `json:"live_session_id"`
		BacktestSessionID string   `json:"backtest_session_id"`
		ScaleSource       string   `json:"scale_source"`
		LiveRiskPct       *float64 `json:"live_risk_pct"`
		BacktestRiskPct   *float64 `json:"backtest_risk_pct"`
		LiveFrom          string   `json:"live_from"`
		Days              int      `json:"days"`
		Paths             int      `json:"paths"`
		Scale             float64  `json:"scale"`
		Fan               []fanDay `json:"fan"`
		LivePct           float64  `json:"live_pct"`
		Percentile        float64  `json:"percentile"`
		DaysBelowP5       int      `json:"days_below_p5"`
		DaysAboveP95      int      `json:"days_above_p95"`
		Notes             []string `json:"notes"`
	}
	driftTrade struct {
		Pair    string    `json:"pair"`
		Side    string    `json:"side"`
		EntryAt time.Time `json:"entry_at"`
	}
	driftMatch struct {
		Pair         string    `json:"pair"`
		Side         string    `json:"side"`
		LiveEntryAt  time.Time `json:"live_entry_at"`
		LagSec       int64     `json:"lag_sec"`
		EntrySlipBps float64   `json:"entry_slip_bps"`
		ExitSlipBps  *float64  `json:"exit_slip_bps"`
		LiveNetR     *float64  `json:"live_net_r"`
		BacktestNetR *float64  `json:"backtest_net_r"`
		LiveNet      float64   `json:"live_net"`
		BacktestNet  float64   `json:"backtest_net"`
	}
	driftResponse struct {
		From               time.Time    `json:"from"`
		To                 time.Time    `json:"to"`
		ToleranceSec       int64        `json:"tolerance_sec"`
		LiveTrades         int          `json:"live_trades"`
		BacktestTrades     int          `json:"backtest_trades"`
		Matched            int          `json:"matched"`
		Missed             int          `json:"missed"`
		Extra              int          `json:"extra"`
		MissedTrades       []driftTrade `json:"missed_trades"`
		ExtraTrades        []driftTrade `json:"extra_trades"`
		EntrySlipMedianBps *float64     `json:"entry_slip_median_bps"`
		EntrySlipMeanBps   *float64     `json:"entry_slip_mean_bps"`
		ExitSlipMedianBps  *float64     `json:"exit_slip_median_bps"`
		ExitSlipMeanBps    *float64     `json:"exit_slip_mean_bps"`
		LiveAvgR           *float64     `json:"live_avg_r"`
		BacktestAvgR       *float64     `json:"backtest_avg_r"`
		LiveNet            float64      `json:"live_net"`
		BacktestNet        float64      `json:"backtest_net"`
		Matches            []driftMatch `json:"matches"`
		Notes              []string     `json:"notes"`
	}
)

// RegisterExecution adds get_cost_stress, get_entry_edge, compare_sessions,
// get_live_cone and get_live_drift.
func RegisterExecution(s *server.MCPServer, d Deps) {
	sessionTool := func(name, desc string, extra []mcp.ToolOption, path string, keys []string, rf func(*render.Doc, []byte) error) {
		opts := []mcp.ToolOption{mcp.WithDescription(desc),
			mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
			mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live"))}
		opts = append(append(opts, extra...), readOnly())
		addTimeout(s, mcp.NewTool(name, opts...), heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := sessionArgs(req)
			if err != nil {
				return toolErr(err), nil
			}
			for _, k := range keys {
				if v := strings.TrimSpace(req.GetString(k, "")); v != "" {
					q.Set(k, v)
				} else {
					setNum(q, req, k)
				}
			}
			return d.renderGet(ctx, name, path, q, rf), nil
		})
	}
	sessionTool("get_cost_stress", "How much worse execution does this result survive? Charges every closed trade again with fees ×1, ×1.5, ×2 and slippage of 0, 2, 5, 10, 20 basis points on every fill, "+
		"and gives the break-even: the fee multiplier and the slippage at which net reaches zero, plus what costs already take in R. "+
		"Trades stay where they were (a cost that moves a stop is missed): a sweep with the stress settings confirms it. Spends no runs.",
		[]mcp.ToolOption{
			mcp.WithString("fee_mults", mcp.Description("Comma-separated fee multipliers, default 1,1.5,2")),
			mcp.WithString("slippage_bps", mcp.Description("Comma-separated slippage in basis points, default 0,2,5,10,20")),
		}, "/api/v1/dev/analytics/costs", []string{"fee_mults", "slippage_bps"}, renderCosts)
	sessionTool("get_entry_edge", "Do the entries know anything? Each entry's move from its entry day's close to 1, 3 and 7 days later, signed by its side, against random entries on the same instruments and sides drawn a thousand times: "+
		"mean move, hit rate and a p-value per horizon. Exits and stops are left out on purpose: an edge that is not in the entries is in the exits or the sizing. Needs the engine's daily record.",
		[]mcp.ToolOption{mcp.WithString("horizons", mcp.Description("Comma-separated days, default 1,3,7"))},
		"/api/v1/dev/analytics/entries", []string{"horizons"}, renderEntries)

	cmp := mcp.NewTool("compare_sessions",
		mcp.WithDescription("Is B really better than A? Tests B's annual Sharpe against A's on the days both cover, by a paired block bootstrap (after Ledoit & Wolf 2008): the difference with its 5–95% range, a p-value, "+
			"how often B came out ahead, their correlation, returns and drawdowns. Use it to decide whether a change helped, rather than eyeballing two reports."),
		mcp.WithString("a", mcp.Required(), mcp.Description("Session A (the baseline)")),
		mcp.WithString("b", mcp.Required(), mcp.Description("Session B (the change)")),
		mcp.WithString("type_a", mcp.Description("backtest (default) or live"), mcp.Enum("backtest", "live")),
		mcp.WithString("type_b", mcp.Description("backtest (default) or live"), mcp.Enum("backtest", "live")),
		readOnly(),
	)
	addTimeout(s, cmp, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := url.Values{}
		for _, k := range []string{"a", "b", "type_a", "type_b"} {
			if v := strings.TrimSpace(req.GetString(k, "")); v != "" {
				q.Set(k, v)
			}
		}
		return d.renderGet(ctx, "compare_sessions", "/api/v1/dev/analytics/compare", q, renderCompare), nil
	})

	cone := mcp.NewTool("get_live_cone",
		mcp.WithDescription("Is a live bot's record still what its backtest promised? The backtest's days are drawn thousands of times into paths as long as the live record (scaled to the bot's risk per trade), "+
			"and the live cumulative return is placed in that cone: its percentile today, and the days it spent below the 5th or above the 95th. Below the 5th is unlikely to be bad luck. Read only."),
		mcp.WithString("live_session_id", mcp.Required(), mcp.Description("The live bot (list_live_sessions)")),
		mcp.WithString("backtest_session_id", mcp.Required(), mcp.Description("Its backtest: the same code and settings")),
		mcp.WithNumber("scale", mcp.Description("Live risk per trade over the backtest's; default from their median risk to the initial stop")),
		readOnly(),
	)
	addTimeout(s, cone, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := url.Values{"live_session_id": {req.GetString("live_session_id", "")}, "backtest_session_id": {req.GetString("backtest_session_id", "")}}
		setNum(q, req, "scale")
		return d.renderGet(ctx, "get_live_cone", "/api/v1/dev/analytics/live_cone", q, renderCone), nil
	})

	drift := mcp.NewTool("get_live_drift",
		mcp.WithDescription("A live bot against its backtest, trade by trade: live trades matched to the backtest's by pair, side and entry time (within tolerance_min), "+
			"the entry and exit slippage in basis points (positive = live filled worse), trades the bot missed or added, and R live against backtest. Run the backtest over the bot's live window first. Read only."),
		mcp.WithString("live_session_id", mcp.Required(), mcp.Description("The live bot")),
		mcp.WithString("backtest_session_id", mcp.Required(), mcp.Description("A backtest of the same code and settings over the live window")),
		mcp.WithNumber("tolerance_min", mcp.Description("Entry time tolerance in minutes, default 120")),
		readOnly(),
	)
	addTimeout(s, drift, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := url.Values{"live_session_id": {req.GetString("live_session_id", "")}, "backtest_session_id": {req.GetString("backtest_session_id", "")}}
		setNum(q, req, "tolerance_min")
		return d.renderGet(ctx, "get_live_drift", "/api/v1/dev/analytics/drift", q, renderDrift), nil
	})
}

// renderGet reads an analytics route and renders it.
func (d Deps) renderGet(ctx context.Context, name, path string, q url.Values, rf func(*render.Doc, []byte) error) *mcp.CallToolResult {
	var raw json.RawMessage
	if err := d.getValidation(ctx, name, path, q, &raw); err != nil {
		return toolErr(err)
	}
	doc := render.New()
	if err := rf(doc, raw); err != nil {
		return toolErr(fmt.Errorf("%s: %w", name, err))
	}
	return toolText(doc.String())
}

func renderCosts(doc *render.Doc, b []byte) error {
	var c costsResponse
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	doc.Line("Cost stress of session %s: %d closed trades · net %s · fees %s (%s of turnover) · funding %s.",
		c.SessionID, c.Trades, render.Signed(c.Net, 2), render.Num(c.Fees, 2), render.Num(c.FeesBps, 1)+" bp", render.Num(c.Funding, 2))
	be := "net is not positive now: there is no headroom to find"
	if c.BreakEvenSlippageBps != nil {
		be = fmt.Sprintf("net reaches zero at %s bp of slippage on every fill (at today's fees), or at fees ×%s (no slippage)",
			render.Num(*c.BreakEvenSlippageBps, 1), render.Opt(c.BreakEvenFeeMult, 2))
	}
	doc.Line("Break-even: %s.", be)
	if c.CostR != nil {
		doc.Line("Per trade (median): fees and funding take %sR already; each basis point of slippage takes %sR more.", render.Num(*c.CostR, 3), render.Opt(c.SlipR, 3))
	}
	rows := make([][]string, 0, len(c.Grid))
	for _, g := range c.Grid {
		ret := render.Signed(g.ReturnPct, 2) + "%"
		if g.WipedOutAt != nil {
			ret += fmt.Sprintf(" (account gone at trade %d of %d, %s)", g.WipedOutTrade, c.Trades, g.WipedOutAt.UTC().Format("2006-01-02"))
		}
		rows = append(rows, []string{"×" + render.Num(g.FeeMult, -1), render.Num(g.SlippageBps, -1), render.Signed(g.Net, 2), ret,
			render.Num(g.WinRate*100, 1) + "%", render.Opt(g.PF, 2), signedOpt(g.AvgR, 3)})
	}
	doc.Table("the closed trades charged again", []string{"fees", "slip_bp", "net", "return", "win", "pf", "avg_r"}, rows, nil)
	for _, n := range c.Notes {
		doc.Line("Note: %s", n)
	}
	highWarnings(doc, c.Warnings)
	doc.Line("Confirm with a re-run: workspace_run stress=standard (6 runs: fees ×1.5 and ×2, 5 and 10 bp slippage, funding off; dev_sdk v1.10.0 and later).")
	return nil
}

func renderEntries(doc *render.Doc, b []byte) error {
	var e entriesResponse
	if err := json.Unmarshal(b, &e); err != nil {
		return err
	}
	doc.Line("Entry edge of session %s: %d entries on %d instruments against random entries on the same instruments and sides.", e.SessionID, e.Entries, e.Instruments)
	rows := make([][]string, 0, len(e.Horizons))
	for _, h := range e.Horizons {
		rows = append(rows, []string{render.Int(h.Days) + "d", render.Int(h.Trades), render.Signed(h.MeanBps, 1), render.Signed(h.RandomMeanBps, 1) + " ± " + render.Num(h.RandomSDBps, 1),
			render.Num(h.HitRate*100, 1) + "%", render.Num(h.RandomHitRate*100, 1) + "%", render.Num(h.PValue, 3)})
	}
	doc.Table("signed move after the entry day's close, basis points", []string{"horizon", "entries", "mean_bp", "random_mean_bp", "hit", "random_hit", "p_value"}, rows, nil)
	doc.Line("p under 0.05: the entries catch moves random entries don't. Above it, any edge is in the exits or the sizing, not the timing of entries.")
	for _, n := range e.Notes {
		doc.Line("Note: %s", n)
	}
	return nil
}

func renderCompare(doc *render.Doc, b []byte) error {
	var c compareResponse
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	doc.Line("A %s against B %s over %d days (%s → %s, %s).", c.A, c.B, c.Days, c.From, c.To, strings.ReplaceAll(c.Source, "_", "-"))
	doc.Line("Sharpe A %s · B %s · difference %s (5–95%%: %s to %s) · p %s · B ahead in %s of resamples · correlation %s.",
		render.Num(c.SharpeA, 2), render.Num(c.SharpeB, 2), render.Signed(c.Diff, 2), render.Signed(c.DiffP5, 2), render.Signed(c.DiffP95, 2),
		render.Num(c.PValue, 3), pct0(c.ShareBBetter), render.Num(c.Correlation, 2))
	doc.Line("Return A %s, B %s · max drawdown A %s, B %s.", render.Signed(c.ReturnPctA, 1)+"%", render.Signed(c.ReturnPctB, 1)+"%",
		render.Num(c.MaxDDPctA, 1)+"%", render.Num(c.MaxDDPctB, 1)+"%")
	switch {
	case c.PValue < 0.05 && c.Diff > 0:
		doc.Line("Verdict: B's Sharpe is higher, beyond what resampling the same days explains.")
	case c.PValue < 0.05:
		doc.Line("Verdict: B's Sharpe is lower, beyond what resampling the same days explains.")
	default:
		doc.Line("Verdict: no difference the days can tell apart (p %s): treat the change as not shown to help.", render.Num(c.PValue, 2))
	}
	for _, n := range c.Notes {
		doc.Line("Note: %s", n)
	}
	return nil
}

func renderCone(doc *render.Doc, b []byte) error {
	var c coneResponse
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	scale := "not scaled"
	switch c.ScaleSource {
	case "risk":
		scale = fmt.Sprintf("scaled ×%s (live risks %s per trade, the backtest %s)", render.Num(c.Scale, 2), pctOpt(c.LiveRiskPct, 2), pctOpt(c.BacktestRiskPct, 2))
	case "given":
		scale = "scaled ×" + render.Num(c.Scale, 2)
	}
	doc.Line("Live bot %s since %s (%d days) against backtest %s: %d paths, %s.", c.LiveSessionID, c.LiveFrom, c.Days, c.BacktestSessionID, c.Paths, scale)
	where := "inside the cone"
	switch {
	case c.Percentile < 0.05:
		where = "BELOW the cone: unlikely to be the backtested edge having bad luck"
	case c.Percentile < 0.25:
		where = "in the lower part of the cone"
	case c.Percentile > 0.95:
		where = "above the cone: better than the backtest allows, check sizing and data"
	}
	doc.Line("Live %s sits at the %s percentile: %s. Days below the 5th: %d · above the 95th: %d.",
		render.Signed(c.LivePct, 2)+"%", ordinal(int(c.Percentile*100+0.5)), where, c.DaysBelowP5, c.DaysAboveP95)
	step := max(1, len(c.Fan)/20)
	rows := [][]string{}
	for i, f := range c.Fan {
		if i%step != 0 && i != len(c.Fan)-1 {
			continue
		}
		rows = append(rows, []string{render.Int(f.Day), render.Signed(f.Live, 2), render.Signed(f.P5, 2), render.Signed(f.P25, 2), render.Signed(f.P50, 2), render.Signed(f.P75, 2), render.Signed(f.P95, 2)})
	}
	doc.Table("cumulative return by day, %", []string{"day", "live", "p5", "p25", "p50", "p75", "p95"}, rows, nil)
	for _, n := range c.Notes {
		doc.Line("Note: %s", n)
	}
	doc.Line("Flag, don't act: stopping or resizing a bot is the user's call.")
	return nil
}

func renderDrift(doc *render.Doc, b []byte) error {
	var d driftResponse
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	doc.Line("Live against backtest over %s → %s (tolerance %s): %d live trades, %d backtest trades in the window · %d matched · %d missed by the bot · %d extra.",
		render.Time(d.From), render.Time(d.To), (time.Duration(d.ToleranceSec) * time.Second).String(), d.LiveTrades, d.BacktestTrades, d.Matched, d.Missed, d.Extra)
	doc.Line("Slippage, positive = live filled worse: entry median %s bp (mean %s) · exit median %s bp (mean %s).",
		render.Opt(d.EntrySlipMedianBps, 1), render.Opt(d.EntrySlipMeanBps, 1), render.Opt(d.ExitSlipMedianBps, 1), render.Opt(d.ExitSlipMeanBps, 1))
	doc.Line("Matched trades: avg R live %s, backtest %s · net live %s, backtest %s.",
		signedOpt(d.LiveAvgR, 3), signedOpt(d.BacktestAvgR, 3), render.Signed(d.LiveNet, 2), render.Signed(d.BacktestNet, 2))
	rows := make([][]string, 0, len(d.Matches))
	for _, m := range d.Matches[:min(len(d.Matches), 20)] {
		rows = append(rows, []string{m.Pair, m.Side, render.Time(m.LiveEntryAt), strconv.FormatInt(m.LagSec/60, 10) + "m", render.Signed(m.EntrySlipBps, 1),
			signedOpt(m.ExitSlipBps, 1), signedOpt(m.LiveNetR, 2), signedOpt(m.BacktestNetR, 2)})
	}
	doc.Table("matched trades (first 20)", []string{"pair", "side", "live_entry", "lag", "entry_slip_bp", "exit_slip_bp", "live_r", "backtest_r"}, rows, nil)
	list := func(name string, ts []driftTrade, n int) {
		if n == 0 {
			return
		}
		parts := make([]string, 0, len(ts))
		for _, t := range ts {
			parts = append(parts, fmt.Sprintf("%s %s %s", t.Pair, t.Side, render.Time(t.EntryAt)))
		}
		doc.Line("%s (%d): %s", name, n, strings.Join(parts, " · "))
	}
	list("Missed by the bot", d.MissedTrades, d.Missed)
	list("Extra in the bot", d.ExtraTrades, d.Extra)
	for _, n := range d.Notes {
		doc.Line("Note: %s", n)
	}
	return nil
}
