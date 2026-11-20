package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/telemetry"
	"github.com/kdraigo/dev_sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── fakes ───────────────────────────────────────────────────────────────────

// exchange is an in-memory Bybit: a contract, a wallet, positions, and the
// orders that reach it.
type exchange struct {
	mu         sync.Mutex
	bracketErr error
	mark       float64
	positions  []*types.Position
	open       []*types.Order
	placed     []string
	cancelled  []string
}

func (x *exchange) PlaceOrder(_ context.Context, r *types.OrderRequest) (*types.Order, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if r.Type == types.OrderTypeStopLoss {
		x.placed = append(x.placed, fmt.Sprintf("%s STOP %g @ %g", r.Side, r.Quantity, r.StopPrice))
		return &types.Order{ID: "sl-new", Type: types.OrderTypeStopLoss, StopPrice: r.StopPrice, ReduceOnly: true}, nil
	}
	if r.Type == types.OrderTypeTakeProfitLimit {
		x.placed = append(x.placed, fmt.Sprintf("%s TARGET %g @ %g", r.Side, r.Quantity, r.Price))
		return &types.Order{ID: "tp-new", Type: types.OrderTypeTakeProfitLimit, Price: r.Price, ReduceOnly: true}, nil
	}
	x.placed = append(x.placed, fmt.Sprintf("%s %s %g", r.Side, r.Type, r.Quantity))
	if r.ReduceOnly {
		x.positions = nil // closed
	}
	return &types.Order{ID: fmt.Sprintf("o-%d", len(x.placed)), Status: types.OrderStatusFilled, Quantity: r.Quantity, FilledQty: r.Quantity, AveragePrice: x.mark}, nil
}
func (x *exchange) CancelOrder(_ context.Context, _, _, id string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.cancelled = append(x.cancelled, id)
	return nil
}
func (x *exchange) GetOrder(_ context.Context, _, _, id string) (*types.Order, error) {
	return &types.Order{ID: id, Status: types.OrderStatusFilled}, nil
}
func (x *exchange) GetAccount(context.Context, string, string) (*types.Account, error) {
	return &types.Account{Balances: []types.Balance{{Asset: "USDT", Free: 1000}}}, nil
}
func (x *exchange) GetPositions(context.Context, string) ([]*types.Position, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.positions, nil
}
func (x *exchange) SetLeverage(context.Context, string, string, float64) error { return nil }
func (x *exchange) PlaceBracket(_ context.Context, r *types.BracketRequest) ([]*types.Order, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.placed = append(x.placed, fmt.Sprintf("bracket %s %g tp %g sl %g", r.Side, r.Quantity, r.TakeProfitPrice, r.StopPrice))
	if x.bracketErr != nil {
		return nil, x.bracketErr
	}
	return []*types.Order{{ID: "tp", Type: types.OrderTypeTakeProfitLimit}, {ID: "sl", Type: types.OrderTypeStopLoss}}, nil
}
func (x *exchange) Contract(_ context.Context, exchange, symbol string) (*types.Contract, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return &types.Contract{Exchange: exchange, Symbol: symbol, TickSize: 0.1, StepSize: 0.001, MinQty: 0.001, MinNotional: 5, MaxLeverage: 100, MaintenanceMargin: 0.005, Mark: x.mark}, nil
}
func (x *exchange) RealizedPnL(context.Context, string, time.Time) (float64, error) { return 0, nil }
func (x *exchange) ListOpenOrders(context.Context, string, string) ([]*types.Order, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.open, nil
}

// server is an in-memory research_service.
type server struct {
	mu        sync.Mutex
	drawings  []Drawing
	proposals map[string]*Proposal
	proposed  []ProposalInput
	reports   []string
	results   map[string]resultDoc
	events    io.ReadCloser
	n         int
}

func newServer() *server {
	return &server{proposals: map[string]*Proposal{}, results: map[string]resultDoc{}}
}

func (s *server) ChartMarket(context.Context) (string, string, error) {
	return "bybit_futures", "BTCUSDT", nil
}
func (s *server) Drawings(context.Context, string, string) ([]Drawing, error) {
	return s.drawings, nil
}
func (s *server) Propose(_ context.Context, in ProposalInput) (*Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.proposed = append(s.proposed, in)
	p := &Proposal{ID: fmt.Sprintf("p-%d", s.n), Kind: in.Kind, Venue: in.Venue, Pair: in.Pair, Status: StatusPending, Executor: in.Executor}
	if in.Action != nil {
		p.Ticket, _ = json.Marshal(in.Action)
		p.Hash = in.Action.Hash
	} else {
		p.Ticket, _ = json.Marshal(in.Ticket)
		p.Hash = in.Ticket.Hash
	}
	s.proposals[p.ID] = p
	return p, nil
}
func (s *server) Proposal(_ context.Context, id string) (*Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.proposals[id]
	if !ok {
		return nil, &APIError{Status: 404, Message: "not found"}
	}
	cp := *p
	return &cp, nil
}
func (s *server) Proposals(context.Context, int) ([]Proposal, error)  { return nil, nil }
func (s *server) Withdraw(context.Context, string) (*Proposal, error) { return nil, nil }
func (s *server) Report(_ context.Context, id, executor, status string, result any) (*Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.proposals[id]
	if p == nil || p.Status != StatusConfirmed || p.Executor != executor {
		return nil, &APIError{Status: 409, Message: "not a confirmed one of this executor's"}
	}
	p.Status = status
	s.reports = append(s.reports, id+" "+status)
	s.results[id] = result.(resultDoc)
	return p, nil
}
func (s *server) Events(context.Context, string) (io.ReadCloser, error) {
	if s.events == nil {
		return nil, &APIError{Status: 403, Message: "orders from your AI are part of Plus and Pro"}
	}
	r := s.events
	s.events = nil
	return r, nil
}

func (s *server) confirm(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposals[id].Status = StatusConfirmed
}

func longDrawing(id string) Drawing {
	return Drawing{ID: id, Version: 7, Body: json.RawMessage(`{"kind":"long-position","points":[{"time":1794873600,"price":65000},{"time":1794960000,"price":65000}],"style":{"stopLevel":1000,"profitLevel":2000}}`)}
}

func testConfig() *Config {
	c := &Config{Venue: "bybit_futures", Network: "testnet", KeyFile: "k.yaml", AutoAllowed: false, path: "/tmp/orders.yaml"}
	c.Limits = LimitsConfig{RiskPerTrade: 10, MaxPositionValue: 2000, Leverage: 3, MaxLeverage: 5, MaxOpenPositions: 2, MaxDailyLoss: 50,
		MaxEntryGap: 0.005, MaxLimitGap: 0.05, MinStopGap: 0.002}
	c.Tolerance.PriceDrift, c.Tolerance.RiskSlack = 0.003, 0.1
	return c
}

func rig(t *testing.T) (*Executor, *server, *exchange) {
	t.Helper()
	srv, x := newServer(), &exchange{mark: 65000}
	srv.drawings = []Drawing{longDrawing("ai_1"), {ID: "dw_2", Version: 1, Body: json.RawMessage(`{"kind":"trend-line","points":[{"time":1,"price":2}]}`)}}
	j, err := OpenJournal(filepath.Join(t.TempDir(), "journal.jsonl"))
	require.NoError(t, err)
	venues := func(context.Context, string) (orderexec.Venue, error) { return x, nil }
	return New(testConfig(), srv, venues, j, "pc-test0001", nil), srv, x
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "orders.yaml"))
	assert.ErrorIs(t, err, ErrNotSetUp)

	path := filepath.Join(dir, "orders.yaml")
	require.NoError(t, os.WriteFile(path, []byte(Template), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	probs := strings.Join(c.Problems(), "\n")
	for _, want := range []string{"limits.risk_per_trade is not set", "limits.max_daily_loss is not set", "tolerance.price_drift is not set",
		"key_file " + filepath.Join(dir, "exchange-key.yaml") + " does not exist"} {
		assert.Contains(t, probs, want)
	}

	key := filepath.Join(dir, "exchange-key.yaml")
	require.NoError(t, os.WriteFile(key, []byte("api_key: k\napi_secret: s\n"), 0o644))
	good := testConfig()
	good.KeyFile, good.path = "exchange-key.yaml", path
	assert.Contains(t, strings.Join(good.Problems(), "\n"), "can be read by other users (mode 644): run chmod 600")
	_, err = good.Keys()
	assert.ErrorContains(t, err, "chmod 600")

	require.NoError(t, os.Chmod(key, 0o600))
	assert.Empty(t, good.Problems())
	k, err := good.Keys()
	require.NoError(t, err)
	assert.Equal(t, Keys{APIKey: "k", APISecret: "s"}, k)

	assert.Equal(t, types.EnvTestBybitFutures, good.Environment())
	good.Network = "mainnet"
	assert.Equal(t, types.EnvRealBybitFutures, good.Environment())
	good.Venue, good.Network = "binance_futures", "testnet"
	assert.Equal(t, types.EnvTestBinanceFutures, good.Environment())
	good.Limits.Leverage, good.Limits.MaxEntryGap = 9, 0.5
	probs = strings.Join(good.Problems(), "\n")
	assert.Contains(t, probs, "limits.leverage 9 is over limits.max_leverage 5")
}

func TestReadPosition(t *testing.T) {
	p, err := readPosition(longDrawing("a"))
	require.NoError(t, err)
	assert.Equal(t, drawnPosition{Side: orderplan.Long, Entry: 65000, Stop: 64000, Target: 67000}, p)
	p, err = readPosition(Drawing{ID: "b", Body: json.RawMessage(`{"kind":"short-position","points":[{"price":100}],"style":{"stopLevel":2,"profitLevel":5}}`)})
	require.NoError(t, err)
	assert.Equal(t, drawnPosition{Side: orderplan.Short, Entry: 100, Stop: 102, Target: 95}, p)
	_, err = readPosition(Drawing{ID: "c", Body: json.RawMessage(`{"kind":"rectangle"}`)})
	assert.ErrorContains(t, err, "is a rectangle")
	_, err = readPosition(Drawing{ID: "d", Body: json.RawMessage(`{"kind":"long-position","points":[{"price":100}]}`)})
	assert.ErrorContains(t, err, "has no stop and target")
}

func TestEvents(t *testing.T) {
	stream := ": ping\n\nevent: ready\ndata: {\"confirmed\":[{\"id\":\"p-1\",\"status\":\"confirmed\",\"hash\":\"h\"}]}\n\n" +
		"event: decided\ndata: {\"id\":\"p-2\",\"status\":\"rejected\",\"hash\":\"h\"}\n\n"
	var got []decision
	require.NoError(t, readEvents(strings.NewReader(stream), func(ev event) { got = append(got, decisions(ev)...) }))
	assert.Equal(t, []decision{{ID: "p-1", Status: "confirmed", Hash: "h"}, {ID: "p-2", Status: "rejected", Hash: "h"}}, got)
}

func TestJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := OpenJournal(path)
	require.NoError(t, err)
	require.NoError(t, j.Started("a", "h1"))
	require.NoError(t, j.Started("b", "h2"))
	require.NoError(t, j.Reported("b", "placed"))
	again, err := OpenJournal(path)
	require.NoError(t, err)
	assert.Equal(t, "started", again.State("a"))
	assert.Equal(t, "reported", again.State("b"))
	assert.Equal(t, "", again.State("c"))
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

// A drawing becomes a checked ticket on the exchange's numbers, and goes to
// kdraigo for the user.
func TestPropose(t *testing.T) {
	e, srv, _ := rig(t)
	ctx := context.Background()
	p, tk, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	assert.Equal(t, StatusPending, p.Status)
	require.Len(t, srv.proposed, 1)
	in := srv.proposed[0]
	assert.Equal(t, "bybit_futures", in.Venue)
	assert.Equal(t, "BTCUSDT", in.Pair)
	assert.Equal(t, "ai_1", in.DrawingID)
	assert.Equal(t, int64(7), in.DrawingVersion)
	assert.Equal(t, "pc-test0001", in.Executor)
	assert.False(t, in.AutoAllowed)
	assert.True(t, tk.OK)
	assert.Equal(t, orderplan.Long, tk.Side)
	assert.InDelta(t, 0.01, tk.Qty, 1e-9, "10 USDT at risk over a 1,000 stop")
	assert.Equal(t, 64000.0, tk.Stop)
	assert.Equal(t, 67000.0, tk.Target)
	assert.Equal(t, orderplan.Hash(*tk), tk.Hash)

	_, _, err = e.Propose(ctx, ProposeInput{DrawingID: "dw_2", EntryType: orderplan.AtMarket})
	assert.ErrorContains(t, err, "is a trend-line")
	_, _, err = e.Propose(ctx, ProposeInput{DrawingID: "nope", EntryType: orderplan.AtMarket})
	assert.ErrorContains(t, err, `drawing "nope" is not on the bybit_futures BTCUSDT chart`)
	_, _, err = e.Propose(ctx, ProposeInput{DrawingID: "ai_1"})
	assert.ErrorContains(t, err, "entry_type: market or limit, as the user chose")
	_, _, err = e.Propose(ctx, ProposeInput{Venue: "binance_futures", Pair: "BTCUSDT", DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	assert.ErrorContains(t, err, "orders from this computer go to bybit_futures")

	// A plan that does not pass is not proposed; the checks say why.
	e.cfg.Limits.MaxPositionValue = 3
	_, tk, err = e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	var refused *Refused
	require.ErrorAs(t, err, &refused)
	assert.False(t, tk.OK)
	assert.Contains(t, err.Error(), "nothing was proposed")
	assert.Len(t, srv.proposed, 1)

	// Limits on Binance wait for a testnet answer.
	e.cfg.Venue = "binance_futures"
	_, _, err = e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtLimit})
	assert.ErrorContains(t, err, "a limit entry is placed on Bybit only for now")
}

// A confirmed proposal is re-checked, placed, journalled and reported, once.
func TestHandle_Places(t *testing.T) {
	e, srv, x := rig(t)
	ctx := context.Background()
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)

	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)
	assert.Equal(t, []string{"BUY MARKET 0.01", "bracket SELL 0.01 tp 67000 sl 64000"}, x.placed)
	doc := srv.results[p.ID]
	assert.True(t, doc.Result.Protected)
	assert.Equal(t, "reported", e.journal.State(p.ID))

	e.Handle(ctx, p.ID)
	assert.Len(t, srv.reports, 1, "never twice")
	assert.Len(t, x.placed, 2)

	// A pending (not confirmed) proposal is not placed.
	p2, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	e.Handle(ctx, p2.ID)
	assert.Len(t, x.placed, 2)
}

// Protection failed, so the executor closed at market: the closing order is
// reported to the live console with the entry. Without it the console kept a
// position the venue no longer held (bug #69).
func TestHandle_FlattenedCloseIsReported(t *testing.T) {
	e, srv, x := rig(t)
	x.bracketErr = errors.New("live trading refused at the cap gate")
	pub := &publisher{}
	console, err := OpenConsole(filepath.Join(t.TempDir(), "console.json"), func(string) telemetry.Publisher { return pub })
	require.NoError(t, err)
	e.WithConsole(console)
	ctx := context.Background()
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)

	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{"BUY MARKET 0.01", "bracket SELL 0.01 tp 67000 sl 64000", "SELL MARKET 0.01"}, x.placed)
	assert.True(t, srv.results[p.ID].Result.Flattened)
	assert.Equal(t, []string{"o-1 FILLED", "o-3 FILLED"}, pub.orders, "the entry and the close, so the console is flat too")
}

// The price moved past the tolerance after the yes: nothing is placed, and
// the checks say why.
func TestHandle_Recheck(t *testing.T) {
	e, srv, x := rig(t)
	ctx := context.Background()
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	x.mark = 65600 // +0.9%, past the 0.3% drift

	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{p.ID + " failed"}, srv.reports)
	assert.Empty(t, x.placed)
	doc := srv.results[p.ID]
	assert.Contains(t, doc.Error, "the market or the account changed since it was confirmed")
	assert.Contains(t, failedChecks(doc.Checks), "price")
	assert.Equal(t, "reported", e.journal.State(p.ID))
}

// A ticket that is not the one confirmed is never placed.
func TestHandle_HashMismatch(t *testing.T) {
	e, srv, x := rig(t)
	ctx := context.Background()
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	var changed orderplan.Ticket
	require.NoError(t, json.Unmarshal(srv.proposals[p.ID].Ticket, &changed))
	changed.Qty = 1 // changed after the yes
	srv.proposals[p.ID].Ticket, _ = json.Marshal(changed)
	e.Handle(ctx, p.ID)
	assert.Empty(t, x.placed)
	assert.Contains(t, srv.results[p.ID].Error, "does not match the one confirmed")
}

// Before a new position: a leftover exit is cancelled; a resting entry stops it.
func TestHandle_ClearsOld(t *testing.T) {
	e, srv, x := rig(t)
	ctx := context.Background()
	x.open = []*types.Order{{ID: "old-sl", Side: types.OrderSideSell, Type: types.OrderTypeStopLoss, ReduceOnly: true}}
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	e.Handle(ctx, p.ID)
	assert.Equal(t, []string{"old-sl"}, x.cancelled)
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)

	e, srv, x = rig(t)
	x.open = []*types.Order{{ID: "entry", Side: types.OrderSideBuy, Type: types.OrderTypeLimit, Quantity: 0.02}}
	p, _, err = e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	e.Handle(ctx, p.ID)
	assert.Empty(t, x.placed)
	assert.Contains(t, srv.results[p.ID].Error, "an order already rests on BTCUSDT (BUY LIMIT 0.02): cancel it first")
}

// Placing began and this computer stopped before reporting: the next run
// does not place again, it looks at the exchange.
func TestHandle_Recovers(t *testing.T) {
	e, srv, x := rig(t)
	ctx := context.Background()
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	require.NoError(t, e.journal.Started(p.ID, p.Hash))
	x.positions = []*types.Position{{Symbol: "BTCUSDT", Side: "LONG", Size: 0.01}}

	e.Handle(ctx, p.ID)
	assert.Empty(t, x.placed, "never placed again")
	assert.Equal(t, []string{p.ID + " placed"}, srv.reports)
	doc := srv.results[p.ID]
	assert.True(t, doc.Recovered)
	assert.Contains(t, doc.Error, "a position is open on BTCUSDT: check that its stop is on the exchange")
}

// The stream: confirmed proposals in "ready" are placed; a refused stream
// (no plan with AI orders) is told, not retried in a tight loop.
func TestRun(t *testing.T) {
	e, srv, x := rig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, _, err := e.Propose(ctx, ProposeInput{DrawingID: "ai_1", EntryType: orderplan.AtMarket})
	require.NoError(t, err)
	srv.confirm(p.ID)
	pr, pw := io.Pipe()
	srv.events = pr
	go e.Run(ctx)
	_, err = fmt.Fprintf(pw, "event: ready\ndata: {\"confirmed\":[{\"id\":%q,\"status\":\"confirmed\"}]}\n\n", p.ID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.reports) == 1
	}, 2*time.Second, 10*time.Millisecond)
	on, _ := e.Connection()
	assert.True(t, on)
	assert.Equal(t, []string{"BUY MARKET 0.01", "bracket SELL 0.01 tp 67000 sl 64000"}, x.placed)

	// The stream ends; the next one is refused for the plan.
	pw.Close()
	require.Eventually(t, func() bool {
		on, last := e.Connection()
		return !on && strings.Contains(last, "part of Plus and Pro")
	}, 5*time.Second, 10*time.Millisecond)
}

func TestExecutorID(t *testing.T) {
	dir := t.TempDir()
	id, err := ExecutorID(dir)
	require.NoError(t, err)
	assert.Regexp(t, `^pc-[0-9a-f]{12}$`, id)
	again, err := ExecutorID(dir)
	require.NoError(t, err)
	assert.Equal(t, id, again, "kept")
}

var _ = errors.New
