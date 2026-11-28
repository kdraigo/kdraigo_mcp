package orders

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kdraigo/dev_sdk/exchange/live"
	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/types"
)

// VenueFactory makes a venue prepared for one symbol: the real adapters in
// use (LiveVenues), a fake in tests.
type VenueFactory func(ctx context.Context, symbol string) (orderexec.Venue, error)

// LiveVenues prepares dev_sdk's futures adapter for the configured venue and
// network. The SDK guard's caps are the user's limits (a little over the
// largest position, for the price moving between building and placing); on
// mainnet the guard also wants KDRAIGO_LIVE_ARMED=1 in this process's
// environment, which this code never sets.
func LiveVenues(cfg *Config, keys Keys) VenueFactory {
	return func(ctx context.Context, symbol string) (orderexec.Venue, error) {
		tc := &types.Config{
			Environment: cfg.Environment(),
			Credentials: types.Credentials{APIKey: keys.APIKey, APISecret: keys.APISecret},
			Live: &types.LiveOptions{
				Assets:           []string{symbol},
				MaxOrderNotional: cfg.Limits.MaxPositionValue * (1 + cfg.Tolerance.PriceDrift + 0.01),
				MaxLeverage:      cfg.Limits.MaxLeverage,
				Armed:            cfg.Mainnet(),
			},
		}
		var v interface {
			orderexec.Venue
			PrepareSession(context.Context, *types.Config) error
		}
		switch cfg.Venue {
		case "bybit_futures":
			v = live.NewBybitFuturesClient(tc)
		case "binance_futures":
			v = live.NewBinanceFuturesClient(tc)
		default:
			return nil, fmt.Errorf("venue %q: bybit_futures or binance_futures", cfg.Venue)
		}
		if err := v.PrepareSession(ctx, tc); err != nil {
			return nil, err
		}
		return v, nil
	}
}

// Executor proposes orders from drawings and places the confirmed ones.
type Executor struct {
	cfg     *Config
	server  Server
	venues  VenueFactory
	journal *Journal
	id      string
	now     func() time.Time
	logf    func(format string, a ...any)

	place sync.Mutex // one placement at a time: each re-reads the account

	// handover, asked as the executor stops, says whether another process
	// took over (nil: none can).
	handover func() bool

	mu sync.Mutex
	// console is the AI orders' live-console session and memory (nil: none).
	console *Console
	// standby: another process holds the account's executor lock and places
	// (wiki bug #76); this one proposes, and its checks read the console file
	// at consolePath as it is at each check. holder is that process.
	standby     bool
	consolePath string
	holder      Holder
	// stopReported: the console was told "stopped" as this executor ended.
	stopReported bool
	connected    bool
	lastErr      string
	busy         map[string]bool
}

// New makes an executor. logf may be nil.
func New(cfg *Config, server Server, venues VenueFactory, journal *Journal, id string, logf func(string, ...any)) *Executor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Executor{cfg: cfg, server: server, venues: venues, journal: journal, id: id, now: time.Now, logf: logf, busy: map[string]bool{}}
}

// WithConsole reports AI orders to the live console and keeps the memory of
// what they opened.
func (e *Executor) WithConsole(c *Console) *Executor {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.console = c
	return e
}

// StandBy makes this executor propose only: another process holds the
// account's executor lock and places what the user confirms (wiki bug #76).
// The checks read that process's console file, consolePath, as it is at each
// check. h is that process as it wrote itself down.
func (e *Executor) StandBy(consolePath string, h Holder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.standby, e.consolePath, e.holder = true, consolePath, h
}

// TakeOver makes this executor the one that places, with the journal and
// the console read now: what the process that placed before left in them.
// Run starts after it.
func (e *Executor) TakeOver(j *Journal, c *Console) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.journal, e.console, e.standby, e.holder = j, c, false, Holder{}
}

// Standby says whether another process places, and which (zero: not known).
func (e *Executor) Standby() (bool, Holder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.standby, e.holder
}

// OnStop sets what the executor asks as it stops: whether another process on
// this computer took over. Then the live-console session runs on with that
// one's heartbeats, and is not told "stopped".
func (e *Executor) OnStop(handedOver func() bool) { e.handover = handedOver }

// StopReported: the console was told "stopped" as this executor ended (the
// report is sent in the background, so the process gives it a moment).
func (e *Executor) StopReported() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopReported
}

// live is the console of the process that places (nil: none).
func (e *Executor) live() *Console {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.console
}

// memory is what the checks read for what AI orders opened: this process's
// console while it places; for a standby, the console file of the process
// that places, as it is now. Nil: no memory is kept, and the checks are off.
func (e *Executor) memory() (*Console, error) {
	e.mu.Lock()
	c, path, standby := e.console, e.consolePath, e.standby
	e.mu.Unlock()
	if !standby || path == "" {
		return c, nil
	}
	m, err := ReadConsole(path)
	if err != nil {
		return nil, fmt.Errorf("the AI orders' memory could not be read: %w", err)
	}
	return m, nil
}

// shared is what the account holds that AI orders did not place (D4).
func (e *Executor) shared(ctx context.Context, v orderexec.Venue, venue, symbol string) error {
	c, err := e.memory()
	if err != nil {
		return err
	}
	if c == nil {
		return nil
	}
	what, err := c.foreign(ctx, v, venue, symbol)
	if err != nil {
		return err
	}
	if len(what) > 0 {
		return errShared(what)
	}
	return nil
}

// track reports orders by id, read back from the venue, to the console.
func (e *Executor) track(ctx context.Context, v orderexec.Venue, venue, symbol string, ids ...string) {
	c := e.live()
	if c == nil {
		return
	}
	var list []*types.Order
	for _, id := range ids {
		if id == "" {
			continue
		}
		o, err := v.GetOrder(context.WithoutCancel(ctx), venue, symbol, id)
		if err != nil || o == nil {
			o = &types.Order{ID: id, Status: types.OrderStatusNew}
		}
		if o.Symbol == "" {
			o.Symbol = symbol
		}
		if o.Exchange == "" {
			o.Exchange = venue
		}
		list = append(list, o)
	}
	c.Track(list...)
}

// runConsole declares the session, heartbeats every 5 s (the live console
// calls a session silent for 15 s disconnected), follows the AI orders'
// fills every 30 s, and says "stopped" when ctx ends, unless another process
// on this computer took over (OnStop).
func (e *Executor) runConsole(ctx context.Context) {
	c := e.live()
	if c == nil {
		return
	}
	start := e.now()
	meta := map[string]any{"executor": e.id, "venue": e.cfg.Venue, "network": e.cfg.Network, "kind": "ai-orders"}
	var acct *types.Account
	if v, err := e.venues(ctx, "BTCUSDT"); err == nil {
		acct, _ = v.GetAccount(ctx, e.cfg.Venue, orderexec.Settlement)
	}
	c.Start(meta, string(e.cfg.Environment()), acct)
	beat := time.NewTicker(5 * time.Second)
	defer beat.Stop()
	follow := time.NewTicker(30 * time.Second)
	defer follow.Stop()
	c.Heartbeat(0)
	for {
		select {
		case <-ctx.Done():
			if e.handover != nil && e.handover() {
				e.logf("orders: another kdraigo-mcp on this computer took over the executor")
				return
			}
			c.Stopped("kdraigo-mcp on this computer closed: AI orders are not followed until it runs again")
			e.mu.Lock()
			e.stopReported = true
			e.mu.Unlock()
			return
		case <-beat.C:
			c.Heartbeat(e.now().Sub(start))
		case <-follow.C:
			if err := c.Follow(ctx, e.venues, e.cfg.Venue); err != nil {
				e.logf("orders: following the AI orders: %v", err)
			}
		}
	}
}

// ID is this computer's executor name, the one proposals carry.
func (e *Executor) ID() string { return e.id }

// Config is the orders file it runs with.
func (e *Executor) Config() *Config { return e.cfg }

// Server is kdraigo, as this executor talks to it.
func (e *Executor) Server() Server { return e.server }

// Connection is whether kdraigo hears this executor now, and the last
// trouble.
func (e *Executor) Connection() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connected, e.lastErr
}

func (e *Executor) setConnection(on bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.connected = on
	if err != nil {
		e.lastErr = err.Error()
	} else if on {
		e.lastErr = ""
	}
}

// ── proposing ───────────────────────────────────────────────────────────────

// ProposeInput is what the AI asks for: one of the user's position drawings,
// entered at market or with a limit, as the user chose.
type ProposeInput struct {
	Venue, Pair string // the drawing's market; empty is the shared chart's
	DrawingID   string
	EntryType   orderplan.EntryType
	// Risk can lower the user's risk per trade for this order, never raise it.
	Risk float64
}

// Refused is a ticket that did not pass its checks: nothing was proposed.
type Refused struct{ Ticket orderplan.Ticket }

func (r *Refused) Error() string {
	return "the order did not pass its checks, so nothing was proposed:\n" + failedChecks(r.Ticket.Checks)
}

// Propose builds the drawing's ticket on the exchange's numbers now and, if
// it passes, sends it to kdraigo for the user to confirm.
func (e *Executor) Propose(ctx context.Context, in ProposeInput) (*Proposal, *orderplan.Ticket, error) {
	if in.EntryType != orderplan.AtMarket && in.EntryType != orderplan.AtLimit {
		return nil, nil, errors.New("entry_type: market or limit, as the user chose for this order (ask them if they did not say)")
	}
	if in.EntryType == orderplan.AtLimit && e.cfg.Venue != "bybit_futures" {
		return nil, nil, fmt.Errorf("a limit entry is placed on Bybit only for now (its stop and target must rest on the exchange before the fill); on %s, enter at market", e.cfg.Venue)
	}
	venue, pair := in.Venue, in.Pair
	if venue == "" || pair == "" {
		var err error
		if venue, pair, err = e.server.ChartMarket(ctx); err != nil {
			return nil, nil, err
		}
	}
	if venue != e.cfg.Venue {
		return nil, nil, fmt.Errorf("the drawing is on a %s chart, and orders from this computer go to %s (venue in %s): draw it on the %s chart", venue, e.cfg.Venue, e.cfg.Path(), e.cfg.Venue)
	}
	list, err := e.server.Drawings(ctx, venue, pair)
	if err != nil {
		return nil, nil, err
	}
	d, ok := findDrawing(list, in.DrawingID)
	if !ok {
		return nil, nil, fmt.Errorf("drawing %q is not on the %s %s chart: chart_view lists the drawings and their ids", in.DrawingID, venue, pair)
	}
	pos, err := readPosition(d)
	if err != nil {
		return nil, nil, err
	}
	v, err := e.venues(ctx, pair)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.cfg.Venue, err)
	}
	if err := e.shared(ctx, v, venue, pair); err != nil {
		return nil, nil, err
	}
	m, a, err := orderexec.Snapshot(ctx, v, venue, pair, e.now())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.cfg.Venue, err)
	}
	t := orderplan.Build(orderplan.Plan{
		Venue: venue, Symbol: pair, Side: pos.Side, EntryType: in.EntryType,
		Entry: pos.Entry, Stop: pos.Stop, Target: pos.Target, Risk: in.Risk,
	}, m, a, e.cfg.Limits.Plan())
	if !t.OK {
		return nil, &t, &Refused{Ticket: t}
	}
	p, err := e.server.Propose(ctx, ProposalInput{
		Venue: venue, Pair: pair, DrawingID: d.ID, DrawingVersion: d.Version, Ticket: &t, Kind: KindOpen, Executor: e.id, AutoAllowed: e.cfg.AutoAllowed,
	})
	if err != nil {
		return nil, &t, err
	}
	return p, &t, nil
}

// ── placing ─────────────────────────────────────────────────────────────────

// Run keeps the executor's stream to kdraigo open until ctx ends, placing
// each proposal the user confirms. A refusal (no plan with AI orders, a bad
// key) waits ten minutes before asking again; anything else reconnects
// sooner. It returns once the console is done too (told "stopped", or
// handed over).
func (e *Executor) Run(ctx context.Context) {
	consoleDone := make(chan struct{})
	go func() {
		defer close(consoleDone)
		e.runConsole(ctx)
	}()
	defer func() { <-consoleDone }()
	wait := time.Second
	for ctx.Err() == nil {
		body, err := e.server.Events(ctx, e.id)
		if err != nil {
			e.setConnection(false, err)
			var ae *APIError
			if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 {
				wait = 10 * time.Minute
			}
			e.logf("orders: not connected: %v (again in %s)", err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait < time.Minute {
				wait = min(wait*2, time.Minute)
			} else if wait > time.Minute {
				wait = time.Minute
			}
			continue
		}
		e.setConnection(true, nil)
		wait = time.Second
		err = readEvents(body, func(ev event) {
			for _, d := range decisions(ev) {
				if d.Status == StatusConfirmed {
					go e.Handle(context.WithoutCancel(ctx), d.ID)
				}
			}
		})
		body.Close()
		if err == nil {
			err = errors.New("kdraigo closed the stream")
		}
		e.setConnection(false, err)
	}
}

// resultDoc is what the executor reports for a proposal.
type resultDoc struct {
	Result      *orderexec.Result       `json:"result,omitempty"`
	Action      *orderexec.ActionResult `json:"action,omitempty"`
	Error       string                  `json:"error,omitempty"`
	Unprotected bool                    `json:"unprotected,omitempty"`
	Checks      []orderplan.Check       `json:"checks,omitempty"`
	Recovered   bool                    `json:"recovered,omitempty"`
}

// Handle places one confirmed proposal, once. It is safe to call twice for
// the same id: the second call finds it busy, reported, or started.
func (e *Executor) Handle(ctx context.Context, id string) {
	e.mu.Lock()
	if e.busy[id] {
		e.mu.Unlock()
		return
	}
	e.busy[id] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.busy, id)
		e.mu.Unlock()
	}()

	e.place.Lock()
	defer e.place.Unlock()

	switch e.journal.State(id) {
	case "reported":
		return
	case "started":
		e.recoverStarted(ctx, id)
		return
	}
	p, err := e.server.Proposal(ctx, id)
	if err != nil {
		e.logf("orders: proposal %s: %v", id, err)
		return
	}
	if p.Status != StatusConfirmed || p.Executor != e.id {
		return
	}
	var status string
	var doc resultDoc
	if p.IsAction() {
		status, doc = e.actConfirmed(ctx, p)
	} else {
		status, doc = e.placeConfirmed(ctx, p)
	}
	e.report(ctx, id, status, doc)
}

func (e *Executor) placeConfirmed(ctx context.Context, p *Proposal) (string, resultDoc) {
	t, err := p.OpenTicket()
	if err != nil || t.Hash == "" || t.Hash != p.Hash || orderplan.Hash(t) != t.Hash {
		return StatusFailed, resultDoc{Error: "not placed: the ticket does not match the one confirmed"}
	}
	v, err := e.venues(ctx, t.Symbol)
	if err != nil {
		return StatusFailed, resultDoc{Error: fmt.Sprintf("not placed: %s: %v", e.cfg.Venue, err)}
	}
	m, a, err := orderexec.Snapshot(ctx, v, t.Venue, t.Symbol, e.now())
	if err != nil {
		return StatusFailed, resultDoc{Error: fmt.Sprintf("not placed: %s: %v", e.cfg.Venue, err)}
	}
	checks := orderplan.Recheck(t, m, a, e.cfg.Limits.Plan(), e.cfg.PlanTolerance())
	if !orderplan.Passed(checks) {
		return StatusFailed, resultDoc{Error: "not placed: the market or the account changed since it was confirmed", Checks: checks}
	}
	if err := e.shared(ctx, v, t.Venue, t.Symbol); err != nil {
		return StatusFailed, resultDoc{Error: "not placed: " + err.Error()}
	}
	if msg := e.clearOld(ctx, v, t, a); msg != "" {
		return StatusFailed, resultDoc{Error: msg}
	}
	if err := e.journal.Started(p.ID, t.Hash); err != nil {
		return StatusFailed, resultDoc{Error: "not placed: the local journal could not be written (" + err.Error() + ")"}
	}
	res, err := orderexec.Open(ctx, v, t, orderexec.Options{Reason: map[string]any{"source": "ai-order", "proposal": p.ID}})
	if res != nil {
		// The failsafe's closing order too: without it the live console kept
		// a position the venue no longer held (bug #69).
		e.track(ctx, v, t.Venue, t.Symbol, res.EntryID, res.StopID, res.TargetID, res.CloseID)
	}
	if err != nil {
		return StatusFailed, resultDoc{Result: res, Error: err.Error(), Unprotected: errors.Is(err, orderexec.ErrUnprotected)}
	}
	return StatusPlaced, resultDoc{Result: res}
}

// openOrderLister is the adapters' open-order read.
type openOrderLister interface {
	ListOpenOrders(ctx context.Context, exchange, symbol string) ([]*types.Order, error)
}

// clearOld looks at the contract's open orders before a new position: with
// no position, a resting exit is left over from an earlier one (its sibling
// filled while this computer was off) and is cancelled, since it would cut
// the new position; a resting entry means an order is already waiting there,
// and the new one is refused.
func (e *Executor) clearOld(ctx context.Context, v orderexec.Venue, t orderplan.Ticket, a orderplan.Account) string {
	l, ok := v.(openOrderLister)
	if !ok || a.PositionOnSymbol {
		return ""
	}
	open, err := l.ListOpenOrders(ctx, t.Venue, t.Symbol)
	if err != nil {
		return fmt.Sprintf("not placed: the open orders on %s could not be read (%v)", t.Symbol, err)
	}
	for _, o := range open {
		if o == nil {
			continue
		}
		if !o.ReduceOnly {
			return fmt.Sprintf("not placed: an order already rests on %s (%s %s %g): cancel it first", t.Symbol, o.Side, o.Type, o.Quantity)
		}
	}
	for _, o := range open {
		if o != nil && o.ReduceOnly {
			if err := v.CancelOrder(ctx, t.Venue, t.Symbol, o.ID); err != nil && !errors.Is(err, types.ErrOrderClosed) {
				return fmt.Sprintf("not placed: a leftover exit %s on %s could not be cancelled (%v)", o.ID, t.Symbol, err)
			}
			e.logf("orders: cancelled leftover exit %s on %s", o.ID, t.Symbol)
		}
	}
	return ""
}

// recoverStarted finishes a proposal whose placing began but was never
// reported (this computer stopped in between): it does not place again; it
// looks at the exchange and says what is there.
func (e *Executor) recoverStarted(ctx context.Context, id string) {
	p, err := e.server.Proposal(ctx, id)
	if err != nil {
		e.logf("orders: proposal %s: %v", id, err)
		return
	}
	if p.Status == StatusPlaced || p.Status == StatusFailed {
		_ = e.journal.Reported(id, p.Status)
		return
	}
	if p.IsAction() {
		status, doc := e.recoverAction(ctx, p)
		e.report(ctx, id, status, doc)
		return
	}
	doc := resultDoc{Recovered: true}
	status := StatusFailed
	v, err := e.venues(ctx, p.Pair)
	if err == nil {
		var a orderplan.Account
		_, a, err = orderexec.Snapshot(ctx, v, p.Venue, p.Pair, e.now())
		if err == nil && a.PositionOnSymbol {
			status = StatusPlaced
			doc.Error = fmt.Sprintf("this computer stopped while placing it; a position is open on %s: check that its stop is on the exchange", p.Pair)
		} else if err == nil {
			doc.Error = fmt.Sprintf("this computer stopped while placing it; no position is open on %s (a resting limit entry may remain: check the exchange)", p.Pair)
		}
	}
	if err != nil {
		doc.Error = fmt.Sprintf("this computer stopped while placing it, and %s could not be read (%v): check %s on the exchange", e.cfg.Venue, err, p.Pair)
	}
	e.report(ctx, id, status, doc)
}

// report tells kdraigo the outcome, trying a few times; the journal keeps
// "started" until kdraigo heard it.
func (e *Executor) report(ctx context.Context, id, status string, doc resultDoc) {
	if doc.Error != "" {
		e.logf("orders: %s %s: %s", id, status, doc.Error)
	}
	var err error
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		if _, err = e.server.Report(ctx, id, e.id, status, doc); err == nil {
			_ = e.journal.Reported(id, status)
			return
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 409 {
			// Already finished on kdraigo's side.
			_ = e.journal.Reported(id, status)
			return
		}
	}
	e.logf("orders: could not report %s as %s: %v", id, status, err)
}

// ── this computer's name ────────────────────────────────────────────────────

var executorPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ExecutorID is this computer's executor name, kept in dir/executor-id.
func ExecutorID(dir string) (string, error) {
	path := filepath.Join(dir, "executor-id")
	if raw, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(raw)); executorPattern.MatchString(id) {
			return id, nil
		}
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := "pc-" + hex.EncodeToString(b)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(path, []byte(id+"\n"), 0o600)
}
