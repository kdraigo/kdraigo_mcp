package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// RegisterBacktest adds create_backtest_session + run_backtest_stream.
func RegisterBacktest(s *server.MCPServer, d Deps) {
	addCreateBacktestSession(s, d)
	addRunBacktestStream(s, d)
}

// streamConfig matches data.StartSessionRequest on the backtester side.
type streamConfig struct {
	SessionID uuid.UUID `json:"sessionID"`
	Exchange  string    `json:"exchange"`
	Pair      string    `json:"pair"`
	Timeframe string    `json:"timeframe"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
}

// initialWallet mirrors the engine's dev.InitialWalletRequest, tag for tag.
//
// The names are not cosmetic. That DTO uses snake_case json tags, and Go's
// case-insensitive field matching does not cross underscores: "Exchange" binds
// to `exchange` by luck, but "MarketType" would never bind to `market_type` —
// it would decode as empty, and the session would silently run as spot on
// perpetual data. The engine's own DTO carries a comment warning about exactly
// this. Copy its tags rather than relying on the matcher.
type initialWallet struct {
	Exchange string  `json:"exchange"`
	Asset    string  `json:"asset"`
	Balance  float64 `json:"balance"`

	// MarketType is "spot" (the default) or "linear_perp" for USDT-margined
	// perpetuals; Leverage is the starting leverage for a perp wallet. Both
	// omitempty, so a spot session's payload is byte-identical to before.
	MarketType string  `json:"market_type,omitempty"`
	Leverage   float64 `json:"leverage,omitempty"`
}

// simulationOptions mirrors the engine's simulation block. Pointers throughout
// so "not specified" stays distinguishable from "explicitly zero" — asking for
// no funding is a real, meaningful request, and it is not the same as saying
// nothing.
type simulationOptions struct {
	MarginMode           string  `json:"margin_mode,omitempty"`
	PositionMode         string  `json:"position_mode,omitempty"`
	MarkPriceSource      string  `json:"mark_price_source,omitempty"`
	ContractSpecsVersion string  `json:"contract_specs_version,omitempty"`
	FundingEnabled       *bool   `json:"funding_enabled,omitempty"`
	DefaultLeverage      float64 `json:"default_leverage,omitempty"`
}

type createSessionBody struct {
	Streams        []streamConfig     `json:"streams"`
	InitialWallets []initialWallet    `json:"initial_wallets"`
	Simulation     *simulationOptions `json:"simulation,omitempty"`
}

func addCreateBacktestSession(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("create_backtest_session",
		mcp.WithDescription("Create a backtest session on the backtester_engine. Returns the session id used by run_backtest_stream."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("Exchange ID. Spot: binance, bybit. Perpetual futures: binance_futures, bybit_futures — these are separate exchanges with their own candle data, funding rates and margin ladders, not a mode of the spot ones.")),
		mcp.WithString("pair", mcp.Required(), mcp.Description("Trading pair, slash form, e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Candle timeframe. One of: 1m, 2m, 3m, 5m, 15m, 30m, 1h, 2h, 4h, 1d, 1w, 1M. Case-sensitive: 1m is one minute, 1M is one month.")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start time ISO 8601, e.g. 2026-01-01T00:00:00Z")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End time ISO 8601, e.g. 2026-03-01T00:00:00Z")),
		mcp.WithString("asset", mcp.Required(), mcp.Description("Initial wallet quote asset, e.g. USDT")),
		mcp.WithNumber("initial_balance", mcp.Required(), mcp.Description("Initial wallet balance in the given asset")),
		mcp.WithString("market_type", mcp.Description("Execution model: 'spot' (default) or 'linear_perp' for USDT-margined perpetuals. Defaults to linear_perp when the exchange name ends in _futures, so naming binance_futures alone is enough."), mcp.Enum("spot", "linear_perp")),
		mcp.WithNumber("leverage", mcp.Description("Starting leverage for a linear_perp wallet. Default 1 (unleveraged). Ignored on spot.")),
		mcp.WithString("mark_price_source", mcp.Description("What liquidation triggers on: 'mark_series' (default, and what a real exchange uses) or 'trade_close'. The two diverge by up to 1.3% at the bar low on real data, so trade_close misses liquidations a real venue would have hit."), mcp.Enum("mark_series", "trade_close")),
		mcp.WithBoolean("funding_enabled", mcp.Description("Charge perpetual funding. Default true. Turning it off is a directional bias, not a simplification — it flatters whichever side was being paid over the window.")),
		mcp.WithString("contract_specs_version", mcp.Description("Pins the maintenance-margin ladder so a stored result stays reproducible after the exchange revises its tiers, e.g. binance_futures/2026-08-28. Empty uses the newest capture.")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		exchange, err := req.RequireString("exchange")
		if err != nil {
			return toolErr(err), nil
		}
		pair, err := req.RequireString("pair")
		if err != nil {
			return toolErr(err), nil
		}
		tf, err := req.RequireString("timeframe")
		if err != nil {
			return toolErr(err), nil
		}
		fromStr, err := req.RequireString("from")
		if err != nil {
			return toolErr(err), nil
		}
		toStr, err := req.RequireString("to")
		if err != nil {
			return toolErr(err), nil
		}
		asset, err := req.RequireString("asset")
		if err != nil {
			return toolErr(err), nil
		}
		balance, err := req.RequireFloat("initial_balance")
		if err != nil {
			return toolErr(err), nil
		}

		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return toolErr(fmt.Errorf("from: %w", err)), nil
		}
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return toolErr(fmt.Errorf("to: %w", err)), nil
		}

		// A _futures exchange is a perpetual venue by name, so naming one is
		// enough. Without this default, asking for binance_futures and
		// forgetting market_type produced a spot wallet running on perpetual
		// data — no error, and a run whose fees, leverage and liquidation
		// were all silently wrong.
		marketType := req.GetString("market_type", "")
		if marketType == "" && strings.HasSuffix(exchange, "_futures") {
			marketType = "linear_perp"
		}

		wallet := initialWallet{
			Exchange:   exchange,
			Asset:      asset,
			Balance:    balance,
			MarketType: marketType,
			Leverage:   req.GetFloat("leverage", 0),
		}

		var sim *simulationOptions
		if marketType == "linear_perp" {
			// v1 supports isolated margin and one-way positions only. Stating
			// them makes the run self-describing rather than dependent on
			// whatever the engine currently defaults to.
			sim = &simulationOptions{
				MarginMode:           "isolated",
				PositionMode:         "one_way",
				MarkPriceSource:      req.GetString("mark_price_source", ""),
				ContractSpecsVersion: req.GetString("contract_specs_version", ""),
			}
			if args := req.GetArguments(); args != nil {
				if _, ok := args["funding_enabled"]; ok {
					enabled := req.GetBool("funding_enabled", true)
					sim.FundingEnabled = &enabled
				}
			}
		}

		body := createSessionBody{
			Streams: []streamConfig{{
				SessionID: uuid.New(),
				Exchange:  exchange,
				Pair:      pair,
				Timeframe: tf,
				From:      from,
				To:        to,
			}},
			InitialWallets: []initialWallet{wallet},
			Simulation:     sim,
		}

		respBody, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleBacktester, http.MethodPost, client.Backtester, "/api/v1/dev/session", nil, body)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("create_backtest_session", status, respBody)), nil
		}
		return toolText(string(respBody)), nil
	})
}

type tickPayload struct {
	Tick   json.RawMessage   `json:"tick"`
	Done   bool              `json:"done"`
	Orders []json.RawMessage `json:"orders"`
}

func addRunBacktestStream(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("run_backtest_stream",
		mcp.WithDescription("Drive a backtest session to completion by sending `next` actions over WS. Returns aggregate stats; no strategy logic is executed server-side. Use this to bake-out a passive bot or to confirm the data range is loadable."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID returned by create_backtest_session")),
		mcp.WithNumber("max_candles", mcp.Description("Safety cap on candles processed; 0 means no cap"), mcp.DefaultNumber(0)),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		maxCandles := req.GetInt("max_candles", 0)

		ws, err := client.DialSessionWS(ctx, d.HTTP.BacktesterEndpoint(), d.HTTP.Signer(), sessionID)
		if err != nil {
			return toolErr(err), nil
		}
		defer ws.Close()

		total := 0
		fills := 0
		for {
			if maxCandles > 0 && total >= maxCandles {
				break
			}
			if err := ws.Send(ctx, client.WSAction{Action: "next"}); err != nil {
				return toolErr(fmt.Errorf("ws send: %w", err)), nil
			}
			// Read until the matching "next" response, skipping async keepalive/
			// progress frames the engine pushes (~every 15s) so they aren't
			// miscounted as candles.
			var resp *client.WSResponse
			for {
				resp, err = ws.Recv(ctx)
				if err != nil {
					return toolErr(fmt.Errorf("ws recv: %w", err)), nil
				}
				if resp.Status != "ok" {
					return toolErr(fmt.Errorf("engine error: %s", resp.Error)), nil
				}
				if resp.Action == "next" {
					break
				}
				// progress / heartbeat — ignore and keep waiting for the tick.
			}
			var tp tickPayload
			if err := json.Unmarshal(resp.Data, &tp); err != nil {
				return toolErr(fmt.Errorf("decode tick payload: %w", err)), nil
			}
			fills += len(tp.Orders)
			if tp.Done {
				break
			}
			total++
		}

		summary, _ := json.Marshal(map[string]any{
			"total_candles": total,
			"fills":         fills,
			"session_id":    sessionID,
		})
		return toolText(string(summary)), nil
	})
}

// Quiet unused import warnings in case of future refactor.
var _ = url.Values{}
