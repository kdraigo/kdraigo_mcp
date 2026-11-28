package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/telemetry"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/orders"
)

// Orders from the user's AI (wiki roadmap/ai_orders.md). On the user's
// exchange account: the local server only; the executor in this process holds
// the exchange keys and places what the user confirms on kdraigo.com. On a
// paper wallet (paperorders.go): kdraigo places it, and the hosted server
// offers those tools too (RegisterPaperOrders).

// AIOrders is this computer's executor, or why there is none.
type AIOrders struct {
	Path     string
	Exec     *orders.Executor
	NotSetUp bool
	Problems []string
	// Remote: the user's order manager on a server places the orders
	// (executor: remote); this computer holds no exchange key. Manager is the
	// one named, empty for the one connected.
	Remote  bool
	Manager string
	Server  orders.HTTPServer
	// Venue is the remote order manager's exchange when orders.yaml names it;
	// empty: the shared chart's.
	Venue string
	// done closes once the executor stopped (placing or standing by).
	done chan struct{}
}

// Telemetry is where the AI orders' live-console session reports, signed
// with the user's kdraigo key (config.yaml).
type Telemetry struct {
	URL, KeyID, PrivateKey string
}

// StartAIOrders reads ~/.kdraigo/orders.yaml and, when it is complete,
// starts the executor's connection to kdraigo, and its live-console session,
// for as long as ctx lives.
func StartAIOrders(ctx context.Context, h *client.HTTP, tel Telemetry, logf func(string, ...any)) *AIOrders {
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
	if cfg.Remote() {
		a.Remote, a.Manager, a.Server, a.Venue = true, cfg.Manager, orders.HTTPServer{HTTP: h}, cfg.Venue
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
	// The journal and the console are read when this process starts placing:
	// a standby takes over what the process that placed before left in them.
	owns := func(ctx context.Context, sessionID string) (bool, error) {
		return consoleOwned(ctx, h, sessionID)
	}
	open := func() (*orders.Journal, *orders.Console, error) {
		j, err := orders.OpenJournal(filepath.Join(dir, "orders-journal.jsonl"))
		if err != nil {
			return nil, nil, errors.New("the orders journal could not be read: " + err.Error())
		}
		console, err := orders.OpenConsole(orders.ConsoleFile(ctx, dir, tel.KeyID, owns), func(sessionID string) telemetry.Publisher {
			return telemetry.NewPublisher(sessionID, tel.URL, tel.KeyID, tel.PrivateKey, cfg.Venue, "", telemetry.WithEnvironment(string(cfg.Environment())))
		})
		if err != nil {
			return nil, nil, errors.New("the AI orders' memory (ai-orders-console.json) could not be read: " + err.Error())
		}
		return j, console, nil
	}
	a.Exec = orders.New(cfg, orders.HTTPServer{HTTP: h}, orders.LiveVenues(cfg, keys), nil, id, logf)
	a.done = make(chan struct{})

	// One executor per account on this computer (wiki bug #76): the first
	// kdraigo-mcp to hold the lock places; the others stand by.
	lock, holder, err := orders.TryLockExecutor(dir, tel.KeyID)
	if err != nil {
		a.Exec = nil
		a.Problems = []string{"this computer's executor lock could not be taken in " + dir + ": " + err.Error()}
		return a
	}
	if lock != nil {
		j, console, err := open()
		if err != nil {
			lock.Release()
			a.Exec = nil
			a.Problems = []string{err.Error()}
			return a
		}
		a.Exec.TakeOver(j, console)
		go func() {
			defer close(a.done)
			a.place(ctx, lock)
		}()
		return a
	}
	a.Exec.StandBy(orders.AccountConsoleFile(dir, tel.KeyID), holder)
	logf("orders: another kdraigo-mcp on this computer (process %d) places AI orders; this one proposes, and takes over if it closes", holder.PID)
	go func() {
		defer close(a.done)
		a.standBy(ctx, dir, tel.KeyID, open, logf)
	}()
	return a
}

// How a standby watches the executor lock, and how long the process that
// places waits, as it closes, for one to take over before it tells the live
// console "stopped" (the process has about two seconds to end).
const (
	standbyPoll  = 150 * time.Millisecond
	handoverWait = 500 * time.Millisecond
)

// place runs the executor while ctx lives, holding the lock; as it stops, it
// lets the lock go and waits a moment for a standby to take over.
func (a *AIOrders) place(ctx context.Context, lock *orders.ExecutorLock) {
	a.Exec.OnStop(func() bool {
		lock.Release()
		return lock.TakenOver(handoverWait)
	})
	a.Exec.Run(ctx)
	lock.Release()
}

// standBy watches the executor lock and takes over when the process that
// places lets it go (its Claude Code session closed).
func (a *AIOrders) standBy(ctx context.Context, dir, keyID string, open func() (*orders.Journal, *orders.Console, error), logf func(string, ...any)) {
	consolePath := orders.AccountConsoleFile(dir, keyID)
	wait := standbyPoll
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = standbyPoll
		lock, holder, err := orders.TryLockExecutor(dir, keyID)
		if err != nil {
			logf("orders: the executor lock: %v (again in 30s)", err)
			wait = 30 * time.Second
			continue
		}
		if lock == nil {
			a.Exec.StandBy(consolePath, holder)
			continue
		}
		j, console, err := open()
		if err != nil {
			lock.Release()
			logf("orders: could not take over the executor: %v (again in 30s)", err)
			wait = 30 * time.Second
			continue
		}
		a.Exec.TakeOver(j, console)
		logf("orders: this kdraigo-mcp places AI orders now (the one before it closed)")
		a.place(ctx, lock)
		return
	}
}

// Wait gives the executor up to d to stop once its context has ended. True
// when it told the live console "stopped": that report is sent in the
// background, so the process gives it a moment before it ends.
func (a *AIOrders) Wait(d time.Duration) bool {
	if a == nil || a.Exec == nil || a.done == nil {
		return false
	}
	select {
	case <-a.done:
	case <-time.After(d):
	}
	return a.Exec.StopReported()
}

// standbyText is order_setup's line in a kdraigo-mcp that stands by: another
// Claude Code session's places the orders (wiki bug #76).
func standbyText(h orders.Holder) string {
	who := "another kdraigo-mcp on this computer (another Claude Code session's)"
	if h.PID > 0 {
		who = fmt.Sprintf("another kdraigo-mcp on this computer (process %d, another Claude Code session's, running since %s UTC)", h.PID, h.Started.UTC().Format("2006-01-02 15:04"))
	}
	return "One executor per computer: " + who + " places the confirmed orders. Orders proposed here go to it once the user confirms, and it checks and places them with the orders file as it read it when it started. This one takes over if that one closes.\n"
}

// consoleOwned asks live_trades whether this key's events go to the session:
// one heartbeat (the executor's first, sent a moment early). Taken, or a
// session nobody has yet: this account's. "Session belongs to another user"
// (403): another account's on this computer (wiki bug #65). No answer: not
// known yet.
func consoleOwned(ctx context.Context, h *client.HTTP, sessionID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	event := map[string]any{"session_id": sessionID, "event_type": "heartbeat",
		"heartbeat": map[string]any{"recorded_at": time.Now().UTC(), "uptime_seconds": 0}}
	raw, status, err := h.Do(ctx, true, client.HeaderStyleStandard, http.MethodPost, client.Live, "/api/v1/telemetry", nil, event)
	switch {
	case err != nil:
		return false, err
	case status == http.StatusForbidden && strings.Contains(string(raw), "another user"):
		return false, nil
	case status >= 500 || status == http.StatusTooManyRequests:
		return false, fmt.Errorf("live_trades answered %d", status)
	}
	return true, nil
}

// RegisterOrders adds the order tools (local server only).
func RegisterOrders(s *server.MCPServer, d Deps) {
	a := d.Orders
	if a == nil {
		a = &AIOrders{Path: orders.DefaultPath(), NotSetUp: true}
	}
	// Paper wallets need no executor: kdraigo places those itself.
	srv := orders.HTTPServer{HTTP: d.HTTP}
	addOrderSetup(s, a)
	addOrderPropose(s, a, srv)
	addOrderListWith(s, srv, true)
	addOrderWithdrawWith(s, srv, true)
	addOrderPositions(s, a, srv)
	addPaperWallets(s, srv)
	addOrderAction(s, a, srv, orders.KindClose, "order_close",
		"Propose closing the position on a contract, all of it, at market (reduce-only: it cannot flip it); once flat its stop and target are taken back. "+
			"The user confirms it on kdraigo.com/chart or in Telegram, like an order. Only when the user asks to close.")
	addOrderAction(s, a, srv, orders.KindCancel, "order_cancel",
		"Propose cancelling a resting limit entry that has not filled (on Bybit its attached stop and target go with it). "+
			"The user confirms it on kdraigo.com/chart or in Telegram. order_positions lists the resting entries and their ids.")
	addOrderAction(s, a, srv, orders.KindMoveTarget, "order_move_target",
		"Propose setting an open position's take-profit, or moving the one it has, to a new price: above the price now for a long (below for a short), at least the user's minimum gap from it. "+
			"The new target is placed before the old one goes. The user confirms it on kdraigo.com/chart or in Telegram.")
	addOrderAction(s, a, srv, orders.KindMoveStop, "order_move_stop",
		"Propose moving an open position's stop to a new price: below the price now for a long (above for a short), at least the user's minimum gap from it, "+
			"before the liquidation. The new stop is placed before the old one goes, so the position is never without one. The user confirms it on kdraigo.com/chart or in Telegram.")
}

// paperWalletLocal is the local tools' paper_wallet: set, the order is on a
// paper wallet instead of the exchange account this computer trades.
const paperWalletLocal = "Set it to act on one of the user's paper wallets instead of their exchange account: \"active\" (the one their perpetual charts trade) or a wallet's name or id (paper_wallets). " +
	"kdraigo places paper orders itself after the user's yes: no exchange key, and this computer may be off. Empty: the exchange account this computer places orders on"

func addOrderPositions(s *server.MCPServer, a *AIOrders, srv orders.HTTPServer) {
	tool := mcp.NewTool("order_positions",
		mcp.WithDescription("What is open on the exchange account this computer places orders on (or, with paper_wallet, on a paper wallet): each position (side, size, entry, the price now, P&L, its stop, the liquidation) "+
			"and the resting entry orders with their ids. Read it before order_close, order_cancel or order_move_stop. Reads only."),
		mcp.WithString("pair", mcp.Description("One contract, e.g. BTCUSDT; empty reads every open position")),
		mcp.WithString("paper_wallet", mcp.Description(paperWalletLocal)),
		readOnly(), local(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if w := req.GetString("paper_wallet", ""); w != "" {
			return paperPositions(ctx, srv, w), nil
		}
		e, why := a.ready()
		if e == nil {
			return mcp.NewToolResultError(strings.Replace(why, "Nothing was proposed.", "Nothing was read.", 1)), nil
		}
		list, err := e.Holdings(ctx, req.GetString("pair", ""))
		if err != nil {
			return toolErr(err), nil
		}
		return toolText(holdingsText(e.Config().Venue, e.Config().Network, list)), nil
	})
}

func holdingsText(venue, network string, list []orders.Holding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "On %s (%s):\n", venue, network)
	if len(list) == 0 {
		b.WriteString("No position is open.\n")
		return b.String()
	}
	for _, h := range list {
		mark := ""
		if h.NotMine {
			mark = " (NOT placed by this computer: another trader uses this account)"
		}
		if p := h.Held; p != nil {
			pnl := (h.Mark - p.Entry) * p.Qty
			if p.Side == orderplan.Short {
				pnl = -pnl
			}
			stop := "no stop found"
			if p.Stop > 0 {
				stop = "stop " + num(p.Stop)
			}
			liq := ""
			if p.Liquidation > 0 {
				liq = " · liquidation " + num(p.Liquidation)
			}
			fmt.Fprintf(&b, "- %s %s %s from %s · now %s · P&L %s USDT · %s%s%s\n",
				strings.ToUpper(string(p.Side)), h.Symbol, num(p.Qty), num(p.Entry), num(h.Mark), num(math.Round(pnl*100)/100), stop, liq, mark)
		} else {
			fmt.Fprintf(&b, "- %s: no position\n", h.Symbol)
		}
		for _, r := range h.Resting {
			fmt.Fprintf(&b, "  resting entry %s: %s %s at %s\n", r.ID, r.Side, num(r.Qty), num(r.Price))
		}
	}
	return b.String()
}

func addOrderAction(s *server.MCPServer, a *AIOrders, srv orders.HTTPServer, kind, name, desc string) {
	opts := []mcp.ToolOption{
		mcp.WithDescription(desc + " With paper_wallet, on a paper wallet: kdraigo carries it out after the user's yes."),
		mcp.WithString("pair", mcp.Description("The contract, e.g. BTCUSDT; empty is the shared chart's")),
		mcp.WithString("paper_wallet", mcp.Description(paperWalletLocal)),
	}
	switch kind {
	case orders.KindCancel:
		opts = append(opts, mcp.WithString("order_id", mcp.Description("The resting entry's id (order_positions); empty when only one rests")))
	case orders.KindMoveStop:
		opts = append(opts, mcp.WithNumber("stop", mcp.Required(), mcp.Description("The new stop's price")))
	case orders.KindMoveTarget:
		opts = append(opts, mcp.WithNumber("target", mcp.Required(), mcp.Description("The new take-profit's price")))
	}
	opts = append(opts, additive(false), local())
	add(s, mcp.NewTool(name, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.GetString("paper_wallet", "") != "" {
			return paperPropose(ctx, srv, req, kind), nil
		}
		if a.Remote {
			return a.actRemote(ctx, kind, req), nil
		}
		e, why := a.ready()
		if e == nil {
			return mcp.NewToolResultError(why), nil
		}
		p, act, err := e.ProposeAction(ctx, orders.ActionInput{
			Kind: kind, Pair: req.GetString("pair", ""), OrderID: req.GetString("order_id", ""), Stop: req.GetFloat("stop", 0), Target: req.GetFloat("target", 0),
		})
		if err != nil {
			var r *orders.ActionRefused
			if errors.As(err, &r) {
				return mcp.NewToolResultError(actionText(r.Action) + "\n" + err.Error()), nil
			}
			return toolErr(err), nil
		}
		var b strings.Builder
		b.WriteString(actionText(*act))
		if p.Status == orders.StatusConfirmed {
			fmt.Fprintf(&b, "\nProposal %s was confirmed without asking (the user's switch on kdraigo.com and auto_allowed here are both on): this computer does it now. order_list shows the result.", p.ID)
		} else {
			fmt.Fprintf(&b, "\nProposal %s waits for the user's yes on kdraigo.com/chart or in Telegram until %s UTC. It is carried out from this computer while this Claude Code session stays open; order_list shows what happened.",
				p.ID, p.ExpiresAt.UTC().Format("15:04"))
		}
		return toolText(b.String()), nil
	})
}

// actionText is the exact action on the exchange account, as the user will
// see it to confirm.
func actionText(a orderplan.Action) string { return actionTextOn(a, false) }

// actionTextOn words it for the exchange account or a paper wallet, where a
// stop or target moves in place.
func actionTextOn(a orderplan.Action, paper bool) string {
	var b strings.Builder
	side := strings.ToUpper(string(a.Side))
	switch a.Kind {
	case orderplan.ActClose:
		fmt.Fprintf(&b, "CLOSE the %s %s position on %s: %s from %s, now %s, P&L %s USDT before fees; at market, then its stop and target are taken back.\n",
			side, a.Symbol, a.Venue, num(a.Qty), num(a.Entry), atTick(a.Mark, a.TickSize), cents(a.PnL))
	case orderplan.ActCancel:
		fmt.Fprintf(&b, "CANCEL the resting %s entry %s on %s %s: %s at %s.\n", side, a.OrderID, a.Venue, a.Symbol, num(a.Qty), num(a.Price))
	case orderplan.ActMoveStop:
		from, how := "none", "The new stop is placed before the old one goes."
		if a.OldStop > 0 {
			from = num(a.OldStop)
		}
		switch {
		case paper && a.OldStop <= 0:
			how = "The position has no stop yet: one is placed, as one OCO with its target if it has one (one filling cancels the other)."
		case paper:
			how = "On the paper exchange the stop then works as one OCO with the position's target, if it has one (one filling cancels the other)."
		case a.OldStop <= 0:
			how = "The position has no stop yet: one is placed."
		}
		fmt.Fprintf(&b, "MOVE the stop of the %s %s position on %s: %s → %s (now %s); if it fills %s. %s\n",
			side, a.Symbol, a.Venue, from, num(a.Stop), atTick(a.Mark, a.TickSize), ifFills(a.PnL), how)
	case orderplan.ActMoveTarget:
		from, how := "none", "The new target is placed before the old one goes."
		if a.OldTarget > 0 {
			from = num(a.OldTarget)
		}
		switch {
		case paper && a.OldTarget <= 0:
			how = "The position has no target yet: one is placed (reduce-only, for all of it), as one OCO with its stop if it has one (one filling cancels the other)."
		case paper:
			how = "On the paper exchange the target then works as one OCO with the position's stop, if it has one (one filling cancels the other)."
		case a.OldTarget <= 0:
			how = "The position has no target yet: one is placed (reduce-only, for all of it)."
		}
		fmt.Fprintf(&b, "SET the take-profit of the %s %s position on %s: %s → %s (now %s); if it fills %s. %s\n",
			side, a.Symbol, a.Venue, from, num(a.Target), atTick(a.Mark, a.TickSize), ifFills(a.PnL), how)
	}
	passed := 0
	for _, c := range a.Checks {
		if c.OK {
			passed++
		}
	}
	fmt.Fprintf(&b, "Checks: %d of %d passed.\n", passed, len(a.Checks))
	return b.String()
}

// ready is the executor, or the text saying why there is none.
func (a *AIOrders) ready() (*orders.Executor, string) {
	switch {
	case a.Remote:
		return nil, "Orders go to your order manager on its server (executor: remote in " + a.Path + "): what it holds is read on the exchange or the Live page for now (order_positions through it comes in the next release). Nothing was done."
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
	if a.Remote {
		who := "the one connected"
		if a.Manager != "" && a.Manager != "active" {
			who = a.Manager
		}
		fmt.Fprintf(&b, "Orders from your AI go to your order manager (%s), a program on a server of yours that holds the exchange key and your limits (executor: remote in %s). This computer holds no exchange key.\n", who, a.Path)
		b.WriteString("order_propose sends a position drawing to it through kdraigo: it builds the exact order on its account's numbers and its limits, and the user confirms it on kdraigo.com/chart or in Telegram; it places it with its stop and target, also while this computer is closed.\n")
		b.WriteString("Through it: order_propose (a position from a drawing), order_close, order_cancel, order_move_stop and order_move_target, each confirmed by the user. order_positions through it comes in the next release: what it holds shows on the Live page and the exchange.\n")
		return b.String()
	}
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
	standby, holder := e.Standby()
	switch {
	case standby:
		b.WriteString(standbyText(holder))
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
	b.WriteString("AI orders show in the live console as their own session, \"AI orders\", while this computer runs. They need an exchange account of their own: an order is refused on an account holding positions or orders this computer did not place, and close, cancel and move stop act only on what AI orders opened.\n")
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

func addOrderPropose(s *server.MCPServer, a *AIOrders, srv orders.HTTPServer) {
	tool := mcp.NewTool("order_propose",
		mcp.WithDescription("Propose a long-position or short-position drawing on the user's chart as an order. This computer sizes it from the user's own limits on the exchange's numbers now, checks it, "+
			"and sends the exact order to kdraigo.com for the user to confirm on the Chart page (5 minutes). It places nothing by itself: the order is placed from this computer only after the user's yes. "+
			"Only when the user asked for an order; ask them market or limit if they did not say, and whether it is for their exchange account or a paper wallet (paper_wallet). chart_view lists the drawings and their ids."),
		mcp.WithString("drawing_id", mcp.Required(), mcp.Description("The position drawing's id, from chart_view")),
		mcp.WithString("entry_type", mcp.Required(), mcp.Enum("market", "limit"), mcp.Description("How the user chose to enter: at market now, or a limit at the drawing's entry")),
		mcp.WithString("venue", mcp.Description("The drawing's chart venue; empty is the shared chart's")),
		mcp.WithString("pair", mcp.Description("The drawing's pair, e.g. BTCUSDT; empty is the shared chart's")),
		mcp.WithNumber("risk", mcp.Description("USDT the user wants to risk on this one, below their limit; empty uses the limit")),
		mcp.WithString("paper_wallet", mcp.Description(paperWalletLocal)),
		additive(false), local(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.GetString("paper_wallet", "") != "" {
			return paperPropose(ctx, srv, req, orders.KindOpen), nil
		}
		if a.Remote {
			return a.proposeRemote(ctx, req), nil
		}
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

func addOrderListWith(s *server.MCPServer, srv orders.HTTPServer, onlyHere bool) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("The user's recent order proposals from their AI, newest first, on their exchange account and their paper wallets: waiting, confirmed, rejected, expired, " +
			"placed (with what reached the exchange) or failed (and why). Reads only."),
		mcp.WithNumber("limit", mcp.Description("How many: 1–50, default 10")),
		readOnly(),
	}
	if onlyHere {
		opts = append(opts, local())
	}
	add(s, mcp.NewTool("order_list", opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := max(1, min(50, req.GetInt("limit", 10)))
		list, err := srv.Proposals(ctx, limit)
		if err != nil {
			return toolErr(err), nil
		}
		if len(list) == 0 {
			return toolText("No order proposals yet."), nil
		}
		var b strings.Builder
		for _, p := range list {
			what := p.EntryType
			if p.IsAction() {
				what = p.Kind
			}
			fmt.Fprintf(&b, "%s · %s · %s %s %s (%s) · %s", p.ID, p.CreatedAt.UTC().Format("2006-01-02 15:04"), strings.ToUpper(p.Side), p.Pair, p.Venue, what, p.Status)
			if p.PaperName != nil {
				fmt.Fprintf(&b, " · paper wallet %s", *p.PaperName)
			}
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
		Result      *orderexec.Result       `json:"result"`
		Action      *orderexec.ActionResult `json:"action"`
		Error       string                  `json:"error"`
		Unprotected bool                    `json:"unprotected"`
		Paper       bool                    `json:"paper"`
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
			if r.Paper {
				parts = append(parts, "stop and target held by the paper exchange")
			} else {
				parts = append(parts, "stop and target on the exchange")
			}
		}
		if r.Result.Flattened {
			parts = append(parts, "closed at market")
		}
	}
	if a := r.Action; a != nil {
		if a.Filled > 0 {
			parts = append(parts, fmt.Sprintf("closed %s at %s", num(a.Filled), num(a.AvgPrice)))
		}
		if len(a.Cancelled) > 0 {
			parts = append(parts, "taken back: "+strings.Join(a.Cancelled, ", "))
		}
		parts = append(parts, a.Notes...)
	}
	if r.Error != "" {
		parts = append(parts, r.Error)
	}
	return strings.Join(parts, "; ")
}

func addOrderWithdrawWith(s *server.MCPServer, srv orders.HTTPServer, onlyHere bool) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Withdraw an order proposal that still waits for the user, e.g. when the user changed their mind in the chat. A confirmed or placed order is not withdrawn here."),
		mcp.WithString("id", mcp.Required(), mcp.Description("The proposal id")),
		destructive(true),
	}
	if onlyHere {
		opts = append(opts, local())
	}
	add(s, mcp.NewTool("order_withdraw", opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := srv.Withdraw(ctx, req.GetString("id", ""))
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

// atTick is a price on the contract's tick (the price now is not on it).
func atTick(v, tick float64) string {
	if tick <= 0 {
		return num(v)
	}
	return num(math.Round(v/tick) * tick)
}

// cents is money to the cent with its sign: +34.75, -10.30, 0.00 (never -0).
func cents(v float64) string {
	c := math.Round(v * 100)
	if c == 0 {
		return "0.00"
	}
	return fmt.Sprintf("%+.2f", c/100)
}

// ifFills is what the position makes if a stop or target fills, before fees.
func ifFills(pnl float64) string {
	if math.Round(pnl*100) == 0 {
		return "the position breaks even (before fees)"
	}
	return "the position makes " + cents(pnl) + " USDT (before fees)"
}

var _ = time.Second

// proposeRemote sends a drawing to the user's order manager (executor:
// remote) and says what it answered: the exact order waiting for the user,
// or why it refused.
func (a *AIOrders) proposeRemote(ctx context.Context, req mcp.CallToolRequest) *mcp.CallToolResult {
	r, err := a.Server.ProposePlan(ctx, orders.PlanInput{
		Venue: req.GetString("venue", ""), Pair: strings.ToUpper(req.GetString("pair", "")),
		DrawingID: req.GetString("drawing_id", ""), EntryType: req.GetString("entry_type", ""),
		Risk: req.GetFloat("risk", 0), Manager: a.Manager,
	})
	if err != nil {
		return toolErr(err)
	}
	var doc struct {
		Error  string            `json:"error"`
		Ticket *orderplan.Ticket `json:"ticket"`
	}
	_ = json.Unmarshal(r.Result, &doc)
	switch r.Status {
	case orders.RequestProposed:
		if r.Proposal == nil {
			return toolText("Your order manager proposed it; order_list shows the proposal.")
		}
		var b strings.Builder
		if t, err := r.Proposal.OpenTicket(); err == nil {
			b.WriteString(ticketText(t))
		}
		if r.Proposal.Status == orders.StatusConfirmed {
			fmt.Fprintf(&b, "\nProposal %s was confirmed without asking (the user's switch on kdraigo.com and auto_allowed on the order manager are both on): the order manager places it now. order_list shows the result.", r.Proposal.ID)
		} else {
			fmt.Fprintf(&b, "\nProposal %s, built by your order manager, waits for the user's yes on kdraigo.com/chart until %s UTC. Tell them to check the exact order there. The order manager places it, also while this computer is closed; order_list shows what happened.",
				r.Proposal.ID, r.Proposal.ExpiresAt.UTC().Format("15:04"))
		}
		return toolText(b.String())
	case orders.RequestRefused:
		msg := "Your order manager refused it, so nothing was proposed"
		if doc.Error != "" {
			msg += ":\n" + doc.Error
		}
		if doc.Ticket != nil {
			msg = ticketText(*doc.Ticket) + "\n" + msg
		}
		return mcp.NewToolResultError(msg)
	case orders.RequestFailed:
		return mcp.NewToolResultError("Your order manager could not build it: " + doc.Error + ". Nothing was proposed.")
	case orders.RequestExpired:
		return mcp.NewToolResultError("Your order manager did not answer within a minute (is it running?). Nothing was proposed.")
	}
	return toolText(fmt.Sprintf("Your order manager has not answered yet (request %s, until %s UTC): order_list shows the proposal once it does.", r.ID, r.ExpiresAt.UTC().Format("15:04")))
}

// actRemote asks the user's order manager (executor: remote) to build and
// propose an action on what it holds.
func (a *AIOrders) actRemote(ctx context.Context, kind string, req mcp.CallToolRequest) *mcp.CallToolResult {
	venue, pair := a.Venue, strings.ToUpper(req.GetString("pair", ""))
	if venue == "" || pair == "" {
		cv, cp, err := a.Server.ChartMarket(ctx)
		if err != nil {
			return mcp.NewToolResultError("Name the pair, or share the chart on it: " + err.Error())
		}
		if venue == "" {
			venue = cv
		}
		if pair == "" {
			pair = cp
		}
	}
	r, err := a.Server.ProposeActionPlan(ctx, orders.ActionPlanInput{
		Kind: kind, Venue: venue, Pair: pair, OrderID: req.GetString("order_id", ""),
		Stop: req.GetFloat("stop", 0), Target: req.GetFloat("target", 0), Manager: a.Manager,
	})
	if err != nil {
		return toolErr(err)
	}
	var doc struct {
		Error  string            `json:"error"`
		Action *orderplan.Action `json:"action"`
	}
	_ = json.Unmarshal(r.Result, &doc)
	switch r.Status {
	case orders.RequestProposed:
		var b strings.Builder
		if doc.Action != nil {
			b.WriteString(actionText(*doc.Action))
		}
		if r.Proposal != nil && r.Proposal.Status == orders.StatusConfirmed {
			fmt.Fprintf(&b, "\nProposal %s was confirmed without asking: the order manager does it now. order_list shows the result.", r.Proposal.ID)
		} else if r.Proposal != nil {
			fmt.Fprintf(&b, "\nProposal %s, built by your order manager, waits for the user's yes on kdraigo.com/chart or in Telegram until %s UTC. The order manager carries it out, also while this computer is closed; order_list shows what happened.",
				r.Proposal.ID, r.Proposal.ExpiresAt.UTC().Format("15:04"))
		}
		return toolText(b.String())
	case orders.RequestRefused:
		msg := "Your order manager refused it, so nothing was proposed"
		if doc.Error != "" {
			msg += ": " + doc.Error
		}
		if doc.Action != nil {
			msg = actionText(*doc.Action) + "\n" + msg
		}
		return mcp.NewToolResultError(msg)
	case orders.RequestFailed:
		return mcp.NewToolResultError("Your order manager could not build it: " + doc.Error + ". Nothing was proposed.")
	case orders.RequestExpired:
		return mcp.NewToolResultError("Your order manager did not answer within a minute (is it running?). Nothing was proposed.")
	}
	return toolText(fmt.Sprintf("Your order manager has not answered yet (request %s): order_list shows the proposal once it does.", r.ID))
}
