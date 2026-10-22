package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

const (
	defaultOrdersLimit = 100
	maxOrdersLimit     = 1000
	// maxReasonOrders bounds a page that carries reasons and logs, which can
	// run to a kilobyte an order.
	maxReasonOrders = 50
)

func addGetSessionOrders(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_session_orders",
		mcp.WithDescription("A backtest session's orders, newest first, filtered and a page at a time: when placed and executed, instrument, side, type, role, status, size, price, stop trigger, fill and fee, and the bracket group. "+
			"role is what the order was for: market, limit, stop_loss, take_profit or liquidation. "+
			fmt.Sprintf("with_reason adds the strategy's own reason and log lines for each order (at most %d orders a page then). ", maxReasonOrders)+
			"save_to writes every matching order to a local CSV instead of returning rows (with reason and logs too when with_reason is set); use it for anything you want to analyse in full."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("exchange", mcp.Description("Only this exchange, as the session's instruments name it")),
		mcp.WithString("pair", mcp.Description("Only this pair, e.g. BTC/USDT")),
		mcp.WithString("status", mcp.Description("Only these statuses, comma-separated, e.g. FILLED or CANCELED,EXPIRED")),
		mcp.WithString("role", mcp.Description("Only these roles, comma-separated: market, limit, stop_loss, take_profit, liquidation")),
		mcp.WithNumber("from", mcp.Description("At or after this time, Unix milliseconds")),
		mcp.WithNumber("to", mcp.Description("Before this time, Unix milliseconds")),
		mcp.WithString("time", mcp.Description("Which time from/to filter on: placed (default) or event (when it filled or was canceled)"), mcp.Enum("placed", "event")),
		mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Rows to return, at most %d", maxOrdersLimit)), mcp.DefaultNumber(defaultOrdersLimit)),
		mcp.WithNumber("offset", mcp.Description("Rows to skip"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("with_reason", mcp.Description("Add each order's reason and logs")),
		mcp.WithString("save_to", mcp.Description("Absolute path of a .csv file to write every matching order to; its folder must exist, and an existing file there is replaced. Ignores limit and offset.")),
		additive(true),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		q, err := orderFilters(req)
		if err != nil {
			return toolErr(err), nil
		}
		if path := req.GetString("save_to", ""); path != "" {
			return d.saveOrders(ctx, id, q, path, req.GetBool("with_reason", false)), nil
		}

		limit, offset := pageArgs(req, defaultOrdersLimit, maxOrdersLimit)
		withReason := req.GetBool("with_reason", false)
		note := ""
		if withReason && limit > maxReasonOrders {
			note = fmt.Sprintf("with_reason pages are at most %d orders; limit lowered from %d.", maxReasonOrders, limit)
			limit = maxReasonOrders
		}
		q.Set("session_id", id)
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(offset))
		if withReason {
			q.Set("fields", "full")
		} else {
			q.Set("fields", "summary")
		}
		var page ordersPage
		if err := d.getJSON(ctx, "get_session_orders", client.FrontendAPI, "/api/v1/dev/orders", q, &page); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderOrders(&page, withReason, note)), nil
	})
}

// orderFilters reads the filters /orders and the CSV export share.
func orderFilters(req mcp.CallToolRequest) (url.Values, error) {
	q := url.Values{}
	for _, k := range []string{"exchange", "pair", "status", "role"} {
		if v := strings.TrimSpace(req.GetString(k, "")); v != "" {
			q.Set(k, v)
		}
	}
	for _, k := range []string{"from", "to"} {
		if v := req.GetFloat(k, 0); v > 0 {
			q.Set(k, strconv.FormatInt(int64(v), 10))
		}
	}
	switch t := req.GetString("time", ""); t {
	case "", "placed":
	case "event":
		q.Set("time", t)
	default:
		return nil, fmt.Errorf("time must be placed or event")
	}
	return q, nil
}

func renderOrders(p *ordersPage, withReason bool, note string) string {
	doc := render.New()
	if note != "" {
		doc.Line("%s", note)
	}
	if p.Total == 0 {
		doc.Line("No orders match.")
		return doc.String()
	}
	doc.Line("Orders %d–%d of %d, newest first; times UTC. Role is what the order was for, type how it was sent.",
		min(p.Offset+1, p.Total), p.Offset+len(p.Orders), p.Total)
	cols := []string{"id", "placed", "executed", "exchange", "pair", "side", "type", "role", "status", "amount", "price", "stop", "filled", "avg", "fee", "group"}
	if withReason {
		cols = append(cols, "reason", "logs")
	}
	rows := make([][]string, 0, len(p.Orders))
	for _, o := range p.Orders {
		executed := render.Dash
		if !o.ExecutedAt.IsZero() && o.ExecutedAt.Year() > 1 {
			executed = render.Time(o.ExecutedAt)
		}
		r := []string{
			o.ID, render.Time(o.CreatedAt), executed, o.Exchange, o.Pair, o.Side, o.Type, o.Role, o.Status,
			render.Px(o.Amount), render.Px(o.Price), render.OptPx(o.StopPrice), render.Px(o.FilledQty), render.Px(o.AvgPrice),
			render.Px(o.Fee), o.GroupID,
		}
		if withReason {
			r = append(r, compactJSON(o.Reason, 400), render.Clip(strings.Join(o.Logs, " | "), 400))
		}
		rows = append(rows, r)
	}
	doc.Table("", cols, rows, func(shown int) string {
		return fmt.Sprintf("use offset=%d, or save_to for all of them", p.Offset+shown)
	})
	if p.Offset+len(p.Orders) < p.Total && !doc.Truncated() {
		doc.Line("More: use offset=%d, or save_to for all of them.", p.Offset+len(p.Orders))
	}
	return doc.String()
}

func compactJSON(v map[string]any, n int) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return render.Dash
	}
	return render.Clip(string(b), n)
}

// maxSavedOrders bounds one export: 200 pages of /orders.
const maxSavedOrders = 200 * maxOrdersLimit

var orderCSVHeader = []string{
	"id", "placed_at", "executed_at", "exchange", "pair", "side", "type", "role", "status",
	"amount", "price", "stop_price", "filled_qty", "avg_price", "fee", "fee_asset", "group_id",
}

// saveOrders writes every matching order to a CSV at path, a page of /orders
// at a time. The server's own orders.csv leaves out the role, stop trigger and
// bracket group, which are what a trade's risk is worked out from. The file is
// written beside its destination and renamed into place, so a failed export
// never leaves half a file behind.
func (d Deps) saveOrders(ctx context.Context, id string, q url.Values, path string, withReason bool) *mcp.CallToolResult {
	if err := checkSavePath(path); err != nil {
		return toolErr(err)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := orderCSVHeader
	if withReason {
		header = append(append([]string{}, header...), "reason", "logs")
	}
	_ = w.Write(header)

	q.Set("session_id", id)
	q.Set("limit", strconv.Itoa(maxOrdersLimit))
	q.Set("fields", "summary")
	if withReason {
		q.Set("fields", "full")
	}
	// Pages come newest first. While a run is still writing, a new order
	// shifts every offset by one, so the same order can turn up on two pages.
	var all []order
	seen := map[string]bool{}
	for offset := 0; ; offset += maxOrdersLimit {
		q.Set("offset", strconv.Itoa(offset))
		var page ordersPage
		if err := d.getJSON(ctx, "get_session_orders", client.FrontendAPI, "/api/v1/dev/orders", q, &page); err != nil {
			return toolErr(fmt.Errorf("after %d orders: %w", len(all), err))
		}
		for _, o := range page.Orders {
			if !seen[o.ID] {
				seen[o.ID] = true
				all = append(all, o)
			}
		}
		if len(page.Orders) == 0 || offset+len(page.Orders) >= page.Total {
			break
		}
		if offset+maxOrdersLimit >= maxSavedOrders {
			return toolErr(fmt.Errorf("more than %d orders match: narrow with exchange/pair or from/to and save in parts", maxSavedOrders))
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		_ = w.Write(orderCSVRow(&all[i], withReason))
	}
	n := len(all)
	w.Flush()
	if err := w.Error(); err != nil {
		return toolErr(err)
	}
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return toolErr(err)
	}
	return toolText(fmt.Sprintf("Saved %d orders (%s) to %s\nColumns: %s\nOldest first; times are RFC 3339 UTC; executed_at is empty for an order that never filled or was canceled.\n",
		n, sizeOf(buf.Len()), path, strings.Join(header, ",")))
}

func orderCSVRow(o *order, withReason bool) []string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	stop, executed := "", ""
	if o.StopPrice != nil {
		stop = f(*o.StopPrice)
	}
	if o.ExecutedAt.Year() > 1 {
		executed = o.ExecutedAt.UTC().Format(time.RFC3339Nano)
	}
	row := []string{
		o.ID, o.CreatedAt.UTC().Format(time.RFC3339Nano), executed, o.Exchange, o.Pair, o.Side, o.Type, o.Role, o.Status,
		f(o.Amount), f(o.Price), stop, f(o.FilledQty), f(o.AvgPrice), f(o.Fee), o.FeeAsset, o.GroupID,
	}
	if withReason {
		reason := ""
		if len(o.Reason) > 0 {
			b, _ := json.Marshal(o.Reason)
			reason = string(b)
		}
		row = append(row, reason, strings.Join(o.Logs, "\n"))
	}
	return row
}

// checkSavePath accepts an absolute .csv path in a folder that exists.
func checkSavePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("save_to must be an absolute path, got %q", path)
	}
	if !strings.EqualFold(filepath.Ext(path), ".csv") {
		return fmt.Errorf("save_to must name a .csv file, got %q", path)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		return fmt.Errorf("save_to's folder %s does not exist", filepath.Dir(path))
	}
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("save_to %s exists and is not a regular file", path)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sizeOf(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
