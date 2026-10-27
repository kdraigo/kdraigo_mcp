package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

const (
	defaultTradesLimit = 100
	maxTradesLimit     = 1000
	// maxSavedTrades bounds one export: 100 pages of /analytics/trades.
	maxSavedTrades = 100 * maxTradesLimit
)

var exitKinds = []string{"stop_loss", "take_profit", "market", "limit", "liquidation", "other", "open"}

func addGetTrades(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_trades",
		mcp.WithDescription("A session's trades, one row each: a futures position from open to close, or a spot position flat to flat. "+
			"Each has entry and exit time and price, size, adds and partial exits, gross, fees, funding and net, how it exited, its initial stop and its result in R (net over the risk to the initial stop). "+
			"The initial stop is the first protective STOP_LOSS placed with or after the entry, or else the numeric `stop` in the entry order's reason; trades without one have no R. "+
			"Flags: open (still open at the end and in no statistic: its net is only what partial closes made and what it cost so far; its unrealized P&L at the last close is shown with the flag), end (closed by a market or limit order in the run's last bar), liq (liquidated), unrec (the fills disagree with the engine's position record), inh (began as a starting balance). "+
			"Sort by net or net_r to find the trades that made or broke the result; save_to writes every matching trade with all fields to a local CSV."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live")),
		mcp.WithString("exchange", mcp.Description("Only this instrument, with pair")),
		mcp.WithString("pair", mcp.Description("Only this instrument, with exchange, e.g. BTC/USDT")),
		mcp.WithString("side", mcp.Description("Only long or short trades"), mcp.Enum("long", "short")),
		mcp.WithString("exit_kind", mcp.Description("Only trades that exited this way"), mcp.Enum(exitKinds...)),
		mcp.WithNumber("from", mcp.Description("Entered at or after this time, Unix milliseconds")),
		mcp.WithNumber("to", mcp.Description("Entered before this time, Unix milliseconds")),
		mcp.WithString("sort", mcp.Description("entry_at (default), net or net_r; trades without R sort last under net_r"), mcp.Enum("entry_at", "net", "net_r")),
		mcp.WithString("order", mcp.Description("asc or desc; defaults to asc for entry_at, desc otherwise"), mcp.Enum("asc", "desc")),
		mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Rows to return, at most %d", maxTradesLimit)), mcp.DefaultNumber(defaultTradesLimit)),
		mcp.WithNumber("offset", mcp.Description("Rows to skip"), mcp.DefaultNumber(0)),
		mcp.WithString("save_to", mcp.Description("Absolute path of a .csv file to write every matching trade to, in the chosen order; its folder must exist, and an existing file there is replaced. Ignores limit and offset.")),
		additive(true),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		q := url.Values{"session_id": {id}, "session_type": {req.GetString("session_type", "backtest")}}
		for _, k := range []string{"exchange", "pair", "side", "exit_kind", "sort", "order"} {
			if v := strings.TrimSpace(req.GetString(k, "")); v != "" {
				q.Set(k, v)
			}
		}
		for _, k := range []string{"from", "to"} {
			if v := req.GetFloat(k, 0); v > 0 {
				q.Set(k, strconv.FormatInt(int64(v), 10))
			}
		}
		if path := req.GetString("save_to", ""); path != "" {
			return d.saveTrades(ctx, q, path), nil
		}
		limit, offset := pageArgs(req, defaultTradesLimit, maxTradesLimit)
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(offset))
		var page tradesPage
		if err := d.getResearch(ctx, "get_trades", "/api/v1/dev/analytics/trades", q, &page); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderTrades(&page, q.Get("sort"))), nil
	})
}

// getResearch is getJSON for the report-version-2 endpoints, which an older
// analytics_service does not have: its router answers a plain 404, where a
// missing session is a JSON one.
func (d Deps) getResearch(ctx context.Context, name, path string, q url.Values, out any) error {
	body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodGet, client.Analytics, path, q, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if status == http.StatusNotFound && !bytes.Contains(body, []byte(`"error"`)) {
		return fmt.Errorf("%s: this platform's analytics_service has no %s yet (it needs report version 2); get_session_report and get_session_orders still work", name, strings.TrimPrefix(path, "/api/v1/dev"))
	}
	if status < 200 || status >= 300 {
		return httpErr(name, status, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", name, err)
	}
	return nil
}

func renderTrades(p *tradesPage, sortBy string) string {
	doc := render.New()
	if p.Total == 0 {
		doc.Line("No trades match.")
		return doc.String()
	}
	if sortBy == "" {
		sortBy = "entry_at"
	}
	doc.Line("Trades %d–%d of %d, by %s; times UTC; R is net over the risk to the initial stop.",
		min(p.Offset+1, p.Total), p.Offset+len(p.Trades), p.Total, sortBy)
	high := 0
	for _, w := range p.Warnings {
		if w.Severity == "high" {
			doc.Line("[high] %s", w.Message)
			high++
		}
	}
	if n := len(p.Warnings) - high; n > 0 {
		doc.Line("%d more warning(s) in get_session_report.", n)
	}

	rows := make([][]string, 0, len(p.Trades))
	for _, t := range p.Trades {
		rows = append(rows, []string{
			render.Int(t.ID), render.Time(t.EntryAt), render.OptTime(t.ExitAt), t.Exchange + " " + t.Pair, t.Side,
			render.Px(t.EntryPx), render.OptPx(t.ExitPx), render.Px(t.QtyPeak), fmt.Sprintf("%d/%d", t.Adds, t.Partials),
			render.Signed(t.Gross, 2), render.Num(t.Fees, 2), render.Num(t.Funding, 2), render.Signed(t.Net, 2),
			render.OptPx(t.StopPx), signedOpt(t.NetR, 2), t.ExitKind, hold(t.HoldSec), flags(&t),
		})
	}
	doc.Table("", []string{"id", "entry", "exit", "instrument", "side", "entry_px", "exit_px", "peak_qty", "adds/partials", "gross", "fees", "funding", "net", "stop", "net_r", "exit_kind", "hold", "flags"},
		rows, func(shown int) string {
			return fmt.Sprintf("use offset=%d, or save_to for all of them", p.Offset+shown)
		})
	if p.Offset+len(p.Trades) < p.Total && !doc.Truncated() {
		doc.Line("More: use offset=%d, or save_to for all of them.", p.Offset+len(p.Trades))
	}
	return doc.String()
}

func flags(t *trade) string {
	var f []string
	if t.OpenAtEnd {
		// Still open: its net is only what partial closes made and what it
		// cost so far; its value at the last close is apart.
		if t.Unrealized != nil {
			f = append(f, "open(unreal "+render.Signed(*t.Unrealized, 2)+")")
		} else {
			f = append(f, "open")
		}
	}
	if t.ClosedAtEnd {
		f = append(f, "end")
	}
	if t.Liquidated {
		f = append(f, "liq")
	}
	if t.Reconciled != nil && !*t.Reconciled {
		f = append(f, "unrec")
	}
	if t.Inherited {
		f = append(f, "inh")
	}
	return strings.Join(f, ",")
}

var tradeCSVHeader = []string{
	"id", "exchange", "pair", "side", "entry_at", "exit_at", "hold_sec", "entry_px", "exit_px", "qty_entry", "qty_peak",
	"adds", "partials", "gross", "fees", "funding", "net", "stop_px", "final_stop_px", "risk", "net_r", "gross_r", "r_source",
	"exit_kind", "liquidated", "open_at_end", "closed_at_end", "inherited", "reconciled", "entry_order_id", "exit_order_id",
	"unrealized",
}

// saveTrades writes every matching trade to a CSV at path, a page at a time.
func (d Deps) saveTrades(ctx context.Context, q url.Values, path string) *mcp.CallToolResult {
	if err := checkSavePath(path); err != nil {
		return toolErr(err)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(tradeCSVHeader)
	q.Set("limit", strconv.Itoa(maxTradesLimit))
	n, total := 0, 0
	var warnings []reportWarning
	for offset := 0; ; offset += maxTradesLimit {
		q.Set("offset", strconv.Itoa(offset))
		var page tradesPage
		if err := d.getResearch(ctx, "get_trades", "/api/v1/dev/analytics/trades", q, &page); err != nil {
			return toolErr(err)
		}
		for i := range page.Trades {
			_ = w.Write(tradeCSVRow(&page.Trades[i]))
		}
		n += len(page.Trades)
		total, warnings = page.Total, page.Warnings
		if len(page.Trades) == 0 || n >= page.Total {
			break
		}
		if n >= maxSavedTrades {
			return toolErr(fmt.Errorf("more than %d trades match: narrow with exchange/pair or from/to and save in parts", maxSavedTrades))
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return toolErr(err)
	}
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return toolErr(err)
	}
	doc := render.New()
	doc.Line("Saved %d of %d trades (%s) to %s", n, total, sizeOf(buf.Len()), path)
	doc.Line("Columns: %s", strings.Join(tradeCSVHeader, ","))
	doc.Line("Times are RFC 3339 UTC; empty cells are values the record does not have (no exit yet, no stop).")
	for _, w := range warnings {
		doc.Line("[%s] %s", w.Severity, w.Message)
	}
	return toolText(doc.String())
}

func tradeCSVRow(t *trade) []string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	opt := func(v *float64) string {
		if v == nil {
			return ""
		}
		return f(*v)
	}
	exit := ""
	if t.ExitAt != nil {
		exit = t.ExitAt.UTC().Format(time.RFC3339)
	}
	reconciled := ""
	if t.Reconciled != nil {
		reconciled = strconv.FormatBool(*t.Reconciled)
	}
	return []string{
		strconv.Itoa(t.ID), t.Exchange, t.Pair, t.Side, t.EntryAt.UTC().Format(time.RFC3339), exit, strconv.FormatInt(t.HoldSec, 10),
		f(t.EntryPx), opt(t.ExitPx), f(t.QtyEntry), f(t.QtyPeak), strconv.Itoa(t.Adds), strconv.Itoa(t.Partials),
		f(t.Gross), f(t.Fees), f(t.Funding), f(t.Net), opt(t.StopPx), opt(t.FinalStopPx), opt(t.Risk), opt(t.NetR), opt(t.GrossR), t.RSource,
		t.ExitKind, strconv.FormatBool(t.Liquidated), strconv.FormatBool(t.OpenAtEnd), strconv.FormatBool(t.ClosedAtEnd),
		strconv.FormatBool(t.Inherited), reconciled, t.EntryOrderID, t.ExitOrderID,
		opt(t.Unrealized),
	}
}

var breakdownBys = []string{"day", "week", "month", "quarter", "instrument", "side", "weekday", "hour", "exit_kind", "hold"}

func addGetBreakdown(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_breakdown",
		mcp.WithDescription("A session's closed trades grouped, one row per group with trades, win rate, net, gross, fees, funding, profit factor and R (average, standard error, sum, trades with R), and a total. "+
			"by: day, week (from Monday), month, quarter, instrument, side, weekday, hour (UTC), exit_kind or hold (holding time). Period groupings list every period of the window, empty ones too. "+
			"A trade counts where it closed (at=exit) or opened (at=entry; use it to ask when entries work). "+
			"Unlike the months in get_session_report, which follow the equity curve (a fee on the day it was charged), a group books whole trades."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Description("Session type"), mcp.DefaultString("backtest"), mcp.Enum("backtest", "live")),
		mcp.WithString("by", mcp.Required(), mcp.Description("What to group by"), mcp.Enum(breakdownBys...)),
		mcp.WithString("at", mcp.Description("Place a trade by its exit (default) or its entry"), mcp.Enum("exit", "entry")),
		mcp.WithString("exchange", mcp.Description("Only this instrument, with pair")),
		mcp.WithString("pair", mcp.Description("Only this instrument, with exchange")),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		by, err := req.RequireString("by")
		if err != nil {
			return toolErr(err), nil
		}
		q := url.Values{"session_id": {id}, "session_type": {req.GetString("session_type", "backtest")}, "by": {by}}
		for _, k := range []string{"at", "exchange", "pair"} {
			if v := strings.TrimSpace(req.GetString(k, "")); v != "" {
				q.Set(k, v)
			}
		}
		var resp breakdownResponse
		if err := d.getResearch(ctx, "get_breakdown", "/api/v1/dev/analytics/breakdown", q, &resp); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderBreakdown(&resp)), nil
	})
}

func renderBreakdown(b *breakdownResponse) string {
	doc := render.New()
	at := "closing"
	if b.At == "entry" {
		at = "opening"
	}
	doc.Line("Closed trades by %s, each placed by its %s time (UTC); win is the win rate, R is net over the risk to the initial stop.", b.By, at)
	row := func(x *bucket) []string {
		return []string{
			x.Key, render.Int(x.Trades), optShare(x.WinRate), render.Signed(x.Net, 2), render.Signed(x.Gross, 2),
			render.Num(x.Fees, 2), render.Num(x.Funding, 2), render.Opt(x.ProfitFactor, 2),
			signedOpt(x.AvgR, 2), render.Opt(x.RSE, 2), signedOpt(x.SumR, 1), render.Int(x.RTrades),
		}
	}
	rows := make([][]string, 0, len(b.Buckets)+1)
	for i := range b.Buckets {
		rows = append(rows, row(&b.Buckets[i]))
	}
	rows = append(rows, row(&b.Total))
	doc.Table("", []string{b.By, "trades", "win", "net", "gross", "fees", "funding", "pf", "avg_r", "r_se", "sum_r", "r_trades"}, rows,
		func(int) string { return "group by a longer period, or narrow to one instrument" })
	return doc.String()
}
