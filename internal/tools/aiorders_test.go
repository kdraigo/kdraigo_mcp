package tools

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/dev_sdk/orderplan"
)

// Without an orders file the tools say how to set it up, and propose nothing.
func TestOrders_NotSetUp(t *testing.T) {
	s := backend(t, nil)
	out, isErr := call(t, s, "order_setup", nil)
	require.False(t, isErr)
	assert.Contains(t, out, "Orders from your AI are not set up on this computer")
	assert.Contains(t, out, "never ask for a key in the chat")
	assert.Contains(t, out, "risk_per_trade: 0")
	assert.Contains(t, out, "kdraigo never holds your keys")

	out, isErr = call(t, s, "order_propose", map[string]any{"drawing_id": "ai_1", "entry_type": "market"})
	assert.True(t, isErr)
	assert.Contains(t, out, "not set up on this computer: order_setup says how. Nothing was proposed.")
	out, isErr = call(t, s, "order_list", nil)
	assert.True(t, isErr)
	assert.Contains(t, out, "not set up")
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

// The hosted server never offers order tools: the keys are on the user's
// computer (PC first, the user's decision D8).
func TestOrders_NotHosted(t *testing.T) {
	s := server.NewMCPServer("test", "0")
	RegisterHosted(s, Deps{})
	for _, name := range []string{"order_setup", "order_propose", "order_list", "order_withdraw"} {
		assert.Nil(t, s.GetTool(name), name)
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

// The hosted server keeps "a trade plan is a drawing, never an order"; the
// local one says how an order happens there.
func TestOrders_Instructions(t *testing.T) {
	hosted, local := Instructions(true), Instructions(false)
	assert.Contains(t, hosted, "a trade plan is a drawing, never an order.")
	assert.NotContains(t, hosted, "order_propose")
	assert.Contains(t, local, "a trade plan is a drawing. Here an order exists only through order_propose")
	assert.Contains(t, local, "Never ask for exchange keys in the chat.")
	assert.NotContains(t, local, "never an order")
}
