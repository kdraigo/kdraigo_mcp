package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// initialWallet matches entity.Wallet on the backtester side (it relies on default
// case-insensitive JSON matching since the struct has no json tags).
type initialWallet struct {
	Exchange string  `json:"Exchange"`
	Asset    string  `json:"Asset"`
	Balance  float64 `json:"Balance"`
}

type createSessionBody struct {
	Streams        []streamConfig  `json:"streams"`
	InitialWallets []initialWallet `json:"initial_wallets"`
}

func addCreateBacktestSession(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("create_backtest_session",
		mcp.WithDescription("Create a backtest session on the backtester_engine. Returns the session id used by run_backtest_stream."),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("Exchange ID, e.g. binance or bybit")),
		mcp.WithString("pair", mcp.Required(), mcp.Description("Trading pair, slash form, e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Required(), mcp.Description("Candle timeframe, e.g. 1m, 1h, 1d")),
		mcp.WithString("from", mcp.Required(), mcp.Description("Start time ISO 8601, e.g. 2026-01-01T00:00:00Z")),
		mcp.WithString("to", mcp.Required(), mcp.Description("End time ISO 8601, e.g. 2026-03-01T00:00:00Z")),
		mcp.WithString("asset", mcp.Required(), mcp.Description("Initial wallet quote asset, e.g. USDT")),
		mcp.WithNumber("initial_balance", mcp.Required(), mcp.Description("Initial wallet balance in the given asset")),
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

		body := createSessionBody{
			Streams: []streamConfig{{
				SessionID: uuid.New(),
				Exchange:  exchange,
				Pair:      pair,
				Timeframe: tf,
				From:      from,
				To:        to,
			}},
			InitialWallets: []initialWallet{{
				Exchange: exchange,
				Asset:    asset,
				Balance:  balance,
			}},
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

		ws, err := client.DialSessionWS(ctx, d.HTTP.Endpoint(), d.HTTP.Signer(), sessionID)
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
			resp, err := ws.Recv(ctx)
			if err != nil {
				return toolErr(fmt.Errorf("ws recv: %w", err)), nil
			}
			if resp.Status != "ok" {
				return toolErr(fmt.Errorf("engine error: %s", resp.Error)), nil
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
