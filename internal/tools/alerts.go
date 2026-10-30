package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// RegisterAlerts adds script alerts: one of a chart script's
// alertcondition(…) armed on a pair and timeframe. The platform checks it
// after each closed bar and sends its message to the user's Telegram.
func RegisterAlerts(s *server.MCPServer, d Deps) {
	addAlertList(s, d)
	addAlertCreate(s, d)
	addAlertRenew(s, d)
	addAlertDelete(s, d)
}

type (
	scriptAlert struct {
		ID         string    `json:"id"`
		Script     string    `json:"script"`
		ScriptName string    `json:"script_name"`
		Condition  string    `json:"condition"`
		Exchange   string    `json:"exchange"`
		Symbol     string    `json:"symbol"`
		Timeframe  string    `json:"timeframe"`
		Status     string    `json:"status"`
		Reason     string    `json:"reason"`
		FiredBar   int64     `json:"fired_bar"`
		ExpiresAt  time.Time `json:"expires_at"`
	}
	alertEvent struct {
		Kind      string    `json:"kind"`
		Condition string    `json:"condition"`
		Symbol    string    `json:"symbol"`
		Timeframe string    `json:"timeframe"`
		BarTime   int64     `json:"bar_time"`
		Message   string    `json:"message"`
		Delivery  string    `json:"delivery"`
		CreatedAt time.Time `json:"created_at"`
	}
)

func alertPath(id string) string { return labsAPI + "/alerts/" + url.PathEscape(strings.TrimSpace(id)) }

func alertLine(a scriptAlert) string {
	return fmt.Sprintf("%q on %s %s %s with %s (%s)", a.Condition, a.Symbol, a.Exchange, a.Timeframe, a.ScriptName, a.Script)
}

func addAlertList(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("alert_list",
		mcp.WithDescription("The user's script alerts (active, paused with the reason, expired) and what they did lately: fired with their message, paused, expired."),
		mcp.WithNumber("events", mcp.Description("How many recent events to show (default 20, at most 200)")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var list []scriptAlert
		if err := d.getJSON(ctx, "alert_list", client.Research, labsAPI+"/alerts", nil, &list); err != nil {
			return toolErr(err), nil
		}
		if len(list) == 0 {
			return toolText("No alerts yet. Add alertcondition(cond, \"Title\", message) to a script, script_save it, then alert_create."), nil
		}
		var events []alertEvent
		q := url.Values{"limit": {fmt.Sprint(req.GetInt("events", 20))}}
		if err := d.getJSON(ctx, "alert_list", client.Research, labsAPI+"/alerts/events", q, &events); err != nil {
			return toolErr(err), nil
		}
		rows := make([][]string, 0, len(list))
		for _, a := range list {
			last := ""
			if a.FiredBar > 0 {
				last = msTime(a.FiredBar)
			}
			rows = append(rows, []string{a.ID, a.Script, a.Condition, a.Exchange, a.Symbol, a.Timeframe, a.Status, a.Reason, last, render.Date(a.ExpiresAt)})
		}
		doc := render.New()
		doc.Table("Alerts", []string{"id", "script", "condition", "exchange", "symbol", "timeframe", "status", "reason", "last_fired_bar", "expires"}, rows, nil)
		if len(events) > 0 {
			rows = rows[:0]
			for _, e := range events {
				bar := ""
				if e.BarTime > 0 && e.Kind == "fired" {
					bar = msTime(e.BarTime)
				}
				rows = append(rows, []string{render.Time(e.CreatedAt), e.Kind, e.Condition, e.Symbol, e.Timeframe, bar, e.Message, e.Delivery})
			}
			doc.Blank()
			doc.Table("Recent events", []string{"at", "kind", "condition", "symbol", "timeframe", "bar", "message", "telegram"}, rows, nil)
		}
		return toolText(doc.String()), nil
	})
}

func addAlertCreate(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("alert_create",
		mcp.WithDescription("Arm a script alert: one of a saved script's alertcondition(…) (by its title) on a pair and timeframe. The platform checks it after each closed bar and sends its message to the user's Telegram while it is true; it never fires on a forming bar. It follows the script's newest saved version. Plans: verified 5 active alerts on 15m or longer for 30 days; pro 50 on 5m or longer for 90 days. Tell the user what you armed."),
		mcp.WithString("script", mcp.Required(), mcp.Description("The script: its slug, or syndicate:slug")),
		mcp.WithString("condition", mcp.Required(), mcp.Description("The title of one of its alertcondition(…) calls (script_open lists them)")),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("binance, binance_futures, bybit or bybit_futures")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Pair as BASE/QUOTE, e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("15m, 30m, 1h, 4h, 1d, 1w (pro also 5m)")),
		mcp.WithObject("inputs", mcp.Description(`The script's inputs by name, e.g. {"len": 21}; the defaults otherwise`)),
		additive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body := map[string]any{}
		for _, k := range []string{"script", "condition", "exchange", "symbol", "timeframe"} {
			v, err := req.RequireString(k)
			if err != nil {
				return toolErr(err), nil
			}
			body[k] = v
		}
		inputs, err := objectArg(req, "inputs")
		if err != nil {
			return toolErr(err), nil
		}
		if inputs != nil {
			body["inputs"] = inputs
		}
		var a scriptAlert
		if err := d.sendJSON(ctx, "alert_create", http.MethodPost, client.Research, labsAPI+"/alerts", body, &a); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Armed %s: alert %s, active until %s. It is checked from the next closed bar; alert_list shows what it did.",
			alertLine(a), a.ID, render.Date(a.ExpiresAt))), nil
	})
}

func addAlertRenew(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("alert_renew",
		mcp.WithDescription("Renew a script alert for its plan's lifetime from now: an expired one, or a paused one once its cause is fixed (the script declares the condition again and runs)."),
		mcp.WithString("alert", mcp.Required(), mcp.Description("The alert's id (alert_list)")),
		additive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("alert")
		if err != nil {
			return toolErr(err), nil
		}
		var a scriptAlert
		if err := d.sendJSON(ctx, "alert_renew", http.MethodPost, client.Research, alertPath(id)+"/renew", nil, &a); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Renewed %s: active until %s.", alertLine(a), render.Date(a.ExpiresAt))), nil
	})
}

func addAlertDelete(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("alert_delete",
		mcp.WithDescription("Delete a script alert and its history."),
		mcp.WithString("alert", mcp.Required(), mcp.Description("The alert's id (alert_list)")),
		destructive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("alert")
		if err != nil {
			return toolErr(err), nil
		}
		if err := d.sendJSON(ctx, "alert_delete", http.MethodDelete, client.Research, alertPath(id), nil, nil); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Deleted the alert %s.", id)), nil
	})
}
