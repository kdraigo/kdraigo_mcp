package orders

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdingRig: a long 0.01 BTCUSDT from 64,000 with its stop at 63,000 and a
// target, on an exchange at 65,000.
func holdingRig(t *testing.T) (*Executor, *server, *exchange) {
	e, srv, x := rig(t)
	x.positions = []*types.Position{{Symbol: "BTCUSDT", Side: "LONG", Size: 0.01, EntryPrice: 64000, LiquidationPrice: 43000}}
	x.open = []*types.Order{
		{ID: "tp-1", Type: types.OrderTypeTakeProfitLimit, Side: types.OrderSideSell, Price: 67000, ReduceOnly: true},
		{ID: "sl-1", Type: types.OrderTypeStopLoss, Side: types.OrderSideSell, StopPrice: 63000, ReduceOnly: true},
	}
	return e, srv, x
}

func storedAction(t *testing.T, p *Proposal) orderplan.Action {
	t.Helper()
	a, err := p.Action()
	require.NoError(t, err)
	return a
}

func TestProposeAction(t *testing.T) {
	e, srv, x := holdingRig(t)
	ctx := context.Background()

	p, a, err := e.ProposeAction(ctx, ActionInput{Kind: KindClose})
	require.NoError(t, err)
	assert.Equal(t, KindClose, p.Kind)
	assert.Equal(t, orderplan.ActClose, a.Kind)
	assert.InDelta(t, 10.0, a.PnL, 1e-9)
	assert.Equal(t, 63000.0, a.OldStop, "the stop read from the resting orders")
	assert.Equal(t, a.Hash, storedAction(t, p).Hash)
	in := srv.proposed[len(srv.proposed)-1]
	assert.Nil(t, in.Ticket)
	assert.Equal(t, "pc-test0001", in.Executor)
	assert.Empty(t, x.placed, "a proposal places nothing")

	p, a, err = e.ProposeAction(ctx, ActionInput{Kind: KindMoveStop, Pair: "btcusdt", Stop: 64000})
	require.NoError(t, err)
	assert.Equal(t, KindMoveStop, p.Kind)
	assert.Equal(t, 64000.0, a.Stop)

	// Above the price for a long: refused, with the checks, nothing proposed.
	n := len(srv.proposed)
	_, a, err = e.ProposeAction(ctx, ActionInput{Kind: KindMoveStop, Stop: 66000})
	var refused *ActionRefused
	require.ErrorAs(t, err, &refused)
	assert.False(t, a.OK)
	assert.Contains(t, err.Error(), "side:")
	assert.Len(t, srv.proposed, n)

	// Cancel: the one resting entry, or the one named; none or several unnamed is said.
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindCancel})
	assert.ErrorContains(t, err, "no entry order rests on BTCUSDT: nothing to cancel")
	x.open = append(x.open, &types.Order{ID: "e-1", Type: types.OrderTypeLimit, Side: types.OrderSideBuy, Quantity: 0.02, Price: 62000})
	p, a, err = e.ProposeAction(ctx, ActionInput{Kind: KindCancel})
	require.NoError(t, err)
	assert.Equal(t, "e-1", a.OrderID)
	assert.Equal(t, KindCancel, p.Kind)
	x.open = append(x.open, &types.Order{ID: "e-2", Type: types.OrderTypeLimit, Side: types.OrderSideBuy, Quantity: 0.01, Price: 61000})
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindCancel})
	assert.ErrorContains(t, err, "2 entries rest on BTCUSDT: name one with order_id (e-1: long 0.02 at 62000; e-2: long 0.01 at 61000)")
	_, a, err = e.ProposeAction(ctx, ActionInput{Kind: KindCancel, OrderID: "e-2"})
	require.NoError(t, err)
	assert.Equal(t, "e-2", a.OrderID)

	x.positions = nil
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose})
	assert.ErrorContains(t, err, "no position is open on bybit_futures BTCUSDT: nothing to close")
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: "flip"})
	assert.ErrorContains(t, err, `kind "flip"`)
	_, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindClose, Venue: "binance_futures", Pair: "BTCUSDT"})
	assert.ErrorContains(t, err, "orders from this computer are on bybit_futures")
}

// A confirmed close: re-checked, carried out once (the exits taken back
// once flat), journalled and reported.
func TestHandle_Close(t *testing.T) {
	e, srv, x := holdingRig(t)
	ctx := context.Background()
	p, _, err := e.ProposeAction(ctx, ActionInput{Kind: KindClose})
	require.NoError(t, err)
	srv.confirm(p.ID)
	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)
	assert.Equal(t, []string{"SELL MARKET 0.01"}, x.placed)
	assert.Equal(t, []string{"tp-1", "sl-1"}, x.cancelled)
	doc := srv.results[p.ID]
	require.NotNil(t, doc.Action)
	assert.Equal(t, 0.01, doc.Action.Filled)
	assert.Equal(t, "reported", e.journal.State(p.ID))
	e.Handle(ctx, p.ID)
	assert.Len(t, x.placed, 1, "once")
}

// The new stop is in place before the old one goes.
func TestHandle_MoveStop(t *testing.T) {
	e, srv, x := holdingRig(t)
	ctx := context.Background()
	p, _, err := e.ProposeAction(ctx, ActionInput{Kind: KindMoveStop, Stop: 64000})
	require.NoError(t, err)
	srv.confirm(p.ID)
	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)
	assert.Equal(t, []string{"SELL STOP 0.01 @ 64000"}, x.placed)
	assert.Equal(t, []string{"sl-1"}, x.cancelled)
	assert.Equal(t, &orderexec.ActionResult{OrderID: "sl-new", Cancelled: []string{"sl-1"}}, srv.results[p.ID].Action)
}

// The new target rests before the old one goes; its price is in the hash.
func TestHandle_MoveTarget(t *testing.T) {
	e, srv, x := holdingRig(t)
	ctx := context.Background()
	_, _, err := e.ProposeAction(ctx, ActionInput{Kind: KindMoveTarget})
	assert.ErrorContains(t, err, "target")
	p, a, err := e.ProposeAction(ctx, ActionInput{Kind: KindMoveTarget, Target: 68000})
	require.NoError(t, err)
	assert.Equal(t, orderplan.ActMoveTarget, a.Kind)
	assert.Equal(t, 67000.0, a.OldTarget)
	assert.Equal(t, 68000.0, a.Target)
	assert.Equal(t, storedAction(t, p).Hash, a.Hash)
	srv.confirm(p.ID)
	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)
	assert.Equal(t, []string{"SELL TARGET 0.01 @ 68000"}, x.placed)
	assert.Equal(t, []string{"tp-1"}, x.cancelled)
	assert.Equal(t, &orderexec.ActionResult{OrderID: "tp-new", Cancelled: []string{"tp-1"}}, srv.results[p.ID].Action)

	// The wrong side of the price is refused before anything is proposed.
	_, bad, err := e.ProposeAction(ctx, ActionInput{Kind: KindMoveTarget, Target: 64000})
	assert.Error(t, err)
	assert.False(t, bad.OK)
}

// What is open changed after the yes: nothing is done, the checks say why.
func TestHandle_ActionRecheck(t *testing.T) {
	e, srv, x := holdingRig(t)
	ctx := context.Background()
	p, _, err := e.ProposeAction(ctx, ActionInput{Kind: KindClose})
	require.NoError(t, err)
	srv.confirm(p.ID)
	x.positions = nil // the stop filled meanwhile
	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{p.ID + " failed"}, srv.reports)
	assert.Empty(t, x.placed)
	doc := srv.results[p.ID]
	assert.Contains(t, doc.Error, "what is open changed since it was confirmed")
	assert.Contains(t, failedChecks(doc.Checks), "position")

	// An action changed after the yes is never carried out.
	e, srv, x = holdingRig(t)
	p, _, err = e.ProposeAction(ctx, ActionInput{Kind: KindMoveStop, Stop: 64000})
	require.NoError(t, err)
	srv.confirm(p.ID)
	a := storedAction(t, p)
	a.Stop = 64500
	srv.proposals[p.ID].Ticket, _ = json.Marshal(a)
	e.Handle(ctx, p.ID)
	assert.Empty(t, x.placed)
	assert.Contains(t, srv.results[p.ID].Error, "does not match the one confirmed")
}

// Carrying out began and this computer stopped: not done again; the exchange
// says whether it took.
func TestHandle_ActionRecovers(t *testing.T) {
	e, srv, x := holdingRig(t)
	ctx := context.Background()
	p, _, err := e.ProposeAction(ctx, ActionInput{Kind: KindClose})
	require.NoError(t, err)
	srv.confirm(p.ID)
	require.NoError(t, e.journal.Started(p.ID, p.Hash))
	x.positions = nil
	e.Handle(ctx, p.ID)
	assert.Empty(t, x.placed)
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)
	doc := srv.results[p.ID]
	assert.True(t, doc.Recovered)
	assert.Contains(t, doc.Error, "the exchange shows it done")
}
