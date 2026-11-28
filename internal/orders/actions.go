package orders

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"
)

// Actions on what an AI order opened (the user's D5): close the position,
// cancel a resting entry, move the stop. Built from what the exchange holds
// now (orderexec.Holdings), checked (orderplan), confirmed by the user like
// an open, checked again, then carried out (orderexec.Act).

// ActionInput is what the AI asks for.
type ActionInput struct {
	Kind        string // close, cancel, move_stop, move_target
	Venue, Pair string // empty: the shared chart's market
	OrderID     string // cancel: the order; empty when exactly one entry rests
	Stop        float64
	Target      float64 // move_target: the new target
}

// ActionRefused is an action that did not pass its checks: nothing proposed.
type ActionRefused struct{ Action orderplan.Action }

func (r *ActionRefused) Error() string {
	return "the action did not pass its checks, so nothing was proposed:\n" + failedChecks(r.Action.Checks)
}

// ProposeAction builds the action on the exchange's numbers now and, if it
// passes, sends it to kdraigo for the user to confirm.
func (e *Executor) ProposeAction(ctx context.Context, in ActionInput) (*Proposal, *orderplan.Action, error) {
	switch in.Kind {
	case KindClose, KindCancel, KindMoveStop, KindMoveTarget:
	default:
		return nil, nil, fmt.Errorf("kind %q: close, cancel, move_stop or move_target", in.Kind)
	}
	venue, pair := in.Venue, strings.ToUpper(in.Pair)
	if venue == "" {
		venue = e.cfg.Venue
	}
	if pair == "" {
		cv, cp, err := e.server.ChartMarket(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("name the pair, or share the chart on it: %w", err)
		}
		if cv != e.cfg.Venue {
			return nil, nil, fmt.Errorf("the shared chart is on %s; orders from this computer are on %s: name the pair", cv, e.cfg.Venue)
		}
		pair = cp
	}
	if venue != e.cfg.Venue {
		return nil, nil, fmt.Errorf("orders from this computer are on %s (venue in %s), not %s", e.cfg.Venue, e.cfg.Path(), venue)
	}
	v, err := e.venues(ctx, pair)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.cfg.Venue, err)
	}
	m, _, err := orderexec.Snapshot(ctx, v, venue, pair, e.now())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.cfg.Venue, err)
	}
	h, resting, _, err := orderexec.Holdings(ctx, v, venue, pair)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.cfg.Venue, err)
	}

	// AI orders act only on what they opened (D4).
	mem, err := e.memory()
	if err != nil {
		return nil, nil, err
	}
	mine := func(what string) error {
		if mem != nil && !mem.Owns(pair) {
			return fmt.Errorf("this computer did not open the %s %s: AI orders act only on what they opened", pair, what)
		}
		return nil
	}
	var a orderplan.Action
	switch in.Kind {
	case KindClose:
		if h == nil {
			return nil, nil, fmt.Errorf("no position is open on %s %s: nothing to close", venue, pair)
		}
		if err := mine("position"); err != nil {
			return nil, nil, err
		}
		a = orderplan.BuildClose(venue, pair, *h, m)
	case KindCancel:
		r, err := pickResting(resting, in.OrderID, pair)
		if err != nil {
			return nil, nil, err
		}
		if mem != nil && !mem.OwnsOrder(r.ID) {
			return nil, nil, fmt.Errorf("this computer did not place the order %s on %s: AI orders act only on what they opened", r.ID, pair)
		}
		a = orderplan.BuildCancel(venue, pair, r, m)
	case KindMoveStop:
		if h == nil {
			return nil, nil, fmt.Errorf("no position is open on %s %s: no stop to move", venue, pair)
		}
		if in.Stop <= 0 {
			return nil, nil, fmt.Errorf("stop: the new stop's price")
		}
		if err := mine("position"); err != nil {
			return nil, nil, err
		}
		a = orderplan.BuildMoveStop(venue, pair, *h, in.Stop, m, e.cfg.Limits.Plan())
	case KindMoveTarget:
		if h == nil {
			return nil, nil, fmt.Errorf("no position is open on %s %s: no target to set", venue, pair)
		}
		if in.Target <= 0 {
			return nil, nil, fmt.Errorf("target: the new target's price")
		}
		if err := mine("position"); err != nil {
			return nil, nil, err
		}
		a = orderplan.BuildMoveTarget(venue, pair, *h, in.Target, m, e.cfg.Limits.Plan())
	}
	if !a.OK {
		return nil, &a, &ActionRefused{Action: a}
	}
	p, err := e.server.Propose(ctx, ProposalInput{Venue: venue, Pair: pair, Kind: in.Kind, Action: &a, Executor: e.id, AutoAllowed: e.cfg.AutoAllowed})
	if err != nil {
		return nil, &a, err
	}
	return p, &a, nil
}

// pickResting is the entry to cancel: the one named, or the only one.
func pickResting(rs []orderplan.Resting, id, pair string) (orderplan.Resting, error) {
	if id != "" {
		for _, r := range rs {
			if r.ID == id {
				return r, nil
			}
		}
		return orderplan.Resting{}, fmt.Errorf("no entry %s rests on %s (resting: %s)", id, pair, restingList(rs))
	}
	switch len(rs) {
	case 0:
		return orderplan.Resting{}, fmt.Errorf("no entry order rests on %s: nothing to cancel", pair)
	case 1:
		return rs[0], nil
	}
	return orderplan.Resting{}, fmt.Errorf("%d entries rest on %s: name one with order_id (%s)", len(rs), pair, restingList(rs))
}

func restingList(rs []orderplan.Resting) string {
	if len(rs) == 0 {
		return "none"
	}
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = fmt.Sprintf("%s: %s %g at %g", r.ID, r.Side, r.Qty, r.Price)
	}
	return strings.Join(out, "; ")
}

// actConfirmed carries out a confirmed action, once.
func (e *Executor) actConfirmed(ctx context.Context, p *Proposal) (string, resultDoc) {
	a, err := p.Action()
	if err != nil || a.Hash == "" || a.Hash != p.Hash || orderplan.ActionHash(a) != a.Hash {
		return StatusFailed, resultDoc{Error: "not done: the action does not match the one confirmed"}
	}
	v, err := e.venues(ctx, a.Symbol)
	if err != nil {
		return StatusFailed, resultDoc{Error: fmt.Sprintf("not done: %s: %v", e.cfg.Venue, err)}
	}
	m, _, err := orderexec.Snapshot(ctx, v, a.Venue, a.Symbol, e.now())
	if err != nil {
		return StatusFailed, resultDoc{Error: fmt.Sprintf("not done: %s: %v", e.cfg.Venue, err)}
	}
	h, resting, _, err := orderexec.Holdings(ctx, v, a.Venue, a.Symbol)
	if err != nil {
		return StatusFailed, resultDoc{Error: fmt.Sprintf("not done: %s: %v", e.cfg.Venue, err)}
	}
	var r *orderplan.Resting
	for i := range resting {
		if resting[i].ID == a.OrderID {
			r = &resting[i]
		}
	}
	checks := orderplan.RecheckAction(a, h, r, m, e.cfg.Limits.Plan())
	if !orderplan.Passed(checks) {
		return StatusFailed, resultDoc{Error: "not done: what is open changed since it was confirmed", Checks: checks}
	}
	if err := e.journal.Started(p.ID, a.Hash); err != nil {
		return StatusFailed, resultDoc{Error: "not done: the local journal could not be written (" + err.Error() + ")"}
	}
	res, err := orderexec.Act(ctx, v, a, orderexec.Options{Reason: map[string]any{"source": "ai-order", "proposal": p.ID, "action": string(a.Kind)}})
	if res != nil {
		e.track(ctx, v, a.Venue, a.Symbol, append([]string{res.OrderID}, res.Cancelled...)...)
	}
	if err != nil {
		return StatusFailed, resultDoc{Action: res, Error: err.Error()}
	}
	return StatusPlaced, resultDoc{Action: res}
}

// recoverAction reports an action whose carrying out began but was never
// reported: it is not done again; the exchange says whether it took.
func (e *Executor) recoverAction(ctx context.Context, p *Proposal) (string, resultDoc) {
	doc := resultDoc{Recovered: true}
	a, err := p.Action()
	if err != nil {
		doc.Error = "this computer stopped while carrying it out: check the exchange"
		return StatusFailed, doc
	}
	v, err := e.venues(ctx, a.Symbol)
	var h *orderplan.Held
	var resting []orderplan.Resting
	if err == nil {
		h, resting, _, err = orderexec.Holdings(ctx, v, a.Venue, a.Symbol)
	}
	if err != nil {
		doc.Error = fmt.Sprintf("this computer stopped while carrying it out, and %s could not be read (%v): check %s on the exchange", e.cfg.Venue, err, a.Symbol)
		return StatusFailed, doc
	}
	done := false
	switch a.Kind {
	case orderplan.ActClose:
		done = h == nil
	case orderplan.ActCancel:
		done = true
		for _, r := range resting {
			if r.ID == a.OrderID {
				done = false
			}
		}
	case orderplan.ActMoveStop:
		done = h != nil && math.Abs(h.Stop-a.Stop) < a.TickSize/2
	case orderplan.ActMoveTarget:
		done = h != nil && math.Abs(h.Target-a.Target) < a.TickSize/2
	}
	if done {
		doc.Error = "this computer stopped while carrying it out; the exchange shows it done"
		return StatusPlaced, doc
	}
	doc.Error = fmt.Sprintf("this computer stopped while carrying it out; the exchange does not show it done: check %s", a.Symbol)
	return StatusFailed, doc
}

// Holding is what is open on one contract, for the AI to read.
type Holding struct {
	Symbol  string
	Mark    float64
	Held    *orderplan.Held
	Resting []orderplan.Resting
	// NotMine: AI orders did not open this (another trader uses the account).
	NotMine bool
}

// Holdings reads what is open on the account (or on one pair): positions
// with their stops, and resting entries.
func (e *Executor) Holdings(ctx context.Context, pair string) ([]Holding, error) {
	pair = strings.ToUpper(pair)
	prep := pair
	if prep == "" {
		prep = "BTCUSDT" // any listed contract: the account's reads do not depend on it
	}
	v, err := e.venues(ctx, prep)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.cfg.Venue, err)
	}
	symbols := []string{}
	seen := map[string]bool{}
	if pair != "" {
		symbols, seen[pair] = append(symbols, pair), true
	} else {
		ps, err := v.GetPositions(ctx, e.cfg.Venue)
		if err != nil {
			return nil, fmt.Errorf("positions: %w", err)
		}
		for _, p := range ps {
			s := strings.ToUpper(strings.ReplaceAll(p.Symbol, "/", ""))
			if p.Size > 0 && !seen[s] {
				symbols, seen[s] = append(symbols, s), true
			}
		}
	}
	mem, err := e.memory()
	if err != nil {
		return nil, err
	}
	out := make([]Holding, 0, len(symbols))
	for _, s := range symbols {
		h, resting, _, err := orderexec.Holdings(ctx, v, e.cfg.Venue, s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s, err)
		}
		hv := Holding{Symbol: s, Held: h, Resting: resting, NotMine: mem != nil && !mem.Owns(s)}
		if c, err := v.Contract(ctx, e.cfg.Venue, s); err == nil {
			hv.Mark = c.Mark
		}
		out = append(out, hv)
	}
	return out, nil
}
