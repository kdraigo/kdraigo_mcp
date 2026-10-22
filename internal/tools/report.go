package tools

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

func addGetSessionReport(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_session_report",
		mcp.WithDescription("The results of a backtest or live session in one call. Start every evaluation here. "+
			"Header: starting capital to final equity, return, max drawdown, return over drawdown, net P&L split into gross, fees and funding, "+
			"trades and win rate per side, profit factor, average win and loss, order outcomes. "+
			"Then a table per instrument, P&L per calendar month (UTC) from daily closed-trade equity, "+
			"and warnings about whatever makes the figures less trustworthy: a run that has not finished or stopped short of its window, few trades, "+
			"positions still open at the end (valued at their last fill), liquidations, a result carried by one trade or one instrument, costs that eat the edge. "+
			"P&L counts on the day a position closes; fees on the day an order fills."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live")),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		kind := req.GetString("session_type", "backtest")
		if kind != "backtest" && kind != "live" {
			return toolErr(fmt.Errorf("session_type must be backtest or live")), nil
		}
		in := d.fetchReport(ctx, id, kind)
		if in.perfErr != nil {
			return toolErr(in.perfErr), nil
		}
		return toolText(renderReport(in)), nil
	})
}

// reportInputs is what one report is built from. Only the performance report
// is required; the rest add context and their failure is noted, not fatal.
type reportInputs struct {
	kind      string
	perf      performanceReport
	perfErr   error
	detail    *sessionDetail // backtests only: frontend_api has no live sessions
	detailErr error
	days      []seriesPoint
	daysErr   error
}

// seriesEpoch is earlier than any session; the server clamps a series to the
// session's own window, so asking from here is asking for all of it.
const seriesEpoch = 946_684_800_000 // 2000-01-01

func (d Deps) fetchReport(ctx context.Context, id, kind string) *reportInputs {
	in := &reportInputs{kind: kind}
	var wg sync.WaitGroup
	wg.Go(func() {
		q := url.Values{"session_id": {id}, "session_type": {kind}}
		in.perfErr = d.getJSON(ctx, "get_session_report", client.Analytics, "/api/v1/dev/analytics/performance", q, &in.perf)
	})
	if kind == "backtest" {
		wg.Go(func() {
			var det sessionDetail
			if in.detailErr = d.getJSON(ctx, "session detail", client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id), nil, &det); in.detailErr == nil {
				in.detail = &det
			}
		})
	}
	wg.Go(func() {
		q := url.Values{
			"session_id": {id}, "session_type": {kind}, "type": {"equity"}, "timeframe": {"1d"},
			"from": {strconv.FormatInt(seriesEpoch, 10)},
			"to":   {strconv.FormatInt(time.Now().Add(24*time.Hour).UnixMilli(), 10)},
		}
		var s seriesResponse
		if in.daysErr = d.getJSON(ctx, "daily equity", client.Analytics, "/api/v1/dev/analytics/series", q, &s); in.daysErr == nil {
			in.days = s.Points
		}
	})
	wg.Wait()
	return in
}

// terminalStatuses are the statuses a run never leaves, as analytics_service
// lists them.
var terminalStatuses = map[string]bool{
	"DONE": true, "FAILED": true, "COMPLETED": true, "FINISHED": true, "EXPIRED": true, "ABORTED": true,
}

// stoppedEarly are the terminal statuses of a run that did not reach the end
// of its window.
var stoppedEarly = map[string]bool{"FAILED": true, "EXPIRED": true, "ABORTED": true}

// minTrades is the count below which rates and averages are mostly noise.
const minTrades = 30

func renderReport(in *reportInputs) string {
	p := &in.perf
	t := &p.Total
	q := p.Quote
	doc := render.New()

	// Who and when.
	if det := in.detail; det != nil {
		doc.Line("Session %s%s · %s · %s · %s", det.ID, titled(det.Name), in.kind, det.Status, det.Timeframe)
		if det.StartAt != nil && det.EndAt != nil {
			doc.Line("Window %s → %s UTC · %s days", render.Date(*det.StartAt), render.Date(*det.EndAt), render.Num(det.EndAt.Sub(*det.StartAt).Hours()/24, 0))
		}
	} else {
		doc.Line("Session %s · %s", p.SessionID, in.kind)
	}

	// Headline.
	doc.Line("Capital %s %s → %s · return %s · max DD %s (%s) · return/DD %s",
		render.Num(p.InitialCapital, 2), q, render.Num(p.FinalEquity, 2),
		signedPct(t.ReturnPct), render.Num(t.MaxDrawdown, 2), render.OptPct(t.MaxDrawdownPct, 1), returnOverDD(t))
	doc.Line("Net %s = gross %s − fees %s − funding %s (funding positive = paid)",
		render.Signed(t.NetPnL, 2), render.Signed(t.GrossPnL, 2), render.Num(t.Fees, 2), render.Num(t.Funding, 2))
	doc.Line("Trades %d (long %d / short %d) · win rate %s (long %s / short %s) · PF %s · avg trade %s",
		t.Trades, t.LongTrades, t.ShortTrades, rate(t.Wins, t.Trades), rate(t.LongWins, t.LongTrades), rate(t.ShortWins, t.ShortTrades),
		render.Opt(t.ProfitFactor, 2), avgTrade(t))
	doc.Line("Avg win %s · avg loss %s · best %s · worst %s · liquidations %d · open at end %d",
		optSigned(t.AvgWin), optSigned(t.AvgLoss), optSigned(t.BestTrade), optSigned(t.WorstTrade), t.Liquidations, t.OpenAtEnd)
	doc.Line("Orders placed/filled/canceled: %s", orderTallies(&t.Orders))
	if p.ReportVersion >= 2 {
		renderResearch(doc, p)
	}

	warnings := reportWarnings(in)
	if p.ReportVersion >= 2 {
		warnings = serverWarnings(in)
	}
	if len(warnings) > 0 {
		doc.Blank()
		doc.Line("## warnings")
		for _, w := range warnings {
			doc.Line("- %s", w)
		}
	}

	doc.Blank()
	rows := make([][]string, 0, len(p.Instruments))
	for _, s := range p.Instruments {
		rows = append(rows, []string{
			s.Exchange, s.Pair, s.Model, render.Signed(s.NetPnL, 2), signedPct(s.ReturnPct),
			render.Int(s.Trades), fmt.Sprintf("%d/%d", s.LongTrades, s.ShortTrades), rate(s.Wins, s.Trades),
			render.Opt(s.ProfitFactor, 2), optSigned(s.AvgWin), optSigned(s.AvgLoss),
			render.OptPct(s.MaxDrawdownPct, 1), render.Opt(s.ReturnToDD, 2), signedOpt(s.AvgR, 2),
			render.Num(s.Fees, 2), render.Num(s.Funding, 2),
			render.Int(s.Liquidations), render.Int(s.OpenAtEnd),
		})
	}
	doc.Table(fmt.Sprintf("instruments (%d), busiest first; return is each one's share of the session's return", len(p.Instruments)),
		[]string{"exchange", "pair", "model", "net", "return", "trades", "L/S", "win", "pf", "avg_win", "avg_loss", "max_dd", "ret/dd", "avg_r", "fees", "funding", "liq", "open"},
		rows, nil)

	doc.Blank()
	switch {
	case in.daysErr != nil:
		doc.Line("## months: not available (%v)", in.daysErr)
	case len(in.days) == 0:
		doc.Line("## months: no days in the session's window yet")
	default:
		renderMonths(doc, in.days, p.InitialCapital)
	}
	return doc.String()
}

// flatPnl is a change too small to call a day up or down.
const flatPnl = 0.005

type monthPnl struct {
	key                string // 2026-10
	net, open          float64
	days, up, down     int
	worstDay, worstPnl float64
	worstAt            int64
}

// renderMonths adds the P&L per UTC month, from a 1d equity series: each
// day's P&L is its change from the day before, the first day's from the
// starting capital.
func renderMonths(doc *render.Doc, pts []seriesPoint, capital float64) {
	sort.Slice(pts, func(i, j int) bool { return pts[i].OpenTime < pts[j].OpenTime })
	var months []*monthPnl
	prev := capital
	if capital == 0 {
		prev = pts[0].Value
	}
	upDays, downDays := 0, 0
	var best, worst *seriesPoint
	bestPnl, worstPnl := 0.0, 0.0
	for i := range pts {
		pt := &pts[i]
		pnl := pt.Value - prev
		key := time.UnixMilli(pt.OpenTime).UTC().Format("2006-01")
		if len(months) == 0 || months[len(months)-1].key != key {
			months = append(months, &monthPnl{key: key, open: prev})
		}
		m := months[len(months)-1]
		m.net += pnl
		m.days++
		switch {
		case pnl >= flatPnl:
			m.up++
			upDays++
		case pnl <= -flatPnl:
			m.down++
			downDays++
		}
		if pnl < m.worstPnl {
			m.worstPnl, m.worstAt = pnl, pt.OpenTime
		}
		if pnl > bestPnl {
			best, bestPnl = pt, pnl
		}
		if pnl < worstPnl {
			worst, worstPnl = pt, pnl
		}
		prev = pt.Value
	}

	losing, worstMonth, bestMonth := 0, months[0], months[0]
	streak, longest := 0, 0
	for _, m := range months {
		if m.net <= -flatPnl {
			losing++
			streak++
			longest = max(longest, streak)
		} else {
			streak = 0
		}
		if m.net < worstMonth.net {
			worstMonth = m
		}
		if m.net > bestMonth.net {
			bestMonth = m
		}
	}
	doc.Line("## months (UTC), P&L by closed-trade equity")
	doc.Line("%d months: %d losing, longest losing run %d · best %s %s · worst %s %s",
		len(months), losing, longest, bestMonth.key, render.Signed(bestMonth.net, 2), worstMonth.key, render.Signed(worstMonth.net, 2))
	line := fmt.Sprintf("%d days: %d up, %d down, %d flat", len(pts), upDays, downDays, len(pts)-upDays-downDays)
	if best != nil {
		line += fmt.Sprintf(" · best day %s %s", render.Date(time.UnixMilli(best.OpenTime)), render.Signed(bestPnl, 2))
	}
	if worst != nil {
		line += fmt.Sprintf(" · worst day %s %s", render.Date(time.UnixMilli(worst.OpenTime)), render.Signed(worstPnl, 2))
	}
	doc.Line("%s", line)

	rows := make([][]string, 0, len(months))
	for _, m := range months {
		ret := render.Dash
		if m.open > 0 {
			ret = render.Signed(m.net/m.open*100, 2) + "%"
		}
		worstDay := render.Dash
		if m.worstPnl < 0 {
			worstDay = render.Date(time.UnixMilli(m.worstAt)) + " " + render.Signed(m.worstPnl, 2)
		}
		rows = append(rows, []string{m.key, render.Signed(m.net, 2), ret, render.Int(m.days), render.Int(m.up), render.Int(m.down), worstDay})
	}
	doc.Table("", []string{"month", "net", "return", "days", "up", "down", "worst_day"}, rows, nil)
}

// reportWarnings lists what a reader should know before trusting the figures,
// most serious first.
func reportWarnings(in *reportInputs) []string {
	p := &in.perf
	t := &p.Total
	var out []string

	if det := in.detail; det != nil {
		switch {
		case !terminalStatuses[strings.ToUpper(det.Status)]:
			out = append(out, fmt.Sprintf("Run not finished (status %s): every figure is partial and will change.", det.Status))
		case stoppedEarly[strings.ToUpper(det.Status)]:
			out = append(out, fmt.Sprintf("Run ended %s: it stopped before the end of its window, so the figures cover only what ran.", det.Status))
		}
		if pr := det.Progress; pr != nil && pr.TotalCandles > 0 && pr.Percent < 99 {
			msg := fmt.Sprintf("Processed %d of %d candles (%s)", pr.ProcessedCandles, pr.TotalCandles, render.Pct(pr.Percent, 1))
			if pr.LastCandleTime != nil && det.EndAt != nil {
				msg += fmt.Sprintf(", last candle %s against a window ending %s", render.Time(*pr.LastCandleTime), render.Time(*det.EndAt))
			}
			out = append(out, msg+": missing data or an early stop. Gaps in the exchange's history also lower this.")
		}
	} else if in.detailErr != nil {
		out = append(out, fmt.Sprintf("Session record not available, so completion is unchecked: %v", in.detailErr))
	}

	if t.Trades < minTrades {
		out = append(out, fmt.Sprintf("Only %d closed trades: win rate, profit factor and averages are mostly noise below %d.", t.Trades, minTrades))
	}

	if t.OpenAtEnd > 0 {
		unreal := 0.0
		for _, s := range p.Instruments {
			if s.OpenPosition != nil {
				unreal += s.OpenPosition.UnrealizedAtLastFill
			}
		}
		msg := fmt.Sprintf("%d position(s) open at the end, valued at their last fill: %s of the net P&L", t.OpenAtEnd, render.Signed(unreal, 2))
		if t.NetPnL != 0 {
			msg += fmt.Sprintf(" (%s)", render.Pct(math.Abs(unreal/t.NetPnL)*100, 0))
		}
		out = append(out, msg+". Where they would have closed is unknown.")
	}

	if t.Liquidations > 0 {
		out = append(out, fmt.Sprintf("%d liquidation(s).", t.Liquidations))
	}

	if t.NetPnL > 0 && t.BestTrade != nil && *t.BestTrade/t.NetPnL > 0.3 {
		out = append(out, fmt.Sprintf("The best trade (%s) is %s of the net P&L: the result rests on few trades.",
			render.Signed(*t.BestTrade, 2), render.Pct(*t.BestTrade/t.NetPnL*100, 0)))
	}
	if t.NetPnL > 0 && len(p.Instruments) >= 3 {
		top := p.Instruments[0]
		for _, s := range p.Instruments[1:] {
			if s.NetPnL > top.NetPnL {
				top = s
			}
		}
		if top.NetPnL/t.NetPnL > 0.5 {
			out = append(out, fmt.Sprintf("%s %s made %s of the net P&L across %d instruments.",
				top.Exchange, top.Pair, render.Pct(top.NetPnL/t.NetPnL*100, 0), len(p.Instruments)))
		}
	}

	if g := math.Abs(t.GrossPnL); g > 0 {
		if t.Fees/g > 0.5 {
			out = append(out, fmt.Sprintf("Fees are %s of gross P&L: the edge barely covers costs.", render.Pct(t.Fees/g*100, 0)))
		}
		if t.Funding/g > 0.25 {
			out = append(out, fmt.Sprintf("Funding paid is %s of gross P&L.", render.Pct(t.Funding/g*100, 0)))
		}
	}

	if len(p.UnpricedAssets) > 0 {
		out = append(out, fmt.Sprintf("Starting balances with no price, left out of capital: %s.", strings.Join(p.UnpricedAssets, ", ")))
	}
	return out
}

// renderResearch adds what report version 2 measures: R, how much the result
// rests on a few trades, how long it stayed down, and the daily ratios.
func renderResearch(doc *render.Doc, p *performanceReport) {
	t := &p.Total
	if t.RTrades > 0 {
		doc.Line("R (net, per trade with an initial stop): avg %s ± %s (std err) over %d of %d trades · sum %s · winners over 5R %d",
			signedOpt(t.AvgR, 3), render.Opt(t.RSE, 3), t.RTrades, t.Trades, signedOpt(t.SumR, 1), t.WinnersOver5R)
	} else {
		doc.Line("R: no trade has an initial stop (a protective STOP_LOSS, or `stop` in the entry order's reason)")
	}
	doc.Line("Without the 5 best trades net is %s (they are %s of net) · longest losing streak %d · longest underwater %s days · exposure %s · avg hold %s",
		signedOpt(t.NetWithoutTop5, 2), optShare(t.Top5Share), t.LongestLosingStreak,
		render.Opt(t.LongestUnderwaterDays, 1), render.OptPct(t.ExposurePct, 0), holdOpt(t.AvgHoldSec))
	months := fmt.Sprintf("%d months, %d losing", t.Months, t.LosingMonths)
	if t.WorstMonth != nil {
		months += fmt.Sprintf(", worst %s %s", t.WorstMonth.Key, render.Signed(t.WorstMonth.Net, 2))
	}
	r := &p.Ratios
	doc.Line("Daily ratios over %d days: Sharpe %s · Sortino %s · CAGR %s · Calmar %s · %s · instruments positive %d of %d",
		r.Days, render.Opt(r.Sharpe, 2), render.Opt(r.Sortino, 2), signedPctOpt(r.CAGR), render.Opt(r.Calmar, 2),
		months, p.InstrumentsPositive, len(p.Instruments))
}

// serverWarnings are the report's own, plus what only this tool knows.
func serverWarnings(in *reportInputs) []string {
	var out []string
	for _, w := range in.perf.Warnings {
		out = append(out, fmt.Sprintf("[%s] %s", w.Severity, w.Message))
	}
	if in.detail == nil && in.detailErr != nil {
		out = append(out, fmt.Sprintf("Session record not available: %v", in.detailErr))
	}
	return out
}

func signedOpt(v *float64, dp int) string {
	if v == nil {
		return render.Dash
	}
	return render.Signed(*v, dp)
}

func signedPctOpt(v *float64) string {
	if v == nil {
		return render.Dash
	}
	return render.Signed(*v, 1) + "%"
}

// optShare is a fraction as a percentage.
func optShare(v *float64) string {
	if v == nil {
		return render.Dash
	}
	return render.Pct(*v*100, 0)
}

func holdOpt(sec *float64) string {
	if sec == nil {
		return render.Dash
	}
	return hold(int64(*sec))
}

// hold is a holding time as one rounded unit: 45m, 6.5h, 3.2d.
func hold(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return render.Num(d.Hours(), 1) + "h"
	}
	return render.Num(d.Hours()/24, 1) + "d"
}

func signedPct(v *float64) string {
	if v == nil {
		return render.Dash
	}
	return render.Signed(*v, 2) + "%"
}

func returnOverDD(s *stats) string {
	if s.ReturnPct == nil || s.MaxDrawdownPct == nil || *s.MaxDrawdownPct <= 0 {
		return render.Dash
	}
	return render.Num(*s.ReturnPct / *s.MaxDrawdownPct, 2)
}

// rate is wins over trades as a percentage; Dash without trades.
func rate(wins, trades int) string {
	if trades == 0 {
		return render.Dash
	}
	return render.Pct(float64(wins)/float64(trades)*100, 1)
}

func avgTrade(s *stats) string {
	if s.Trades == 0 {
		return render.Dash
	}
	return render.Signed(s.NetPnL/float64(s.Trades), 2)
}

func optSigned(v *float64) string {
	if v == nil {
		return render.Dash
	}
	return render.Signed(*v, 2)
}

// orderTallies lists the order kinds a session used, all of them first.
func orderTallies(b *orderBreakdown) string {
	parts := []string{"all " + tally(b.Total)}
	for _, k := range []struct {
		name string
		t    orderTally
	}{
		{"market", b.Market}, {"limit", b.Limit}, {"stop_loss", b.StopLoss}, {"take_profit", b.TakeProfit},
		{"liquidation", b.Liquidation}, {"other", b.Other},
	} {
		if k.t.Placed > 0 {
			parts = append(parts, k.name+" "+tally(k.t))
		}
	}
	return strings.Join(parts, " · ")
}

func tally(t orderTally) string {
	s := fmt.Sprintf("%d/%d/%d", t.Placed, t.Filled, t.Canceled)
	if t.Rejected > 0 {
		s += fmt.Sprintf(" (%d rejected)", t.Rejected)
	}
	return s
}
