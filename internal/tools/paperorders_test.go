package tools

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/dev_sdk/orderplan"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

const paperProposals = "/research/api/v1/dev/orders/paper/proposals"

// hostedBackend is backend for the hosted server's tools.
func hostedBackend(t *testing.T, routes map[string]http.HandlerFunc) *server.MCPServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := auth.NewSigner("key-1", hex.EncodeToString(priv))
	require.NoError(t, err)
	s := server.NewMCPServer("test", "0")
	RegisterHosted(s, Deps{HTTP: client.NewHTTP(srv.URL, srv.URL, signer)})
	return s
}

func paperTicket() orderplan.Ticket {
	return orderplan.Ticket{Venue: "binance_futures", Symbol: "BTCUSDT", Side: orderplan.Long, EntryType: orderplan.AtMarket, Qty: 0.151, Stop: 63600, Target: 65900,
		Leverage: 5, Mark: 64260, Notional: 9703.26, Margin: 1940.65, Risk: 99.66, Reward: 247.62, RewardRisk: 2.48, Liquidation: 52050,
		Checks: []orderplan.Check{{Name: "limits", OK: true}}, OK: true, Hash: "h1"}
}

// recordBody keeps what the tool sent and answers.
func recordBody(got *map[string]any, status int, reply any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}
}

// Locally, paper_wallet sends the order to kdraigo with no executor set up.
func TestPaperOrders_LocalPropose(t *testing.T) {
	var got map[string]any
	tk := paperTicket()
	s := backend(t, map[string]http.HandlerFunc{
		paperProposals: recordBody(&got, http.StatusCreated, map[string]any{"id": "p-1", "kind": "open", "status": "pending", "ticket": tk,
			"expires_at": "2026-11-19T15:05:00Z", "paper_account": "w-1", "paper_name": "Swing futures", "executor": "kdraigo-paper"}),
	})
	out, isErr := call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "market", "venue": "binance_futures", "pair": "btcusdt", "paper_wallet": "active"})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]any{"kind": "open", "venue": "binance_futures", "pair": "BTCUSDT", "wallet": "active", "drawing_id": "ai_1", "entry_type": "market"}, got)
	assert.Contains(t, out, "LONG BTCUSDT on binance_futures")
	assert.Contains(t, out, "on the paper wallet Swing futures waits for the user's yes")
	assert.Contains(t, out, "15:05 UTC")
}

// A ticket that fails its checks shows them; nothing is proposed.
func TestPaperOrders_Refused(t *testing.T) {
	var got map[string]any
	tk := paperTicket()
	tk.OK, tk.Checks = false, []orderplan.Check{{Name: "entry near the price", OK: false, Detail: "the drawn entry is 3.4% from the price now"}}
	s := hostedBackend(t, map[string]http.HandlerFunc{
		"/research/api/v1/dev/chart/view": jsonReply(map[string]any{"view": map[string]any{"venue": "binance_futures", "pair": "ETHUSDT"}}),
		paperProposals:                    recordBody(&got, http.StatusUnprocessableEntity, map[string]any{"error": "the order did not pass its checks: entry near the price (the drawn entry is 3.4% from the price now)", "ticket": tk}),
	})
	out, isErr := call(t, s, "order_propose", map[string]any{"drawing_id": "ai_2", "entry_type": "market"})
	assert.True(t, isErr)
	assert.Equal(t, "ETHUSDT", got["pair"], "the shared chart's market")
	assert.NotContains(t, got, "wallet", "hosted: empty is the active wallet")
	assert.Contains(t, out, "Checks: 0 of 1 passed.")
	assert.Contains(t, out, "3.4% from the price now")
	assert.Contains(t, out, "Nothing was proposed.")
}

// The hosted server's actions and reads, on paper.
func TestPaperOrders_HostedActionsAndReads(t *testing.T) {
	var got map[string]any
	act := orderplan.Action{Kind: orderplan.ActMoveStop, Venue: "binance_futures", Symbol: "BTCUSDT", Side: orderplan.Long, Qty: 0.15, Entry: 64000, OldStop: 63600, Stop: 63900, Mark: 64300, PnL: -15,
		Checks: []orderplan.Check{{Name: "stop", OK: true}}, OK: true}
	s := hostedBackend(t, map[string]http.HandlerFunc{
		paperProposals: recordBody(&got, http.StatusCreated, map[string]any{"id": "p-2", "kind": "move_stop", "status": "confirmed", "ticket": act, "paper_name": "Scalps"}),
		"/research/api/v1/dev/orders/paper/wallets": jsonReply(map[string]any{"accounts": []map[string]any{
			{"id": "w-1", "kind": "wallet", "name": "Swing futures", "venue": "binance_futures"}, {"id": "m-1", "kind": "manual", "name": "Paper BTCUSDT", "venue": "binance_futures"}},
			"active": map[string]string{"binance_futures": "w-1"}}),
		"/research/api/v1/dev/orders/paper/positions": jsonReply(map[string]any{"account": map[string]any{"name": "Swing futures"}, "equity": "10184.2",
			"positions": []map[string]any{{"symbol": "BTCUSDT", "side": "LONG", "size": "0.15", "entry_price": "64000", "leverage": 5, "mark": "64300", "unrealized_pnl": "45", "liquidation_price": "51800"}},
			"open_orders": []map[string]any{
				{"id": 41, "symbol": "BTCUSDT", "side": "SELL", "type": "STOP_MARKET", "quantity": "0.15", "stop_price": "63600", "reduce_only": true},
				{"id": 42, "symbol": "BTCUSDT", "side": "SELL", "type": "LIMIT", "quantity": "0.15", "price": "65900", "reduce_only": true},
				{"id": 77, "symbol": "BTCUSDT", "side": "BUY", "type": "LIMIT", "quantity": "0.05", "price": "63000"}}}),
	})
	out, isErr := call(t, s, "order_move_stop", map[string]any{"paper_wallet": "Scalps", "pair": "BTCUSDT", "stop": 63900})
	require.False(t, isErr, out)
	assert.Equal(t, "move_stop", got["kind"])
	assert.Equal(t, 63900.0, got["stop"])
	assert.Equal(t, "Scalps", got["wallet"])
	assert.Contains(t, out, "MOVE the stop")
	assert.Contains(t, out, "confirmed without asking (the user's paper switch")

	out, _ = call(t, s, "paper_wallets", nil)
	assert.Contains(t, out, "Swing futures (wallet: any perpetual) · id w-1 · ACTIVE on perpetual charts")
	assert.Contains(t, out, "Paper BTCUSDT (a market's own paper account)")

	out, _ = call(t, s, "order_positions", nil)
	assert.Contains(t, out, "Paper wallet Swing futures")
	assert.Contains(t, out, "LONG BTCUSDT 0.15 from 64000 at 5x · now 64300 · P&L 45 USDT · stop 63600 · target 65900 · liquidation 51800")
	assert.Contains(t, out, "resting entry 77 on BTCUSDT: LONG 0.05 at 63000")
}

// The take-profit action (the user's ask, 2026-11-19): on a paper wallet
// locally and on the hosted server, worded for paper (it moves in place).
func TestPaperOrders_MoveTarget(t *testing.T) {
	var got map[string]any
	act := orderplan.Action{Kind: orderplan.ActMoveTarget, Venue: "binance_futures", Symbol: "ADAUSDT", Side: orderplan.Short, Qty: 6435, Entry: 0.1554, Target: 0.15, Mark: 0.1552, PnL: 34.749,
		Checks: []orderplan.Check{{Name: "side", OK: true}}, OK: true}
	routes := map[string]http.HandlerFunc{
		paperProposals: recordBody(&got, http.StatusCreated, map[string]any{"id": "p-3", "kind": "move_target", "status": "confirmed", "ticket": act, "paper_name": "Futures wallet"}),
	}
	for _, s := range []*server.MCPServer{backend(t, routes), hostedBackend(t, routes)} {
		out, isErr := call(t, s, "order_move_target", map[string]any{"paper_wallet": "active", "pair": "ADAUSDT", "target": 0.15})
		require.False(t, isErr, out)
		assert.Equal(t, "move_target", got["kind"])
		assert.Equal(t, 0.15, got["target"])
		assert.Equal(t, "binance_futures", got["venue"], "paper wallets trade the paper exchange's perpetuals")
		assert.Contains(t, out, "SET the take-profit of the SHORT ADAUSDT position on binance_futures: none → 0.15")
		assert.Contains(t, out, "The position has no target yet: one is placed")
	}
	tool := hostedBackend(t, routes).GetTool("order_move_target")
	require.NotNil(t, tool)
	assert.Contains(t, tool.Tool.InputSchema.Required, "target")
}

func TestActionTextOnPaper(t *testing.T) {
	m := orderplan.Market{TickSize: 0.1, StepSize: 0.001, Mark: 65000}
	h := orderplan.Held{Side: orderplan.Long, Qty: 0.01, Entry: 64000, Stop: 63000, Target: 67000}
	stop := orderplan.BuildMoveStop("bybit_futures", "BTCUSDT", h, 64000, m, orderplan.Limits{MinStopGap: 0.002})
	assert.Contains(t, actionTextOn(stop, true), "On the paper exchange the stop then works as one OCO with the position's target, if it has one (one filling cancels the other).")
	assert.NotContains(t, actionTextOn(stop, true), "-0", "a stop at entry breaks even (bug #60)")
	assert.Contains(t, actionTextOn(stop, true), "if it fills the position breaks even (before fees)")
	ada := orderplan.Action{Kind: orderplan.ActMoveStop, Symbol: "ADAUSDT", Side: orderplan.Short, OldStop: 0.157, Stop: 0.1554, Mark: 0.15405752, TickSize: 0.0001, PnL: -1e-12}
	assert.Contains(t, actionTextOn(ada, true), "0.157 → 0.1554 (now 0.1541); if it fills the position breaks even", "the price now at the tick")
	ada.Kind, ada.Target, ada.PnL = orderplan.ActMoveTarget, 0.15, 34.749
	assert.Contains(t, actionTextOn(ada, true), "if it fills the position makes +34.75 USDT (before fees)")
	assert.Contains(t, actionTextOn(stop, false), "The new stop is placed before the old one goes.")
	none := orderplan.BuildMoveStop("bybit_futures", "BTCUSDT", orderplan.Held{Side: orderplan.Long, Qty: 0.01, Entry: 64000}, 64000, m, orderplan.Limits{MinStopGap: 0.002})
	assert.Contains(t, actionText(none), "none → 64000")
	assert.Contains(t, actionText(none), "The position has no stop yet: one is placed.")
	assert.Contains(t, actionTextOn(none, true), "The position has no stop yet: one is placed, as one OCO with its target if it has one")
	target := orderplan.BuildMoveTarget("bybit_futures", "BTCUSDT", h, 66000, m, orderplan.Limits{MinStopGap: 0.002})
	assert.Contains(t, actionText(target), "SET the take-profit of the LONG BTCUSDT position on bybit_futures: 67000 → 66000")
	assert.Contains(t, actionText(target), "The new target is placed before the old one goes.")
}
