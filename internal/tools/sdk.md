# kdraigo dev_sdk — Reference

Go SDK for writing trading strategies that run against the kdraigo backtester or a live exchange. Module path: `github.com/kdraigo/dev_sdk`.

## entry

```go
import (
    "context"
    sdk  "github.com/kdraigo/dev_sdk"
    "github.com/kdraigo/dev_sdk/types"
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

## indicators

~95 TA-Lib indicators are computed per timeframe from the candles the SDK has streamed. Access them through the timeframe-scoped manager:

```go
import (
    sdk "github.com/kdraigo/dev_sdk"
    "github.com/kdraigo/dev_sdk/indicators"
    "github.com/kdraigo/dev_sdk/types"
)

s.SetOnCandleFor(types.Timeframe1h, func(ctx *types.Context, c *types.Candle) {
    calc := s.IndicatorManagerFor(types.Timeframe1h) // tf MUST be in Config.Timeframes

    rsi, err := calc.RSI("binance", "BTC/USDT", "close", 14)
    if err != nil {
        return // not enough history yet — treat error as warm-up
    }
    latest := rsi[len(rsi)-1] // most recent value is the LAST element
})
```

Every method: `IndicatorManagerFor(tf).<Name>(exchange, symbol string, <params...>) (<series...> []float64, error)`.

Rules:

- Register the timeframe in `Config.Timeframes`, then read it with `IndicatorManagerFor(tf)`.
- Latest value is the **last** slice element: `series[len(series)-1]`. TA-Lib zero-fills the leading lookback region.
- On insufficient data (`len(points) <= period`) or unknown exchange/symbol the call returns an error — return early, it means warm-up isn't finished.
- `pt` (`pointType`) selects the input series for single-input indicators: `"close"` (default), `"open"`, `"high"`, `"low"`, `"volume"`. Indicators needing OHLC/HL/HLC/HLCV derive them internally and take **no** `pt`.
- `maType` uses re-exported constants: `indicators.TypeSMA`, `TypeEMA`, `TypeWMA`, `TypeDEMA`, `TypeTEMA`, `TypeTRIMA`, `TypeKAMA`, `TypeMAMA`, `TypeT3MA`.

Below, the leading `exchange, symbol` are omitted; `pt` = `pointType`; return is one `[]float64` + `error` unless noted.

### Overlap / moving averages

| Method | Params | Returns |
|---|---|---|
| `BB` | `pt, period int, deviation float64, maType` | upper, middle, lower |
| `DEMA` `EMA` `KAMA` `SMA` `TEMA` `TRIMA` `WMA` | `pt, period int` | series |
| `MA` | `pt, period int, maType` | series |
| `MAMA` | `pt, fastLimit, slowLimit float64` | mama, fama |
| `MaVp` | `pt, periods []float64, minPeriod, maxPeriod int, maType` | series |
| `MidPoint` | `pt, period int` | series |
| `MidPrice` | `period int` (HL) | series |
| `T3` | `pt, period int, vFactor float64` | series |
| `HTTrendline` | `pt` | series |
| `SAR` | `acceleration, maximum float64` (HL) | series |
| `SARExt` | 8 float64 SAR params (HL) | series |

### Momentum

| Method | Params | Returns |
|---|---|---|
| `ADX` `ADXR` `CCI` `DX` `MinusDI` `PlusDI` `WilliamsR` | `period int` (HLC) | series |
| `MinusDM` `PlusDM` `AroonOsc` | `period int` (HL) | series |
| `Aroon` | `period int` (HL) | aroonDown, aroonUp |
| `MFI` | `period int` (HLCV) | series |
| `BOP` | — (OHLC) | series |
| `CMO` `Momentum` `RSI` `ROC` `ROCP` `ROCR` `ROCR100` `Trix` | `pt, period int` | series |
| `APO` `PPO` | `pt, fastPeriod, slowPeriod int, maType` | series |
| `MACD` | `pt, fastPeriod, slowPeriod, signalPeriod int` | macd, signal, hist |
| `MACDExt` | `pt, fastPeriod int, fastMAType, slowPeriod int, slowMAType, signalPeriod int, signalMAType` | macd, signal, hist |
| `MACDFix` | `pt, signalPeriod int` | macd, signal, hist |
| `Stoch` | `fastKPeriod, slowKPeriod int, slowKMAType, slowDPeriod int, slowDMAType` (HLC) | slowK, slowD |
| `StochF` | `fastKPeriod, fastDPeriod int, fastDMAType` (HLC) | fastK, fastD |
| `StochRSI` | `pt, period, fastKPeriod, fastDPeriod int, fastDMAType` | fastK, fastD |
| `UltOsc` | `period1, period2, period3 int` (HLC) | series |

### Volume / volatility / price transform

| Method | Params | Returns |
|---|---|---|
| `OBV` | `pt` (price+volume) | series |
| `Ad` | — (HLCV) | series |
| `AdOsc` | `fastPeriod, slowPeriod int` (HLCV) | series |
| `ATR` `NATR` | `period int` (HLC) | series |
| `TRANGE` | — (HLC) | series |
| `AvgPrice` | — (OHLC) | series |
| `MedPrice` | — (HL) | series |
| `TypPrice` `WCLPrice` | — (HLC) | series |

### Cycle (Hilbert Transform)

| Method | Params | Returns |
|---|---|---|
| `HTDcPeriod` `HTDcPhase` `HTTrendMode` | `pt` | series |
| `HTPhasor` | `pt` | inPhase, quadrature |
| `HTSine` | `pt` | sine, leadSine |

### Statistics

| Method | Params | Returns |
|---|---|---|
| `LinearReg` `LinearRegAngle` `LinearRegIntercept` `LinearRegSlope` `TSF` `Var` | `pt, period int` | series |
| `StdDev` | `pt, period int, nbDev float64` | series |
| `Beta` `Correl` | `pt0, pt1, period int` | series |

**Math** — element-wise transforms take `pt` only and return a series: `Acos` `Asin` `Atan` `Ceil` `Cos` `Cosh` `Exp` `Floor` `Ln` `Log10` `Sin` `Sinh` `Sqrt` `Tan` `Tanh`. Operators: `Add` `Sub` `Mult` `Div` (`pt0, pt1`); `Max` `Min` `MaxIndex` `MinIndex` `Sum` (`pt, period int`); `MinMax` `MinMaxIndex` (`pt, period int` → two series).

Full reference with descriptions: `dev_sdk/indicators/README.md`.

## auth

Strategies authenticate to the platform with the same Ed25519 keypair the MCP server uses. Set `KDRAIGO_KEY_ID` and `KDRAIGO_PRIVATE_KEY` in env; the scaffolded templates read them. Never commit either value.

## known gaps

- `CancelOrder` WS round-trip in backtest engine: implemented; paper wallet cancel stamps `time.Now()` rather than simulated clock — determinism on cancel timestamps not yet guaranteed.
- `IndicatorManager` only auto-updates from streamed candles. If you warm up via `GetCandles*`, feed the warmup candles into your indicators manually.
- The `ctx.GetIndicator(name)` string-map API (used with `Config.Indicators`) returns only a single pre-registered scalar. For the full TA-Lib surface use the `IndicatorManagerFor(tf)` methods documented under `## indicators` — those return the whole series and cover ~95 functions.
