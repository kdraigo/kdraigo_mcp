package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/orders"
)

// Orders from the user's AI (wiki roadmap/ai_orders.md): the local server
// only. The executor in this process holds the exchange keys and places what
// the user confirms on kdraigo.com; the hosted server has none of these tools.

// AIOrders is this computer's executor, or why there is none.
type AIOrders struct {
	Path     string
	Exec     *orders.Executor
	NotSetUp bool
	Problems []string
}

// StartAIOrders reads ~/.kdraigo/orders.yaml and, when it is complete,
// starts the executor's connection to kdraigo for as long as ctx lives.
func StartAIOrders(ctx context.Context, h *client.HTTP, logf func(string, ...any)) *AIOrders {
	path := orders.DefaultPath()
	a := &AIOrders{Path: path}
	cfg, err := orders.Load(path)
	switch {
	case errors.Is(err, orders.ErrNotSetUp):
		a.NotSetUp = true
		return a
	case err != nil:
		a.Problems = []string{err.Error()}
		return a
	}
	if p := cfg.Problems(); len(p) > 0 {
		a.Problems = p
		return a
	}
	keys, err := cfg.Keys()
	if err != nil {
		a.Problems = []string{err.Error()}
		return a
	}
	dir := filepath.Dir(path)
	id, err := orders.ExecutorID(dir)
	if err != nil {
		a.Problems = []string{"this computer's executor name could not be kept: " + err.Error()}
		return a
	}
	j, err := orders.OpenJournal(filepath.Join(dir, "orders-journal.jsonl"))
	if err != nil {
		a.Problems = []string{"the orders journal could not be read: " + err.Error()}
		return a
	}
	a.Exec = orders.New(cfg, orders.HTTPServer{HTTP: h}, orders.LiveVenues(cfg, keys), j, id, logf)
	go a.Exec.Run(ctx)
	return a
}

// RegisterOrders adds the order tools (local server only).
func RegisterOrders(s *server.MCPServer, d Deps) {
	a := d.Orders
	if a == nil {
		a = &AIOrders{Path: orders.DefaultPath(), NotSetUp: true}
	}
	addOrderSetup(s, a)
	addOrderPropose(s, a)
	addOrderList(s, a)
	addOrderWithdraw(s, a)
}

// ready is the executor, or the text saying why there is none.
func (a *AIOrders) ready() (*orders.Executor, string) {
	switch {
	case a.Exec != nil:
		return a.Exec, ""
	case a.NotSetUp:
		return nil, "Orders from the user's AI are not set up on this computer: order_setup says how. Nothing was proposed."
	default:
		return nil, "Orders from the user's AI are not ready on this computer (" + strings.Join(a.Problems, "; ") + "): order_setup says what to fix. Nothing was proposed."
	}
}

func addOrderSetup(s *server.MCPServer, a *AIOrders) {
	tool := mcp.NewTool("order_setup",
		mcp.WithDescription("Whether orders from the user's AI can be placed from this computer, and how to set them up: the exchange (its own sub-account), the network, the user's limits, "+
			"and whether kdraigo hears this computer now. kdraigo never holds the exchange keys: they stay in a file here. Read it before order_propose, and when the user asks about AI orders. Reads only."),
		readOnly(), local(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return toolText(a.setupText()), nil
	})
}

func (a *AIOrders) setupText() string {
	var b strings.Builder
	dir := filepath.Dir(a.Path)
	switch {
	case a.NotSetUp:
		fmt.Fprintf(&b, "Orders from your AI are not set up on this computer (no %s).\n\n", a.Path)
		b.WriteString("How it works: you draw (or your AI draws) a long or short position on kdraigo.com/chart; the AI proposes it as an order; this computer checks it on the exchange's numbers against your limits; you confirm the exact order on the Chart page; this computer places it with its stop and target. kdraigo never holds your keys.\n\n")
		b.WriteString("To set up (the user does steps 1–2 themselves; never ask for a key in the chat):\n")
		b.WriteString("1. On Bybit (or Binance), a sub-account of its own, with no bot trading on it: a Unified Trading Account in isolated margin and one-way mode. Start on testnet.\n")
		fmt.Fprintf(&b, "2. An API key for it that can trade and cannot withdraw, saved by the user as %s with two lines, api_key: … and api_secret: …, then chmod 600 on it.\n", filepath.Join(dir, "exchange-key.yaml"))
		fmt.Fprintf(&b, "3. %s from this template, with every limit chosen by the user (none has a default):\n\n%s\n", a.Path, orders.Template)
		b.WriteString("4. Restart Claude Code, then order_setup again. The plan must include AI orders (Plus or Pro), and the Chart page's \"Share with my AI\" must be on.\n")
		return b.String()
	case a.Exec == nil:
		fmt.Fprintf(&b, "Orders from your AI are not ready on this computer. To fix in %s (then restart Claude Code):\n", a.Path)
		for _, p := range a.Problems {
			b.WriteString("- " + p + "\n")
		}
		return b.String()
	}
	e := a.Exec
	c := e.Config()
	on, last := e.Connection()
	fmt.Fprintf(&b, "Orders from your AI: set up (%s).\n", c.Path())
	fmt.Fprintf(&b, "Exchange: %s, %s; this computer is executor %s.\n", c.Venue, c.Network, e.ID())
	if c.Mainnet() && strings.TrimSpace(os.Getenv("KDRAIGO_LIVE_ARMED")) != "1" {
		b.WriteString("Mainnet is not armed: real orders also need KDRAIGO_LIVE_ARMED=1 in this MCP server's environment (Claude Code's MCP settings). Until then every order is refused.\n")
	}
	switch {
	case on:
		b.WriteString("kdraigo hears this computer now: confirmed orders are placed while this Claude Code session stays open.\n")
	case last != "":
		fmt.Fprintf(&b, "kdraigo does not hear this computer now (%s). A confirmation is refused until it does.\n", last)
	default:
		b.WriteString("Connecting to kdraigo.\n")
	}
	l := c.Limits
	fmt.Fprintf(&b, "Limits (the user's): risk %s USDT per trade, positions up to %s USDT, leverage %gx (max %gx), %d open at most, no new position after a %s USDT loss today, a market entry within %s of the price, a limit within %s, a stop at least %s from the entry.\n",
		num(l.RiskPerTrade), num(l.MaxPositionValue), l.Leverage, l.MaxLeverage, l.MaxOpenPositions, num(l.MaxDailyLoss), pct(l.MaxEntryGap), pct(l.MaxLimitGap), pct(l.MinStopGap))
	fmt.Fprintf(&b, "Between the user's yes and placing, the price may move %s and the loss at the stop grow %s; past that the order is not placed.\n", pct(c.Tolerance.PriceDrift), pct(c.Tolerance.RiskSlack))
	if c.AutoAllowed {
		b.WriteString("auto_allowed is on here: with the user's switch on kdraigo.com on too, orders go without asking.\n")
	} else {
		b.WriteString("Every order waits for the user's confirmation on kdraigo.com/chart (auto_allowed is off here).\n")
	}
	if c.Venue != "bybit_futures" {
		b.WriteString("Entries at market only on this exchange: a limit entry needs its stop and target held by the exchange before the fill, which Bybit does.\n")
	}
	return b.String()
}

func addOrderPropose(s *server.MCPServer, a *AIOrders) {
	tool := mcp.NewTool("order_propose",
		mcp.WithDescription("Propose a long-position or short-position drawing on the user's chart as an order. This computer sizes it from the user's own limits on the exchange's numbers now, checks it, "+
			"and sends the exact order to kdraigo.com for the user to confirm on the Chart page (5 minutes). It places nothing by itself: the order is placed from this computer only after the user's yes. "+
			"Only when the user asked for an order; ask them market or limit if they did not say. chart_view lists the drawings and their ids."),
		mcp.WithString("drawing_id", mcp.Required(), mcp.Description("The position drawing's id, from chart_view")),
		mcp.WithString("entry_type", mcp.Required(), mcp.Enum("market", "limit"), mcp.Description("How the user chose to enter: at market now, or a limit at the drawing's entry")),
		mcp.WithString("venue", mcp.Description("The drawing's chart venue; empty is the shared chart's")),
		mcp.WithString("pair", mcp.Description("The drawing's pair, e.g. BTCUSDT; empty is the shared chart's")),
		mcp.WithNumber("risk", mcp.Description("USDT the user wants to risk on this one, below their limit; empty uses the limit")),
		additive(false), local(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		e, why := a.ready()
		if e == nil {
			return mcp.NewToolResultError(why), nil
		}
		p, t, err := e.Propose(ctx, orders.ProposeInput{
			Venue: req.GetString("venue", ""), Pair: strings.ToUpper(req.GetString("pair", "")),
			DrawingID: req.GetString("drawing_id", ""), EntryType: orderplan.EntryType(req.GetString("entry_type", "")),
			Risk: req.GetFloat("risk", 0),
		})
		if err != nil {
			var r *orders.Refused
			if errors.As(err, &r) {
				return mcp.NewToolResultError(ticketText(r.Ticket) + "\n" + err.Error()), nil
			}
			return toolErr(err), nil
		}
		var b strings.Builder
		b.WriteString(ticketText(*t))
		if p.Status == orders.StatusConfirmed {
			fmt.Fprintf(&b, "\nProposal %s was confirmed without asking (the user's switch on kdraigo.com and auto_allowed here are both on): this computer places it now. order_list shows the result.", p.ID)
		} else {
			fmt.Fprintf(&b, "\nProposal %s waits for the user's yes on kdraigo.com/chart until %s UTC. Tell them to check the exact order there. It is placed from this computer while this Claude Code session stays open; order_list shows what happened.",
				p.ID, p.ExpiresAt.UTC().Format("15:04"))
		}
		return toolText(b.String()), nil
	})
}

// ticketText is the exact order, as the user will see it to confirm.
func ticketText(t orderplan.Ticket) string {
	var b strings.Builder
	entry := fmt.Sprintf("at market (now %s)", num(t.Mark))
	if t.EntryType == orderplan.AtLimit {
		entry = "with a limit at " + num(t.Entry)
	}
	fmt.Fprintf(&b, "%s %s on %s, entering %s: %s for %s USDT at %gx (margin %s).\n",
		strings.ToUpper(string(t.Side)), t.Symbol, t.Venue, entry, num(t.Qty), num(t.Notional), t.Leverage, num(t.Margin))
	fmt.Fprintf(&b, "Stop %s (lose %s USDT), target %s (win %s USDT), %.2fR; estimated liquidation %s.\n",
		num(t.Stop), num(t.Risk), num(t.Target), num(t.Reward), t.RewardRisk, num(t.Liquidation))
	passed := 0
	for _, c := range t.Checks {
		if c.OK {
			passed++
		}
	}
	fmt.Fprintf(&b, "Checks: %d of %d passed.\n", passed, len(t.Checks))
	return b.String()
}

func addOrderList(s *server.MCPServer, a *AIOrders) {
	tool := mcp.NewTool("order_list",
		mcp.WithDescription("The user's recent order proposals from their AI, newest first: waiting, confirmed, rejected, expired, placed (with what reached the exchange) or failed (and why). Reads only."),
		mcp.WithNumber("limit", mcp.Description("How many: 1–50, default 10")),
		readOnly(), local(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if a.Exec == nil {
			_, why := a.ready()
			return mcp.NewToolResultError(why), nil
		}
		limit := max(1, min(50, req.GetInt("limit", 10)))
		list, err := a.Exec.Server().Proposals(ctx, limit)
		if err != nil {
			return toolErr(err), nil
		}
		if len(list) == 0 {
			return toolText("No order proposals yet."), nil
		}
		var b strings.Builder
		for _, p := range list {
			fmt.Fprintf(&b, "%s · %s · %s %s %s (%s) · %s", p.ID, p.CreatedAt.UTC().Format("2006-01-02 15:04"), strings.ToUpper(p.Side), p.Pair, p.Venue, p.EntryType, p.Status)
			if p.DecidedVia != nil {
				fmt.Fprintf(&b, " via %s", *p.DecidedVia)
			}
			if line := resultLine(p.Result); line != "" {
				b.WriteString(" · " + line)
			}
			b.WriteString("\n")
		}
		return toolText(b.String()), nil
	})
}

// resultLine is what the executor reported, in a line.
func resultLine(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var r struct {
		Result      *orderexec.Result `json:"result"`
		Error       string            `json:"error"`
		Unprotected bool              `json:"unprotected"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	var parts []string
	if r.Unprotected {
		parts = append(parts, "POSITION OPEN WITHOUT A STOP")
	}
	if r.Result != nil {
		if r.Result.Filled > 0 {
			parts = append(parts, fmt.Sprintf("filled %s at %s", num(r.Result.Filled), num(r.Result.AvgPrice)))
		}
		if r.Result.Protected {
			parts = append(parts, "stop and target on the exchange")
		}
		if r.Result.Flattened {
			parts = append(parts, "closed at market")
		}
	}
	if r.Error != "" {
		parts = append(parts, r.Error)
	}
	return strings.Join(parts, "; ")
}

func addOrderWithdraw(s *server.MCPServer, a *AIOrders) {
	tool := mcp.NewTool("order_withdraw",
		mcp.WithDescription("Withdraw an order proposal that still waits for the user, e.g. when the user changed their mind in the chat. A confirmed or placed order is not withdrawn here."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The proposal id")),
		destructive(true), local(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if a.Exec == nil {
			_, why := a.ready()
			return mcp.NewToolResultError(why), nil
		}
		p, err := a.Exec.Server().Withdraw(ctx, req.GetString("id", ""))
		if err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Proposal %s is %s.", p.ID, p.Status)), nil
	})
}

func num(v float64) string {
	s := fmt.Sprintf("%.8f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func pct(f float64) string { return num(f*100) + "%" }

var _ = time.Second
