package orders

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kdraigo/dev_sdk/orderexec"
	"github.com/kdraigo/dev_sdk/telemetry"
	"github.com/kdraigo/dev_sdk/types"
)

// Console is the user's AI orders as a live-console session of their own
// (the user's D7): every order the executor places, closes, moves or cancels
// is reported to live_trades like a bot's, fills are followed while the
// executor runs, and the session shows its balance and P&L.
//
// It is also the executor's memory of what AI orders opened (the user's D4:
// AI orders use an account of their own, never a bot's): a position or an
// order this computer did not place means another trader uses the account,
// and the executor refuses to open beside it, or to act on it.
type Console struct {
	path string
	pub  telemetry.Publisher
	now  func() time.Time

	mu    sync.Mutex
	state consoleState
}

type consoleState struct {
	SessionID   string `json:"session_id"`
	InitialSent bool   `json:"initial_sent"`
	// Orders are the AI orders' orders still to follow, by id.
	Orders map[string]trackedOrder `json:"orders"`
	// Symbols are the contracts where AI orders have a position or an order.
	Symbols map[string]bool `json:"symbols"`
}

type trackedOrder struct {
	Symbol string            `json:"symbol"`
	Status types.OrderStatus `json:"status"`
	Filled float64           `json:"filled"`
}

// PublisherFor makes the session's publisher once its id is known.
type PublisherFor func(sessionID string) telemetry.Publisher

// legacyConsoleFile is the one console file every account on a computer
// shared before bug #65.
const legacyConsoleFile = "ai-orders-console.json"

// ConsoleFile is the console file of the account a key belongs to,
// ai-orders-console-<key>.json in dir. One file per computer, as before, let
// two accounts' MCPs on one computer (a wallet and a Google login) share one
// live-console session: live_trades gives a session to the account that
// first wrote to it and refused the other's heartbeat every 5 s (wiki bug
// #65). The shared file goes to the account that owns its session: owns
// reports whether live_trades takes this key's events for it. A file whose
// owner is not known yet (no answer) is used as it is, and asked about at
// the next start.
func ConsoleFile(ctx context.Context, dir, keyID string, owns func(ctx context.Context, sessionID string) (bool, error)) string {
	mine := AccountConsoleFile(dir, keyID)
	legacy := filepath.Join(dir, legacyConsoleFile)
	if _, err := os.Stat(mine); err == nil {
		return mine
	}
	raw, err := os.ReadFile(legacy)
	if err != nil {
		return mine // none to take over
	}
	var st consoleState
	if json.Unmarshal(raw, &st) == nil && st.SessionID != "" {
		ok, err := owns(ctx, st.SessionID)
		if err != nil {
			return legacy
		}
		if !ok {
			return mine // another account's: it stays for that account
		}
	}
	if os.Rename(legacy, mine) != nil {
		return legacy
	}
	return mine
}

// keyTag names an account's files on this computer after its key.
func keyTag(keyID string) string {
	sum := sha256.Sum256([]byte(keyID))
	return hex.EncodeToString(sum[:6])
}

// AccountConsoleFile is the console file of the account a key belongs to,
// without taking over the shared one (ConsoleFile does that, once, in the
// process that places).
func AccountConsoleFile(dir, keyID string) string {
	return filepath.Join(dir, "ai-orders-console-"+keyTag(keyID)+".json")
}

// ReadConsole reads a console file as it is now, to check against: a
// standby executor's view of what AI orders opened (wiki bug #76). It reports
// nothing and keeps nothing; no file yet is an empty memory.
func ReadConsole(path string) (*Console, error) {
	c := &Console{path: path, now: time.Now}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &c.state); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.state.Orders == nil {
		c.state.Orders = map[string]trackedOrder{}
	}
	if c.state.Symbols == nil {
		c.state.Symbols = map[string]bool{}
	}
	return c, nil
}

// OpenConsole reads the console's state (ConsoleFile), making the session id
// the first time.
func OpenConsole(path string, pubFor PublisherFor) (*Console, error) {
	c := &Console{path: path, now: time.Now}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &c.state); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.state.SessionID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		b[6], b[8] = (b[6]&0x0f)|0x40, (b[8]&0x3f)|0x80 // a version-4 UUID
		h := hex.EncodeToString(b)
		c.state.SessionID = h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	}
	if c.state.Orders == nil {
		c.state.Orders = map[string]trackedOrder{}
	}
	if c.state.Symbols == nil {
		c.state.Symbols = map[string]bool{}
	}
	c.pub = pubFor(c.state.SessionID)
	return c, c.save()
}

// SessionID is the live-console session's id.
func (c *Console) SessionID() string { return c.state.SessionID }

func (c *Console) save() error {
	raw, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func symbolKey(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "/", "")) }

// Owns: AI orders have a position or an order on this contract.
func (c *Console) Owns(symbol string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Symbols[symbolKey(symbol)]
}

// OwnsOrder: this order is an AI order's.
func (c *Console) OwnsOrder(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.state.Orders[id]
	return ok
}

// Track records orders AI orders placed (or that changed) and reports them.
func (c *Console) Track(orders ...*types.Order) {
	c.mu.Lock()
	for _, o := range orders {
		if o == nil || o.ID == "" {
			continue
		}
		sym := symbolKey(o.Symbol)
		c.state.Symbols[sym] = true
		if final(o.Status) {
			delete(c.state.Orders, o.ID)
		} else {
			c.state.Orders[o.ID] = trackedOrder{Symbol: sym, Status: o.Status, Filled: o.FilledQty}
		}
	}
	_ = c.save()
	c.mu.Unlock()
	for _, o := range orders {
		if o != nil && o.ID != "" {
			c.pub.PublishOrder(o, map[string]any{"source": "ai-order"}, nil)
		}
	}
}

func final(s types.OrderStatus) bool {
	return s == types.OrderStatusFilled || s == types.OrderStatusCanceled || s == types.OrderStatusRejected
}

// Start declares the session (and its first balance, once).
func (c *Console) Start(meta map[string]any, env string, acct *types.Account) {
	c.pub.PublishSessionMeta("AI orders", meta)
	c.mu.Lock()
	first := !c.state.InitialSent && acct != nil
	if first {
		c.state.InitialSent = true
		_ = c.save()
	}
	c.mu.Unlock()
	if first {
		c.pub.PublishInitialBalance(acct)
	}
}

// Heartbeat says the executor runs.
func (c *Console) Heartbeat(uptime time.Duration) {
	c.mu.Lock()
	n := len(c.state.Orders)
	c.mu.Unlock()
	c.pub.PublishHeartbeat(telemetry.HeartbeatMeta{UptimeSeconds: int64(uptime.Seconds()), OrdersPlaced: n})
}

// Stopped says the executor went away (the MCP closed); the next heartbeat
// makes the session run again.
func (c *Console) Stopped(reason string) { c.pub.PublishStopped(reason) }

// Follow reads every AI order still open and reports what changed (fills
// while this computer was off included), then the balance; contracts left
// with no position and no AI order are forgotten.
func (c *Console) Follow(ctx context.Context, venueFor func(context.Context, string) (orderexec.Venue, error), exchange string) error {
	c.mu.Lock()
	bySymbol := map[string][]string{}
	for id, o := range c.state.Orders {
		bySymbol[o.Symbol] = append(bySymbol[o.Symbol], id)
	}
	symbols := make([]string, 0, len(c.state.Symbols))
	for s := range c.state.Symbols {
		symbols = append(symbols, s)
	}
	c.mu.Unlock()
	sort.Strings(symbols)
	if len(symbols) == 0 {
		return nil
	}

	v, err := venueFor(ctx, symbols[0])
	if err != nil {
		return err
	}
	var changed []*types.Order
	for sym, ids := range bySymbol {
		for _, id := range ids {
			o, err := v.GetOrder(ctx, exchange, sym, id)
			if err != nil || o == nil {
				continue
			}
			if o.Symbol == "" {
				o.Symbol = sym
			}
			c.mu.Lock()
			prev := c.state.Orders[id]
			c.mu.Unlock()
			if o.Status != prev.Status || o.FilledQty != prev.Filled {
				changed = append(changed, o)
			}
		}
	}
	if len(changed) > 0 {
		c.Track(changed...)
	}

	positions, err := v.GetPositions(ctx, exchange)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, p := range positions {
		if p != nil && p.Size > 0 {
			held[symbolKey(p.Symbol)] = true
		}
	}
	c.mu.Lock()
	open := map[string]bool{}
	for _, o := range c.state.Orders {
		open[o.Symbol] = true
	}
	for s := range c.state.Symbols {
		if !held[s] && !open[s] {
			delete(c.state.Symbols, s)
		}
	}
	_ = c.save()
	c.mu.Unlock()

	if acct, err := v.GetAccount(ctx, exchange, orderexec.Settlement); err == nil {
		c.pub.PublishBalance(acct)
	}
	return nil
}

// foreign is what the account holds that AI orders did not place: positions
// on contracts AI orders never traded, and on symbol, orders that are not
// AI orders'. Empty means the account is AI orders' alone, as far as it shows.
func (c *Console) foreign(ctx context.Context, v orderexec.Venue, exchange, symbol string) ([]string, error) {
	positions, err := v.GetPositions(ctx, exchange)
	if err != nil {
		return nil, fmt.Errorf("positions: %w", err)
	}
	var out []string
	for _, p := range positions {
		if p == nil || p.Size == 0 || c.Owns(p.Symbol) {
			continue
		}
		out = append(out, fmt.Sprintf("a %s %s position of %g", strings.ToLower(p.Side), symbolKey(p.Symbol), p.Size))
	}
	if symbol != "" {
		open, err := v.ListOpenOrders(ctx, exchange, symbol)
		if err != nil {
			return nil, fmt.Errorf("open orders: %w", err)
		}
		for _, o := range open {
			if o == nil || c.OwnsOrder(o.ID) {
				continue
			}
			// An exit AI orders placed before this memory existed is not
			// foreign if AI orders hold the contract.
			if o.ReduceOnly && c.Owns(symbol) {
				continue
			}
			out = append(out, fmt.Sprintf("an order %s on %s (%s %s %g)", o.ID, symbolKey(symbol), o.Side, o.Type, o.Quantity))
		}
	}
	return out, nil
}

// errShared refuses an account another trader uses.
func errShared(what []string) error {
	return fmt.Errorf("this exchange account holds %s that this computer did not place: AI orders need an account of their own, with no bot or other trading on it (a sub-account)",
		strings.Join(what, ", "))
}
