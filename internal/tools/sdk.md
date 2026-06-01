# kdraigo dev_sdk — Reference

Go SDK for writing trading strategies that run against the kdraigo backtester or a live exchange. Module path: `github.com/kdraigo/flow_v1/dev_sdk`.

## entry

```go
import (
    "context"
    sdk  "github.com/kdraigo/flow_v1/dev_sdk"
    "github.com/kdraigo/flow_v1/dev_sdk/types"
)

s, err := sdk.New(&types.Config{...})
s.SetOnCandle(func(ctx *types.Context, c *types.Candle) { ... })
s.SetOnCandleFor(types.Timeframe1h, func(ctx *types.Context, c *types.Candle) { ... })
s.SetOnOrderUpdate(func(ctx *types.Context, o *types.Order) { ... })
s.SetOnComplete(func() { /* backtest only */ })
err = s.Start(context.Background())
```

`sdk.New` picks the adapter from `cfg.Environment`:

| Environment value | Adapter |
|---|---|
| `types.EnvBacktest` | backtester_engine via WS to `/api/v1/dev/session/ws` |
| `types.EnvRealBybit` / `types.EnvTestBybit` | Bybit Spot live/testnet |
| `types.EnvRealBinance` / `types.EnvTestBinance` | Binance live/testnet |

## types

```go
types.Config{
    Environment: types.EnvBacktest,
    Timeframes:  []types.Timeframe{types.Timeframe1h, types.Timeframe4h},
    Credentials: types.Credentials{
        KeyID:      os.Getenv("KDRAIGO_KEY_ID"),
        PrivateKey: os.Getenv("KDRAIGO_PRIVATE_KEY"), // hex-encoded ed25519 private key
    },
    Backtest: &types.BacktestOptions{
        Endpoint:           "http://localhost:4000", // override per environment
        SessionName:        "v1",
        RequestedExchanges: []string{"binance"},
        Assets:             []string{"BTC/USDT"},
        Wallets:            map[string]float64{"USDT": 10000},
        StartTime:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        EndTime:            time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
    },
}
```

Key types you will touch:

- `types.Candle` — `Exchange`, `Symbol`, `Timeframe`, `OpenTime`, `CloseTime`, `Open/High/Low/Close/Volume`, `IsComplete`.
- `types.Order` — `ID`, `Side` (BUY/SELL), `Type` (MARKET/LIMIT), `Status` (NEW/PARTIALLY_FILLED/FILLED/CANCELED/REJECTED), `Price`, `Quantity`, `FilledQty`, `AveragePrice`.
- `types.OrderRequest` — what you pass to `ctx.PlaceOrder`. Includes `Reason` (`map[string]any`) and `Logs` (`[]string`) for telemetry.
- `types.Context` — provided to every callback; exposes `PlaceOrder`, `CancelOrder`, `Now`, `GetIndicator`, the `Config`, and the `Trader` (paper or live).
- `types.Timeframe` — string-backed; constants `Timeframe1m`, `Timeframe5m`, `Timeframe15m`, `Timeframe30m`, `Timeframe1h`, `Timeframe2h`, `Timeframe4h`, `Timeframe1d`.

## clock

`ctx.Now()` (inside callbacks) and `s.Now()` (before `Start`) return the strategy clock:

- **Live** — wall clock.
- **Backtest** — close time of the last dispatched closed candle (initialised to `Config.Backtest.From`).

Strategies must use `ctx.Now()` instead of `time.Now()` for "current time" so they remain portable across modes. Real `time.Now()` is fine for telemetry / signing / WS timeouts.

The backtest clock is **monotonic** and advances exactly once per dispatched closed candle. Historical fetches never advance it.

## history fetch

```go
candles, err := s.GetCandles(ctx, "binance", "BTC/USDT", 300, types.Timeframe1m)
candles, err := s.GetCandlesFromTo(ctx, "binance", "BTC/USDT", from, to, types.Timeframe15m)
```

In backtest, `to` must not exceed `ctx.Now()` — the engine rejects future fetches with `historical candles not available at simulated time T`. This prevents look-ahead leakage.

## callbacks

| Method | Fired when |
|---|---|
| `SetOnCandle(fn)` | Every **closed** candle across all timeframes |
| `SetOnCandleFor(tf, fn)` | Closed candle on a specific timeframe only |
| `SetOnOrderUpdate(fn)` | Order status change |
| `SetOnComplete(fn)` | Backtest finished (no-op in live) |

Live adapters drop in-progress klines before they reach the SDK pipeline, so `OnCandle` fires exactly once per close. In backtest the engine only emits closed historical candles.

## placing orders

```go
order, err := ctx.PlaceOrder(ctx.Ctx, &types.OrderRequest{
    Exchange: "binance",
    Asset:    "USDT",
    Pair:     "BTC/USDT",
    Side:     types.SideBuy,
    Type:     types.OrderTypeMarket,
    Quantity: 0.01,
    Reason:   map[string]any{"signal": "rsi_oversold", "rsi": 28.4},
    Logs:     []string{"computed rsi=28.4 over 14 closes"},
})
```

`Reason` and `Logs` are forwarded to the backtester engine and persisted alongside the order. Use them — Claude can read them back via `get_session_detail` and explain trades after the fact.

## auth

Strategies authenticate to the platform with the same Ed25519 keypair the MCP server uses. Set `KDRAIGO_KEY_ID` and `KDRAIGO_PRIVATE_KEY` in env; the scaffolded templates read them. Never commit either value.

## known gaps

- `CancelOrder` WS round-trip in backtest engine: implemented; paper wallet cancel stamps `time.Now()` rather than simulated clock — determinism on cancel timestamps not yet guaranteed.
- `IndicatorManager` only auto-updates from streamed candles. If you warm up via `GetCandles*`, feed the warmup candles into your indicators manually.
- Only RSI is currently exposed via the `Indicator` interface; other talib indicators require direct calls.
