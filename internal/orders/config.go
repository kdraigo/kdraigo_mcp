// Package orders is the executor for orders from the user's AI: the part of
// the local kdraigo-mcp that holds the user's exchange keys, builds a checked
// ticket from a position drawing (dev_sdk/orderplan), and places only what the
// user confirmed on kdraigo.com (dev_sdk/orderexec). kdraigo never sees the
// keys: they stay in a file on this computer, and kdraigo's servers only pass
// proposals and the user's answers (research_service /orders).
package orders

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kdraigo/dev_sdk/orderplan"
	"github.com/kdraigo/dev_sdk/types"
)

// ConfigEnv names another config file than ~/.kdraigo/orders.yaml.
const ConfigEnv = "KDRAIGO_ORDERS"

// Config is ~/.kdraigo/orders.yaml. Every limit is the user's own: none has
// a default, and an unset one refuses every order (orderplan).
type Config struct {
	// Venue is where orders go: bybit_futures or binance_futures.
	Venue string `yaml:"venue"`
	// Network is testnet or mainnet. Mainnet also needs KDRAIGO_LIVE_ARMED=1
	// in the MCP server's environment (the SDK's arming gate).
	Network string `yaml:"network"`
	// KeyFile holds api_key and api_secret, readable by the user only.
	KeyFile string `yaml:"key_file"`
	// AutoAllowed is this computer's consent to place without asking, when
	// the user's switch on kdraigo.com is on too.
	AutoAllowed bool `yaml:"auto_allowed"`
	// Executor is where the orders are placed: empty, by the executor in this
	// kdraigo-mcp with the key here; "remote", by the user's order manager on a
	// server of theirs (github.com/kdraigo/order_manager), which holds the key
	// and the limits. This computer then keeps no exchange key.
	Executor string `yaml:"executor"`
	// Manager is the order manager's id with executor: remote; empty or
	// "active" is the one connected.
	Manager   string       `yaml:"manager"`
	Limits    LimitsConfig `yaml:"limits"`
	Tolerance struct {
		PriceDrift float64 `yaml:"price_drift"`
		RiskSlack  float64 `yaml:"risk_slack"`
	} `yaml:"tolerance"`

	path string
}

// LimitsConfig is orderplan.Limits as the file spells it.
type LimitsConfig struct {
	RiskPerTrade     float64 `yaml:"risk_per_trade"`
	MaxPositionValue float64 `yaml:"max_position_value"`
	Leverage         float64 `yaml:"leverage"`
	MaxLeverage      float64 `yaml:"max_leverage"`
	MaxOpenPositions int     `yaml:"max_open_positions"`
	MaxDailyLoss     float64 `yaml:"max_daily_loss"`
	MaxEntryGap      float64 `yaml:"max_entry_gap"`
	MaxLimitGap      float64 `yaml:"max_limit_gap"`
	MinStopGap       float64 `yaml:"min_stop_gap"`
}

// Plan is the limits as the checker takes them.
func (l LimitsConfig) Plan() orderplan.Limits {
	return orderplan.Limits{
		RiskPerTrade: l.RiskPerTrade, MaxNotional: l.MaxPositionValue, Leverage: l.Leverage, MaxLeverage: l.MaxLeverage,
		MaxOpenPositions: l.MaxOpenPositions, MaxDailyLoss: l.MaxDailyLoss, MaxEntryGap: l.MaxEntryGap,
		MaxLimitGap: l.MaxLimitGap, MinStopGap: l.MinStopGap,
	}
}

// Tolerance is how far the market may move between confirming and placing.
func (c *Config) PlanTolerance() orderplan.Tolerance {
	return orderplan.Tolerance{PriceDrift: c.Tolerance.PriceDrift, RiskSlack: c.Tolerance.RiskSlack}
}

// Path is the file the config was read from.
func (c *Config) Path() string { return c.path }

// Mainnet: real funds.
func (c *Config) Mainnet() bool { return c.Network == "mainnet" }

// Environment is the SDK's name for the venue and network.
func (c *Config) Environment() types.Environment {
	switch {
	case c.Venue == "bybit_futures" && c.Mainnet():
		return types.EnvRealBybitFutures
	case c.Venue == "bybit_futures":
		return types.EnvTestBybitFutures
	case c.Venue == "binance_futures" && c.Mainnet():
		return types.EnvRealBinanceFutures
	default:
		return types.EnvTestBinanceFutures
	}
}

// ErrNotSetUp: there is no orders file.
var ErrNotSetUp = errors.New("orders from your AI are not set up on this computer")

// DefaultPath is ~/.kdraigo/orders.yaml, or KDRAIGO_ORDERS.
func DefaultPath() string {
	if p := os.Getenv(ConfigEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kdraigo", "orders.yaml")
	}
	return filepath.Join(home, ".kdraigo", "orders.yaml")
}

// Load reads the orders file. A missing file is ErrNotSetUp; a file with
// problems loads, and Problems lists them.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotSetUp
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c := &Config{path: path}
	if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Venue = strings.TrimSpace(c.Venue)
	c.Network = strings.TrimSpace(c.Network)
	return c, nil
}

// Problems is what stops orders, one sentence each; none means ready.
// Remote: an order manager on a server places the orders (executor: remote).
func (c *Config) Remote() bool { return c.Executor == "remote" }

func (c *Config) Problems() []string {
	var p []string
	if c.Executor != "" && c.Executor != "remote" {
		return []string{fmt.Sprintf("executor %q: remote (your order manager on a server), or leave it out (this computer)", c.Executor)}
	}
	if c.Remote() {
		// The key and the limits are the order manager's, on its server.
		return nil
	}
	if c.Venue != "bybit_futures" && c.Venue != "binance_futures" {
		p = append(p, fmt.Sprintf("venue %q: bybit_futures or binance_futures", c.Venue))
	}
	if c.Network != "testnet" && c.Network != "mainnet" {
		p = append(p, fmt.Sprintf("network %q: testnet or mainnet", c.Network))
	}
	if c.KeyFile == "" {
		p = append(p, "key_file: the file with the exchange key (api_key, api_secret)")
	} else if err := checkKeyFile(c.keyPath()); err != nil {
		p = append(p, err.Error())
	}
	l := c.Limits
	need := func(name string, v float64) {
		if v <= 0 {
			p = append(p, fmt.Sprintf("limits.%s is not set: every limit is yours to choose, none has a default", name))
		}
	}
	need("risk_per_trade", l.RiskPerTrade)
	need("max_position_value", l.MaxPositionValue)
	need("leverage", l.Leverage)
	need("max_leverage", l.MaxLeverage)
	need("max_open_positions", float64(l.MaxOpenPositions))
	need("max_daily_loss", l.MaxDailyLoss)
	need("max_entry_gap", l.MaxEntryGap)
	if l.Leverage > l.MaxLeverage && l.MaxLeverage > 0 {
		p = append(p, fmt.Sprintf("limits.leverage %g is over limits.max_leverage %g", l.Leverage, l.MaxLeverage))
	}
	for name, v := range map[string]float64{"max_entry_gap": l.MaxEntryGap, "max_limit_gap": l.MaxLimitGap, "min_stop_gap": l.MinStopGap} {
		if v >= 1 {
			p = append(p, fmt.Sprintf("limits.%s %g: a fraction (0.005 is 0.5%%)", name, v))
		}
	}
	need("tolerance.price_drift", c.Tolerance.PriceDrift)
	need("tolerance.risk_slack", c.Tolerance.RiskSlack)
	return p
}

// Keys is the exchange key. It is never printed or sent to kdraigo.
type Keys struct {
	APIKey    string `yaml:"api_key"`
	APISecret string `yaml:"api_secret"`
}

// Keys reads the key file, refusing one others can read.
func (c *Config) Keys() (Keys, error) {
	var k Keys
	path := c.keyPath()
	if err := checkKeyFile(path); err != nil {
		return k, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return k, fmt.Errorf("key_file: %w", err)
	}
	if err := yaml.Unmarshal(raw, &k); err != nil {
		return k, fmt.Errorf("key_file %s: not YAML with api_key and api_secret", path)
	}
	if strings.TrimSpace(k.APIKey) == "" || strings.TrimSpace(k.APISecret) == "" {
		return k, fmt.Errorf("key_file %s: api_key and api_secret are both needed", path)
	}
	k.APIKey, k.APISecret = strings.TrimSpace(k.APIKey), strings.TrimSpace(k.APISecret)
	return k, nil
}

func (c *Config) keyPath() string {
	p := c.KeyFile
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) && c.path != "" {
		p = filepath.Join(filepath.Dir(c.path), p)
	}
	return p
}

// checkKeyFile refuses a key file that is missing or that other users can
// read. It looks at the file's mode only, never its contents.
func checkKeyFile(path string) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("key_file %s does not exist", path)
	}
	if err != nil {
		return fmt.Errorf("key_file: %w", err)
	}
	if st.IsDir() {
		return fmt.Errorf("key_file %s is a folder", path)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("key_file %s can be read by other users (mode %o): run chmod 600 %s", path, st.Mode().Perm(), path)
	}
	return nil
}

// Template is a new orders.yaml: every number left for the user to choose.
const Template = `# Orders from your AI, placed from this computer by kdraigo-mcp.
# kdraigo never sees this file or the key file. Every limit is yours: none has
# a default, and an order is refused while one is 0.
venue: bybit_futures        # bybit_futures or binance_futures (a sub-account of its own, no bot on it)
network: testnet            # testnet first; mainnet also needs KDRAIGO_LIVE_ARMED=1 in the MCP's environment
key_file: exchange-key.yaml # api_key and api_secret; chmod 600; next to this file unless a full path
auto_allowed: false         # true lets orders go without asking when your switch on kdraigo.com is on too
limits:
  risk_per_trade: 0         # USDT lost if the stop fills
  max_position_value: 0     # USDT, the largest position
  leverage: 0               # to open with (isolated margin)
  max_leverage: 0
  max_open_positions: 0
  max_daily_loss: 0         # USDT; no new position once today's realized loss reaches it
  max_entry_gap: 0          # fraction: how far a drawn market entry may be from the price now (0.005 = 0.5%)
  max_limit_gap: 0          # fraction: how far a limit entry may rest from the price now (needed for limits)
  min_stop_gap: 0           # fraction: the nearest a stop may be to the entry
tolerance:
  price_drift: 0            # fraction the price may move between confirming and placing (0.003 = 0.3%)
  risk_slack: 0             # fraction the loss at the stop may grow over risk_per_trade by then (0.1 = 10%)
`
