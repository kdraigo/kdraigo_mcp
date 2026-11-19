package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/dev_sdk/orderplan"

	"github.com/kdraigo/kdraigo_mcp/internal/orders"
)

// Without an orders file the tools say how to set it up, and propose nothing.
func TestOrders_NotSetUp(t *testing.T) {
	s := backend(t, map[string]http.HandlerFunc{"/research/api/v1/dev/orders/proposals": jsonReply(map[string]any{"proposals": []any{}})})
	out, isErr := call(t, s, "order_setup", nil)
	require.False(t, isErr)
	assert.Contains(t, out, "Orders from your AI are not set up on this computer")
	assert.Contains(t, out, "never ask for a key in the chat")
	assert.Contains(t, out, "risk_per_trade: 0")
	assert.Contains(t, out, "kdraigo never holds your keys")

	out, isErr = call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "market"})
	assert.True(t, isErr)
	assert.Contains(t, out, "not set up on this computer: order_setup says how. Nothing was proposed.")
	// The list is read from kdraigo: paper orders need no executor here.
	out, isErr = call(t, s, "order_list", nil)
	assert.False(t, isErr)
	assert.Contains(t, out, "No order proposals yet.")
}

// A file with problems names each one.
func TestOrders_Problems(t *testing.T) {
	s := server.NewMCPServer("test", "0")
	RegisterOrders(s, Deps{Orders: &AIOrders{Path: "/home/u/.kdraigo/orders.yaml", Problems: []string{"limits.risk_per_trade is not set", "key_file k.yaml can be read by other users"}}})
	out, _ := call(t, s, "order_setup", nil)
	assert.Contains(t, out, "not ready on this computer. To fix in /home/u/.kdraigo/orders.yaml (then restart Claude Code):\n- limits.risk_per_trade is not set\n- key_file k.yaml can be read by other users")
	out, isErr := call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "limit"})
	assert.True(t, isErr)
	assert.Contains(t, out, "not ready on this computer (limits.risk_per_trade is not set; key_file k.yaml can be read by other users)")
}

// The hosted server never reaches an exchange account: the keys are on the
// user's computer (PC first, the user's decision D8). It has the order tools
// for paper wallets only (the user's decision, 2026-11-19).
func TestOrders_HostedPaperOnly(t *testing.T) {
	s := server.NewMCPServer("test", "0")
	RegisterHosted(s, Deps{})
	assert.Nil(t, s.GetTool("order_setup"), "no executor to set up")
	for _, name := range []string{"order_propose", "order_positions", "order_close", "order_cancel", "order_move_stop"} {
		tool := s.GetTool(name)
		require.NotNil(t, tool, name)
		assert.True(t, strings.HasPrefix(tool.Tool.Description, "Paper wallets only:"), name)
		assert.Contains(t, tool.Tool.InputSchema.Properties, "paper_wallet", name)
	}
	for _, name := range []string{"order_list", "order_withdraw", "paper_wallets"} {
		assert.NotNil(t, s.GetTool(name), name)
	}
}

func TestTicketText(t *testing.T) {
	tk := orderplan.Ticket{Venue: "bybit_futures", Symbol: "BTCUSDT", Side: orderplan.Long, EntryType: orderplan.AtMarket, Qty: 0.01, Stop: 64000, Target: 67000,
		Leverage: 3, Mark: 65000.5, Notional: 650.01, Margin: 216.67, Risk: 10.01, Reward: 19.99, RewardRisk: 2, Liquidation: 43658.3,
		Checks: []orderplan.Check{{Name: "a", OK: true}, {Name: "b", OK: true}}}
	assert.Equal(t, "LONG BTCUSDT on bybit_futures, entering at market (now 65000.5): 0.01 for 650.01 USDT at 3x (margin 216.67).\n"+
		"Stop 64000 (lose 10.01 USDT), target 67000 (win 19.99 USDT), 2.00R; estimated liquidation 43658.3.\nChecks: 2 of 2 passed.\n", ticketText(tk))
	tk.EntryType, tk.Entry = orderplan.AtLimit, 64500
	assert.Contains(t, ticketText(tk), "entering with a limit at 64500")
}

func TestResultLine(t *testing.T) {
	line := func(v any) string {
		b, _ := json.Marshal(v)
		return resultLine(b)
	}
	assert.Equal(t, "filled 0.01 at 65010; stop and target on the exchange",
		line(map[string]any{"result": map[string]any{"filled": 0.01, "avg_price": 65010, "protected": true}}))
	assert.Equal(t, "POSITION OPEN WITHOUT A STOP; filled 0.01 at 65010; boom",
		line(map[string]any{"unprotected": true, "error": "boom", "result": map[string]any{"filled": 0.01, "avg_price": 65010}}))
	assert.Equal(t, "not placed: the market or the account changed since it was confirmed",
		line(map[string]any{"error": "not placed: the market or the account changed since it was confirmed"}))
	assert.Equal(t, "", resultLine(nil))
}

// Each server says how an order happens there: the hosted one on paper
// wallets only, the local one on the exchange account or a paper wallet.
func TestOrders_Instructions(t *testing.T) {
	hosted, local := Instructions(true), Instructions(false)
	assert.Contains(t, hosted, "a trade plan is a drawing. Here an order exists only on the user's paper wallets (virtual money)")
	assert.Contains(t, hosted, "A real-money order needs the local kdraigo-mcp")
	assert.Contains(t, local, "a trade plan is a drawing. Here an order exists only through order_propose")
	assert.Contains(t, local, "With paper_wallet it is on one of their paper wallets instead")
	for _, s := range []string{hosted, local} {
		assert.Contains(t, s, "Never ask for exchange keys in the chat.")
		assert.NotContains(t, s, "never an order")
	}
}

func TestActionText(t *testing.T) {
	m := orderplan.Market{TickSize: 0.1, StepSize: 0.001, Mark: 65000}
	h := orderplan.Held{Side: orderplan.Long, Qty: 0.01, Entry: 64000, Stop: 63000}
	assert.Equal(t, "CLOSE the LONG BTCUSDT position on bybit_futures: 0.01 from 64000, now 65000, P&L +10.00 USDT before fees; at market, then its stop and target are taken back.\nChecks: 2 of 2 passed.\n",
		actionText(orderplan.BuildClose("bybit_futures", "BTCUSDT", h, m)))
	assert.Equal(t, "MOVE the stop of the LONG BTCUSDT position on bybit_futures: 63000 → 64000 (now 65000); if it fills the position breaks even (before fees). The new stop is placed before the old one goes.\nChecks: 5 of 5 passed.\n",
		actionText(orderplan.BuildMoveStop("bybit_futures", "BTCUSDT", h, 64000, m, orderplan.Limits{MinStopGap: 0.002})))
	assert.Equal(t, "CANCEL the resting LONG entry e-1 on bybit_futures BTCUSDT: 0.02 at 62000.\nChecks: 1 of 1 passed.\n",
		actionText(orderplan.BuildCancel("bybit_futures", "BTCUSDT", orderplan.Resting{ID: "e-1", Side: orderplan.Long, Qty: 0.02, Price: 62000}, m)))
}

func TestHoldingsText(t *testing.T) {
	assert.Equal(t, "On bybit_futures (testnet):\nNo position is open.\n", holdingsText("bybit_futures", "testnet", nil))
	got := holdingsText("bybit_futures", "testnet", []orders.Holding{{Symbol: "BTCUSDT", Mark: 65000,
		Held:    &orderplan.Held{Side: orderplan.Short, Qty: 0.01, Entry: 66000, Stop: 67000, Liquidation: 88000},
		Resting: []orderplan.Resting{{ID: "e-1", Side: orderplan.Short, Qty: 0.02, Price: 67500}}}})
	assert.Equal(t, "On bybit_futures (testnet):\n- SHORT BTCUSDT 0.01 from 66000 · now 65000 · P&L 10 USDT · stop 67000 · liquidation 88000\n  resting entry e-1: short 0.02 at 67500\n", got)
}
