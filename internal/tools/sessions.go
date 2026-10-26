package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// RegisterSessions adds list_sessions, get_session_detail, close_session,
// delete_session, update_session_metadata and the ledgers: orders, positions
// and funding. All call frontend_api/api/v1/dev/* via the kdraigo.com gateway,
// except close_session, which is the engine's.
func RegisterSessions(s *server.MCPServer, d Deps) {
	addListSessions(s, d)
	addGetSessionDetail(s, d)
	addCloseSession(s, d)
	addDeleteSession(s, d)
	addUpdateSessionMetadata(s, d)
	addGetSessionOrders(s, d)
	addGetSessionPositions(s, d)
	addGetSessionFunding(s, d)
}

// Page bounds for the ledgers. frontend_api caps any page at 1000.
const (
	defaultLedgerLimit = 200
	maxLedgerLimit     = 1000
)

func addListSessions(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("list_sessions",
		mcp.WithDescription("The caller's backtest sessions, newest first, one row each: id, name, status, progress, exchanges, first pair, timeframe, created, tags. A multi-instrument session lists only its first pair here; get_session_detail has them all."),
		mcp.WithNumber("limit", mcp.Description("Rows to return, at most 100"), mcp.DefaultNumber(20)),
		mcp.WithNumber("offset", mcp.Description("Rows to skip"), mcp.DefaultNumber(0)),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit, offset := pageArgs(req, 20, 100)
		q := url.Values{}
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(offset))
		var list []sessionSummary
		if err := d.getJSON(ctx, "list_sessions", client.FrontendAPI, "/api/v1/dev/sessions", q, &list); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderSessions(list, limit, offset)), nil
	})
}

func renderSessions(list []sessionSummary, limit, offset int) string {
	doc := render.New()
	if len(list) == 0 {
		doc.Line("No sessions at offset %d.", offset)
		return doc.String()
	}
	doc.Line("Sessions %d–%d, newest first.", offset+1, offset+len(list))
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		progress := render.Dash
		if s.Progress != nil {
			progress = render.Pct(s.Progress.Percent, 0)
		}
		tags := strings.Join(s.Tags, ",")
		if s.IsFavorite {
			tags = strings.TrimPrefix(tags+",★", ",")
		}
		rows = append(rows, []string{
			s.ID, render.Clip(s.Name, 40), s.Status, progress,
			exchanges(s.Exchange, s.Exchanges), s.Pair, s.Timeframe, render.Time(s.CreatedAt), tags,
		})
	}
	doc.Table("", []string{"id", "name", "status", "progress", "exchanges", "first_pair", "tf", "created", "tags"}, rows, offsetFrom(offset))
	if len(list) == limit {
		doc.Line("More may follow: offset=%d.", offset+limit)
	}
	return doc.String()
}

// exchanges names every exchange a session's wallets were on.
func exchanges(first string, all []string) string {
	if len(all) == 0 {
		return first
	}
	return strings.Join(all, "+")
}

// titled is a session's name in quotes, or nothing for an unnamed one.
func titled(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf(" %q", name)
}

func addGetSessionDetail(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_session_detail",
		mcp.WithDescription("A backtest session's record: status, window, progress, wallet balances (at the end of a finished run, current for a running one) and the instruments it placed orders on, with fills, notional and fees per instrument. "+
			"For results (P&L, drawdown, trades, monthly breakdown) call get_session_report; orders, positions and funding have their own tools."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		var det sessionDetail
		if err := d.getJSON(ctx, "get_session_detail", client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id), nil, &det); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderDetail(&det)), nil
	})
}

func renderDetail(s *sessionDetail) string {
	doc := render.New()
	doc.Line("Session %s%s · %s · %s · %d instruments on %s", s.ID, titled(s.Name), s.Status, s.Timeframe, len(s.Instruments), exchanges(s.Exchange, s.Exchanges))
	doc.Line("Window %s → %s UTC · created %s", render.OptTime(s.StartAt), render.OptTime(s.EndAt), render.Time(s.CreatedAt))
	if p := s.Progress; p != nil {
		doc.Line("Progress %d / %d candles (%s), last candle %s", p.ProcessedCandles, p.TotalCandles, render.Pct(p.Percent, 1), render.OptTime(p.LastCandleTime))
	}
	if s.IsFavorite {
		doc.Line("Favorite · tags %s", strings.Join(s.Tags, ", "))
	} else if len(s.Tags) > 0 {
		doc.Line("Tags %s", strings.Join(s.Tags, ", "))
	}
	if s.Notes != "" {
		doc.Line("Notes %s", render.Cell(render.Clip(s.Notes, 500)))
	}

	doc.Blank()
	var rows [][]string
	for _, w := range s.Wallets {
		rows = append(rows, []string{w.Exchange, w.Asset, render.Px(w.Balance), render.Px(w.LockedBalance)})
	}
	doc.Table("wallets: at the end of a finished run, now for a running one", []string{"exchange", "asset", "balance", "locked"}, rows, nil)

	doc.Blank()
	rows = rows[:0]
	for _, in := range s.Instruments {
		rows = append(rows, []string{
			in.Exchange, in.Pair, render.Int(in.OrderCount), render.Int(in.Filled), render.Int(in.Buys), render.Int(in.Sells),
			render.Num(in.Notional, 2), render.Px(in.Fees), render.OptPx(in.AvgBuy), render.OptPx(in.AvgSell),
			render.OptTime(in.FirstFill), render.OptTime(in.LastFill),
		})
	}
	doc.Table("instruments, busiest first", []string{"exchange", "pair", "orders", "filled", "buys", "sells", "notional", "fees", "avg_buy", "avg_sell", "first_fill", "last_fill"}, rows, nil)
	return doc.String()
}

func addDeleteSession(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("delete_session",
		mcp.WithDescription("Delete a session and all of its data. Cannot be undone. A session still running or waiting to resume is closed first, so it stops holding one of the account's backtest slots."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithIdempotentHintAnnotation(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		// A live or parked session would otherwise keep its slot on the engine
		// for the whole resume window after its rows are gone. One the engine
		// no longer holds (404) or that is over (closed false) is fine.
		if _, _, err := d.closeSession(ctx, id); err != nil {
			return toolErr(err), nil
		}
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodDelete, client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id), nil, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("delete_session", status, body)), nil
		}
		return toolText(`{"deleted":true}`), nil
	})
}

func addUpdateSessionMetadata(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("update_session_metadata",
		mcp.WithDescription("Update a session's user-facing metadata (name, notes, tags, favorite). Only fields provided are changed; tags replace the existing list."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("name", mcp.Description("Optional new session name")),
		mcp.WithString("notes", mcp.Description("Optional free-form notes")),
		mcp.WithArray("tags", mcp.Description("Optional tags array"), mcp.WithStringItems()),
		mcp.WithBoolean("favorite", mcp.Description("Optional favorite flag")),
		mcp.WithIdempotentHintAnnotation(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		payload := map[string]any{}
		if v := req.GetString("name", ""); v != "" {
			payload["name"] = v
		}
		if v := req.GetString("notes", ""); v != "" {
			payload["notes"] = v
		}
		if v := req.GetStringSlice("tags", nil); v != nil {
			payload["tags"] = v
		}
		if args := req.GetArguments(); args != nil {
			if _, ok := args["favorite"]; ok {
				// frontend_api's DTO field is is_favorite, not favorite.
				payload["is_favorite"] = req.GetBool("favorite", false)
			}
		}
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodPatch, client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id)+"/metadata", nil, payload)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("update_session_metadata", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}

// ledgerQuery is the scope and page shared by the positions and funding
// ledgers.
func ledgerQuery(req mcp.CallToolRequest) (url.Values, error) {
	limit, offset := pageArgs(req, defaultLedgerLimit, maxLedgerLimit)
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	exchange, pair := req.GetString("exchange", ""), req.GetString("pair", "")
	if (exchange == "") != (pair == "") {
		return nil, fmt.Errorf("exchange and pair go together")
	}
	if exchange != "" {
		q.Set("exchange", exchange)
		q.Set("pair", pair)
	}
	return q, nil
}

func ledgerOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("exchange", mcp.Description("Narrow to one instrument, with pair; as the session's instruments name it, e.g. binance_futures")),
		mcp.WithString("pair", mcp.Description("Narrow to one instrument, with exchange, e.g. BTC/USDT")),
		mcp.WithNumber("limit", mcp.Description("Rows to return, at most 1000"), mcp.DefaultNumber(defaultLedgerLimit)),
		mcp.WithNumber("offset", mcp.Description("Rows to skip"), mcp.DefaultNumber(0)),
		readOnly(),
	}
}

// offsetFrom says where the next page starts once a page's first rows are shown.
func offsetFrom(offset int) func(shown int) string {
	return func(shown int) string { return fmt.Sprintf("use offset=%d", offset+shown) }
}

// moreAfter notes a page that ends before the last row, unless the table was
// already cut short and said so.
func moreAfter(doc *render.Doc, offset, n, total int) {
	if offset+n < total && !doc.Truncated() {
		doc.Line("More: use offset=%d.", offset+n)
	}
}

func addGetSessionPositions(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_session_positions", append([]mcp.ToolOption{
		mcp.WithDescription("Futures positions a session held, oldest first, a page at a time with counts over all of them. Empty for a spot session. " +
			"These are not derivable from the order log: an order records what was requested and filled, never what the position became. " +
			"Posted margin, the size-weighted entry, realized PnL, accrued funding and whether the position was liquidated are all wallet state."),
	}, ledgerOptions()...)...)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		q, err := ledgerQuery(req)
		if err != nil {
			return toolErr(err), nil
		}
		var page positionsPage
		if err := d.getJSON(ctx, "get_session_positions", client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id)+"/positions/page", q, &page); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderPositions(&page)), nil
	})
}

func renderPositions(p *positionsPage) string {
	doc := render.New()
	if p.Total == 0 {
		doc.Line("No positions (a spot session has none).")
		return doc.String()
	}
	doc.Line("Positions %d–%d of %d, oldest first · %d open at the end · %d liquidated",
		min(p.Offset+1, p.Total), p.Offset+len(p.Positions), p.Total, p.Open, p.Liquidated)
	rows := make([][]string, 0, len(p.Positions))
	for _, x := range p.Positions {
		rows = append(rows, []string{
			render.Time(x.OpenedAt), render.OptTime(x.ClosedAt), x.Exchange, x.Pair, x.Side,
			render.Px(x.Size), render.Px(x.EntryPrice), render.Px(x.Leverage), render.Px(x.IsolatedMargin),
			render.Px(x.RealizedPnL), render.Px(x.FundingPaid), yes(x.Liquidated),
		})
	}
	doc.Table("", []string{"opened", "closed", "exchange", "pair", "side", "size", "entry", "lev", "margin", "realized", "funding_paid", "liq"}, rows,
		offsetFrom(p.Offset))
	moreAfter(doc, p.Offset, len(p.Positions), p.Total)
	return doc.String()
}

func addGetSessionFunding(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_session_funding", append([]mcp.ToolOption{
		mcp.WithDescription("The funding ledger for a session, one row per settlement, oldest first, a page at a time with totals over all of it. Empty for a spot session. " +
			"Kept per settlement rather than as a total because over a long hold funding can exceed the trading PnL, and only individual rows can be checked against the venue's published rates. " +
			"Each row carries the signed position size, the mark price and which source that mark came from ('inline' is the venue's own settlement mark; anything else is a stand-in), so the payment can be re-derived rather than taken on trust. " +
			"A positive amount means the position paid."),
	}, ledgerOptions()...)...)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		q, err := ledgerQuery(req)
		if err != nil {
			return toolErr(err), nil
		}
		var page fundingPage
		if err := d.getJSON(ctx, "get_session_funding", client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id)+"/funding/page", q, &page); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderFunding(&page)), nil
	})
}

func renderFunding(p *fundingPage) string {
	doc := render.New()
	if p.Total == 0 {
		doc.Line("No funding settlements (a spot session has none).")
		return doc.String()
	}
	doc.Line("Funding %d–%d of %d settlements, oldest first · paid on %d · net %s (positive = paid)",
		min(p.Offset+1, p.Total), p.Offset+len(p.Payments), p.Total, p.Paid, render.Px(p.AmountTotal))
	rows := make([][]string, 0, len(p.Payments))
	for _, x := range p.Payments {
		rows = append(rows, []string{
			render.Time(x.FundingTime), x.Exchange, x.Pair, render.Px(x.Rate), render.Px(x.PositionSize),
			render.Px(x.MarkPrice), x.MarkSource, render.Px(x.Amount),
		})
	}
	doc.Table("", []string{"time", "exchange", "pair", "rate", "position", "mark", "mark_source", "amount"}, rows,
		offsetFrom(p.Offset))
	moreAfter(doc, p.Offset, len(p.Payments), p.Total)
	return doc.String()
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

func addCloseSession(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("close_session",
		mcp.WithDescription("End a backtest session that is still running or waiting to resume, now. "+
			"A program that was stopped or crashed leaves its session parked for an hour, holding one of the account's backtest slots: "+
			"creating another is refused with too_many_sessions, naming the sessions that hold the slots. Close one of those. "+
			"It ends FAILED (\"stopped by its owner\"), or DONE if it had already run to its end, and keeps its results so far. A session already over is left as it is."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithIdempotentHintAnnotation(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		status, held, err := d.closeSession(ctx, id)
		switch {
		case err != nil:
			return toolErr(err), nil
		case !held:
			return toolText(`{"closed":false,"note":"the engine holds no such session: it is over (see get_session_detail) or never existed"}`), nil
		}
		return toolText(status), nil
	})
}

// closeSession asks the engine to end a session now. held is false when the
// engine no longer holds it (it ended and was dropped, or never was); status
// is the engine's answer otherwise.
func (d Deps) closeSession(ctx context.Context, id string) (status string, held bool, err error) {
	body, code, err := d.HTTP.Do(ctx, true, client.HeaderStyleBacktester, http.MethodPost, client.Backtester, "/api/v1/dev/session/"+url.PathEscape(id)+"/close", nil, nil)
	if err != nil {
		return "", false, err
	}
	switch {
	case code == http.StatusNotFound:
		return "", false, nil
	case code < 200 || code >= 300:
		return "", false, httpErr("close_session", code, body)
	}
	return string(body), true, nil
}
