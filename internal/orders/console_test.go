package orders

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/telemetry"
	"github.com/kdraigo/dev_sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publisher is live_trades, as the console reports to it.
type publisher struct {
	mu       sync.Mutex
	session  string
	orders   []string
	meta     int
	initial  int
	balances int
	beats    int
	stopped  []string
}

func (p *publisher) PublishOrder(o *types.Order, _ map[string]any, _ []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.orders = append(p.orders, o.ID+" "+string(o.Status))
}
func (p *publisher) PublishBalance(*types.Account)        { p.mu.Lock(); p.balances++; p.mu.Unlock() }
func (p *publisher) PublishInitialBalance(*types.Account) { p.mu.Lock(); p.initial++; p.mu.Unlock() }
func (p *publisher) PublishHeartbeat(telemetry.HeartbeatMeta) {
	p.mu.Lock()
	p.beats++
	p.mu.Unlock()
}
func (p *publisher) PublishStopped(r string) {
	p.mu.Lock()
	p.stopped = append(p.stopped, r)
	p.mu.Unlock()
}
func (p *publisher) PublishSessionMeta(string, map[string]any) {
	p.mu.Lock()
	p.meta++
	p.mu.Unlock()
}
func (p *publisher) Enabled() bool { return true }

func openConsole(t *testing.T, path string) (*Console, *publisher) {
	t.Helper()
	pub := &publisher{}
	c, err := OpenConsole(path, func(id string) telemetry.Publisher { pub.session = id; return pub })
	require.NoError(t, err)
	return c, pub
}

func TestConsole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-orders-console.json")
	c, pub := openConsole(t, path)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, c.SessionID())
	assert.Equal(t, c.SessionID(), pub.session)

	acct := &types.Account{Balances: []types.Balance{{Asset: "USDT", Free: 1000}}}
	c.Start(map[string]any{"executor": "pc-1"}, "test_bybit_futures", acct)
	assert.Equal(t, 1, pub.initial)

	c.Track(&types.Order{ID: "e-1", Symbol: "BTC/USDT", Status: types.OrderStatusFilled},
		&types.Order{ID: "sl-1", Symbol: "BTCUSDT", Status: types.OrderStatusNew})
	assert.Equal(t, []string{"e-1 FILLED", "sl-1 NEW"}, pub.orders)
	assert.True(t, c.Owns("BTCUSDT"))
	assert.True(t, c.OwnsOrder("sl-1"))
	assert.False(t, c.OwnsOrder("e-1"), "a finished order is not followed")

	// The same session after a restart; the initial balance is not sent twice.
	again, pub2 := openConsole(t, path)
	assert.Equal(t, c.SessionID(), again.SessionID())
	assert.True(t, again.OwnsOrder("sl-1"))
	again.Start(nil, "", acct)
	assert.Equal(t, 0, pub2.initial)
	assert.Equal(t, 1, pub2.meta)
}

// Following: what changed is reported; a finished order is dropped; a
// contract left flat with no AI order is forgotten; the balance is sent.
func TestConsoleFollow(t *testing.T) {
	c, pub := openConsole(t, filepath.Join(t.TempDir(), "c.json"))
	c.Track(&types.Order{ID: "sl-1", Symbol: "BTCUSDT", Status: types.OrderStatusNew})
	x := &exchange{mark: 65000, positions: []*types.Position{{Symbol: "BTCUSDT", Side: "LONG", Size: 0.01}}}
	venueFor := func(context.Context, string) (orderexec.Venue, error) { return x, nil }

	// The fake answers every order FILLED: the stop filled while away.
	require.NoError(t, c.Follow(context.Background(), venueFor, "bybit_futures"))
	assert.Equal(t, []string{"sl-1 NEW", "sl-1 FILLED"}, pub.orders)
	assert.False(t, c.OwnsOrder("sl-1"))
	assert.True(t, c.Owns("BTCUSDT"), "still a position there")
	assert.Equal(t, 1, pub.balances)

	x.positions = nil
	require.NoError(t, c.Follow(context.Background(), venueFor, "bybit_futures"))
	assert.False(t, c.Owns("BTCUSDT"), "flat with no AI order: forgotten")
}

// D4: an account holding what this computer did not place is refused; what
// AI orders opened is theirs to act on.
func TestSharedAccount(t *testing.T) {
	e, srv, x := rig(t)
	c, _ := openConsole(t, filepath.Join(t.TempDir(), "c.json"))
	e.WithConsole(c)
	ctx := context.Background()

	x.positions = []*types.Position{{Symbol: "ETHUSDT", Side: "LONG", Size: 0.5}}
	_, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	assert.ErrorContains(t, err, "this exchange account holds a long ETHUSDT position of 0.5 that this computer did not place: AI orders need an account of their own")
	assert.Empty(t, srv.proposed)

	x.positions = nil
	x.open = []*types.Order{{ID: "bot-1", Side: types.OrderSideBuy, Type: types.OrderTypeLimit, Quantity: 0.1}}
	_, _, err = e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	assert.ErrorContains(t, err, "an order bot-1 on BTCUSDT (BUY LIMIT 0.1)")

	// A clean account: placed, and then the position is AI orders'.
	x.open = nil
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	e.Handle(ctx, p.ID)
	require.Equal(t, []string{p.ID + " placed"}, srv.reports)
	assert.True(t, c.Owns("BTCUSDT"))
	x.positions = []*types.Position{{Symbol: "BTCUSDT", Side: "LONG", Size: 0.01, EntryPrice: 65000}}
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose})
	require.NoError(t, err, "AI orders close what they opened")

	// Not what they opened: refused.
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose, Pair: "ETHUSDT"})
	assert.ErrorContains(t, err, "no position is open")
	x.positions = append(x.positions, &types.Position{Symbol: "ETHUSDT", Side: "LONG", Size: 0.5, EntryPrice: 3000})
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose, Pair: "ETHUSDT"})
	assert.ErrorContains(t, err, "this computer did not open the ETHUSDT position: AI orders act only on what they opened")
}
