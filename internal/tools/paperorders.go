package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/orders"
)

// AI orders on paper wallets (Phase 9 of the paper exchange): kdraigo's
// server sizes them from the wallet's own limits, checks them, and places
// them on the paper exchange after the user's yes on kdraigo.com or in
// Telegram, with the stop and target the paper exchange then holds. No
// executor and no exchange key are involved, so the hosted server offers
// them too, for paper wallets only (the user's decision, 2026-11-19).

const paperWalletDesc = "The paper wallet: \"active\" (the one the user's perpetual charts trade) or a wallet's name or id from paper_wallets"

// RegisterPaperOrders adds the order tools for paper wallets only: what the
// hosted server offers in place of the local executor's.
func RegisterPaperOrders(s *server.MCPServer, d Deps) {
	srv := orders.HTTPServer{HTTP: d.HTTP}
	paperOnly := "Paper wallets only: virtual money on kdraigo's paper exchange, never the user's exchange account (that needs the local kdraigo-mcp on their computer). "
	addPaperWallets(s, srv)
	add(s, mcp.NewTool("order_propose",
		mcp.WithDescription(paperOnly+"Propose a long-position or short-position drawing on the user's chart as an order on one of their paper wallets. kdraigo sizes it from the wallet's limits, checks it, "+
			"and shows the exact order on kdraigo.com/chart and in Telegram for the user to confirm (5 minutes); after their yes kdraigo places it with its stop and target. "+
			"Only when the user asked for an order; ask them market or limit if they did not say. chart_view lists the drawings and their ids."),
		mcp.WithString("drawing_id", mcp.Required(), mcp.Description("The position drawing's id, from chart_view")),
		mcp.WithString("entry_type", mcp.Required(), mcp.Enum("market", "limit"), mcp.Description("How the user chose to enter: at market now, or a limit at the drawing's entry")),
		mcp.WithString("paper_wallet", mcp.Description(paperWalletDesc+"; empty is the active one")),
		mcp.WithString("venue", mcp.Description("The drawing's chart venue; empty is the shared chart's")),
		mcp.WithString("pair", mcp.Description("The drawing's pair, e.g. BTCUSDT; empty is the shared chart's")),
		mcp.WithNumber("risk", mcp.Description("USDT the user wants to risk on this one, below the wallet's limit; empty uses the limit")),
		additive(false),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return paperPropose(ctx, srv, req, orders.KindOpen), nil
	})
	paperAction := func(kind, name, desc string) {
		opts := []mcp.ToolOption{
			mcp.WithDescription(paperOnly + desc),
			mcp.WithString("paper_wallet", mcp.Description(paperWalletDesc+"; empty is the active one")),
			mcp.WithString("pair", mcp.Description("The contract, e.g. BTCUSDT; empty is the shared chart's")),
		}
		switch kind {
		case orders.KindCancel:
			opts = append(opts, mcp.WithString("order_id", mcp.Description("The resting entry's id (order_positions); empty when only one rests")))
		case orders.KindMoveStop:
			opts = append(opts, mcp.WithNumber("stop", mcp.Required(), mcp.Description("The new stop's price")))
		}
		opts = append(opts, additive(false))
		add(s, mcp.NewTool(name, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return paperPropose(ctx, srv, req, kind), nil
		})
	}
	paperAction(orders.KindClose, "order_close", "Propose closing the position on a contract, all of it, at market; its stop and target are taken back. The user confirms it on kdraigo.com/chart or in Telegram. Only when the user asks to close.")
	paperAction(orders.KindCancel, "order_cancel", "Propose cancelling a resting limit entry that has not filled, with its attached stop and target. The user confirms it on kdraigo.com/chart or in Telegram. order_positions lists the resting entries and their ids.")
	paperAction(orders.KindMoveStop, "order_move_stop", "Propose moving an open position's stop to a new price: below the price now for a long (above for a short), at least the wallet's minimum gap from it, before the liquidation. It moves in place, keeping its OCO with the target. The user confirms it on kdraigo.com/chart or in Telegram.")
	add(s, mcp.NewTool("order_positions",
		mcp.WithDescription(paperOnly+"What is open on a paper wallet: each position (side, size, entry, the price now, P&L, its stop and target, the liquidation) and the resting entries with their ids. Read it before order_close, order_cancel or order_move_stop. Reads only."),
		mcp.WithString("paper_wallet", mcp.Description(paperWalletDesc+"; empty is the active one")),
		readOnly(),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return paperPositions(ctx, srv, req.GetString("paper_wallet", "")), nil
	})
	addOrderListWith(s, srv, false)
	addOrderWithdrawWith(s, srv, false)
}

func addPaperWallets(s *server.MCPServer, srv orders.HTTPServer) {
	add(s, mcp.NewTool("paper_wallets",
		mcp.WithDescription("The user's futures paper wallets on kdraigo's paper exchange (virtual money on the real order books) and which one is active on their perpetual charts. "+
			"An order on a paper wallet (order_propose with paper_wallet) is placed by kdraigo after the user's yes, from any device. The user makes wallets on kdraigo.com/paper. Reads only."),
		readOnly(),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		w, err := srv.PaperWallets(ctx)
		if err != nil {
			return toolErr(err), nil
		}
		if len(w.Accounts) == 0 {
			return toolText("The user has no futures paper wallet yet: they make one on kdraigo.com/paper (or from the Chart page's Paper trading panel)."), nil
		}
		var b strings.Builder
		for _, a := range w.Accounts {
			tag := ""
			if w.Active["binance_futures"] == a.ID {
				tag = " · ACTIVE on perpetual charts"
			}
			kind := "wallet: any perpetual"
			if a.Kind == "manual" {
				kind = "a market's own paper account"
			}
			fmt.Fprintf(&b, "- %s (%s) · id %s%s\n", a.Name, kind, a.ID, tag)
		}
		if w.Active["binance_futures"] == "" {
			b.WriteString("No wallet is active on perpetual charts: name one with paper_wallet, or the user picks one with the wallet chip on the chart.\n")
		}
		return toolText(b.String()), nil
	})
}

// paperPropose sends an open (from a drawing) or an action on a paper wallet.
func paperPropose(ctx context.Context, srv orders.HTTPServer, req mcp.CallToolRequest, kind string) *mcp.CallToolResult {
	venue, pair := req.GetString("venue", ""), strings.ToUpper(req.GetString("pair", ""))
	if venue == "" && kind != orders.KindOpen {
		venue = "binance_futures" // paper wallets trade the paper exchange's USD-M perpetuals
	}
	if venue == "" || pair == "" {
		v, p, err := srv.ChartMarket(ctx)
		if err != nil {
			return toolErr(err)
		}
		if venue == "" {
			venue = v
		}
		if pair == "" {
			pair = p
		}
	}
	in := orders.PaperInput{Kind: kind, Venue: venue, Pair: pair, Wallet: req.GetString("paper_wallet", "")}
	switch kind {
	case orders.KindOpen:
		in.DrawingID, in.EntryType, in.Risk = req.GetString("drawing_id", ""), req.GetString("entry_type", ""), req.GetFloat("risk", 0)
	case orders.KindCancel:
		in.OrderID = req.GetString("order_id", "")
	case orders.KindMoveStop:
		in.Stop = req.GetFloat("stop", 0)
	}
	p, err := srv.ProposePaper(ctx, in)
	if err != nil {
		var r *orders.PaperRefusal
		if errors.As(err, &r) {
			text := r.Message
			switch {
			case r.Ticket != nil:
				text = ticketText(*r.Ticket) + "\n" + r.Message
			case r.Action != nil:
				text = actionText(*r.Action) + "\n" + r.Message
			}
			return mcp.NewToolResultError(text + "\nNothing was proposed.")
		}
		return toolErr(err)
	}
	var b strings.Builder
	if p.IsAction() {
		if a, err := p.Action(); err == nil {
			b.WriteString(actionText(a))
		}
	} else if t, err := p.OpenTicket(); err == nil {
		b.WriteString(ticketText(t))
	}
	wallet := "the paper wallet"
	if p.PaperName != nil {
		wallet = "the paper wallet " + *p.PaperName
	}
	if p.Status == orders.StatusConfirmed {
		fmt.Fprintf(&b, "\nProposal %s on %s was confirmed without asking (the user's paper switch on kdraigo.com is on): kdraigo places it now. order_list shows the result.", p.ID, wallet)
	} else {
		fmt.Fprintf(&b, "\nProposal %s on %s waits for the user's yes on kdraigo.com/chart or in Telegram until %s UTC. Tell them to check the exact order there. kdraigo places it on the paper exchange after their yes, whatever device they use; the paper exchange then holds its stop and target. order_list shows what happened.",
			p.ID, wallet, p.ExpiresAt.UTC().Format("15:04"))
	}
	return toolText(b.String())
}

func paperPositions(ctx context.Context, srv orders.HTTPServer, wallet string) *mcp.CallToolResult {
	v, err := srv.PaperPositions(ctx, wallet)
	if err != nil {
		return toolErr(err)
	}
	f := func(n interface{ Float64() (float64, error) }) float64 {
		x, _ := n.Float64()
		return x
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Paper wallet %s on the paper exchange (Binance USD-M), equity %s USDT:\n", v.Account.Name, num(math.Round(f(v.Equity)*100)/100))
	if len(v.Positions) == 0 {
		b.WriteString("No position is open.\n")
	}
	stopOf := func(sym, exit string) (string, string) {
		stop, target := "no stop", "no target"
		for _, o := range v.Orders {
			if o.Symbol != sym || !o.ReduceOnly || o.Side != exit {
				continue
			}
			switch {
			case strings.HasPrefix(o.Type, "STOP") && o.StopPrice != nil:
				stop = "stop " + num(f(*o.StopPrice))
			case o.Type == "LIMIT" && o.Price != nil:
				target = "target " + num(f(*o.Price))
			}
		}
		return stop, target
	}
	for _, p := range v.Positions {
		exit := "SELL"
		if p.Side == "SHORT" {
			exit = "BUY"
		}
		stop, target := stopOf(p.Symbol, exit)
		fmt.Fprintf(&b, "- %s %s %s from %s at %dx · now %s · P&L %s USDT · %s · %s · liquidation %s\n",
			p.Side, p.Symbol, num(f(p.Size)), num(f(p.Entry)), p.Leverage, num(f(p.Mark)), num(math.Round(f(p.UnrealizedPnL)*100)/100), stop, target, num(f(p.LiquidationPrice)))
	}
	for _, o := range v.Orders {
		if o.ReduceOnly || o.Type != "LIMIT" || o.Price == nil {
			continue
		}
		side := "LONG"
		if o.Side == "SELL" {
			side = "SHORT"
		}
		fmt.Fprintf(&b, "  resting entry %d on %s: %s %s at %s\n", o.ID, o.Symbol, side, num(f(o.Qty)), num(f(*o.Price)))
	}
	return toolText(b.String())
}
