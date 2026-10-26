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
| `types.EnvRealBinance` / `types.EnvTestBinance` | Binance Spot live/testnet |
| `types.EnvRealBinanceFutures` / `types.EnvTestBinanceFutures` | Binance USDⓈ-M perpetuals live/testnet |

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

- `types.Candle` — `Exchange`, `Symbol`, `Timeframe`, `OpenTime`, `CloseTime`, `Open/High/Low/Close/Volume`, `IsComplete`. `Volume` is the **base-asset** volume (there is no separate `BaseVolume` field). Plus order-flow metrics for Wyckoff / Composite-Man analysis: `TradeCount` (int64, number of trades), `QuoteVolume` (quote-asset turnover), `TakerBuyBaseVolume` and `TakerBuyQuoteVolume` (aggressive-buy pressure). These are **0 when the source exchange doesn't provide them** — Binance klines populate all of them; e.g. Bybit exposes `QuoteVolume` but not `TradeCount`/taker-buy splits. Guard on non-zero before relying on them.
- `types.Order` — `ID`, `Side` (BUY/SELL), `Type` (MARKET/LIMIT/STOP_LOSS/STOP_LOSS_LIMIT/TAKE_PROFIT_LIMIT), `Status` (NEW/PARTIALLY_FILLED/FILLED/CANCELED/REJECTED), `Price`, `Quantity`, `FilledQty`, `AveragePrice`, plus `StopPrice` and `GroupID` for bracket legs (`GroupID` is shared by the two legs of a bracket, empty otherwise).
  - **Fees:** `Fee` + `FeeAsset` are what the venue reported **on this update**, and backends differ: the latest fill's commission alone on Binance futures, a running total on Bybit, the whole order's fee in backtests (orders fill whole there). For an order's total read `CumulativeFee` (dev_sdk ≥ v1.2.12), which means the same on every backend. Summing `Fee` over updates is only right on Binance futures, and only if no update is missed.
  - **`RealizedPnL`** (dev_sdk ≥ v1.2.12) is the venue's realized profit on the order's fills so far, before fees. Binance futures only; 0 on spot and in backtests, where futures P&L is on the positions (`get_session_positions`).
  - **`ReduceOnly`** (dev_sdk ≥ v1.2.13) is true for an order that can only shrink a position (a stop loss, a take profit, a closing order). Set on live Binance futures, including a stop that has fired; false on spot and in backtests. Live telemetry also sends it and the stop price, so the platform can show a resting stop and tell an exit from an entry.
- `types.OrderRequest` — what you pass to `ctx.PlaceOrder`. Includes `StopPrice` (trigger for stop orders), `ReduceOnly` (futures only — see the perpetual futures section) plus `Reason` (`map[string]any`) and `Logs` (`[]string`) for telemetry.
- `types.Position` — returned by `s.GetPositions`; futures only. See the perpetual futures section.
- `types.Context` — provided to every callback; exposes `PlaceOrder`, `CancelOrder`, `Now`, `GetIndicator`, the `Config`, and the `Trader` (paper or live).
- `types.Timeframe` — string-backed; constants `Timeframe1m`, `Timeframe3m`, `Timeframe5m`, `Timeframe15m`, `Timeframe30m`, `Timeframe1h`, `Timeframe2h`, `Timeframe4h`, `Timeframe1d`.
  All of these are now genuinely aggregated server-side. Previously `3m`, `30m` and `2h` were
  accepted but silently served as **1-minute** data, so any strategy calibrated on them before
  2026-08 was computing on the wrong bars. An unsupported timeframe is now a hard error rather
  than a silent downgrade.

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
| `SetOnComplete(fn)` | Backtest finished **cleanly** (reached `done`); no-op in live |
| `SetOnError(fn)` | Backtest ended in a terminal error (e.g. truncated stream) |

Live adapters drop in-progress klines before they reach the SDK pipeline, so `OnCandle` fires exactly once per close. In backtest the engine only emits closed historical candles.

### completion vs. error

`SetOnComplete` fires only when the candle stream ends cleanly (the engine sent
`done:true`). If the websocket drops mid-run, the SDK now surfaces a **terminal
error** instead: `SetOnError` fires (if set), `SetOnComplete` does **not**, and
`Start(ctx)` returns a non-nil error. A truncated run is therefore no longer
mistaken for a finished one. Prefer checking `Start`'s return value over relying on
`OnComplete` alone; you can still count bars and compare the last bar timestamp
against `BacktestOptions.EndTime` as a belt-and-suspenders coverage check.

## placing orders

```go
order, err := ctx.PlaceOrder(&types.OrderRequest{
    Symbol:   "BTC/USDT",
    Exchange: "binance",
    Side:     types.OrderSideBuy,    // or types.OrderSideSell
    Type:     types.OrderTypeMarket, // or types.OrderTypeLimit (then set Price)
    Quantity: 0.01,
    Reason:   map[string]any{"signal": "rsi_oversold", "rsi": 28.4},
    Logs:     []string{"computed rsi=28.4 over 14 closes"},
})
```

`ctx.PlaceOrder` takes a single `*types.OrderRequest`. The pair goes in `Symbol`
(there is no `Asset`/`Pair` field); side is `types.OrderSideBuy/Sell`.

Order types: `OrderTypeMarket`, `OrderTypeLimit` (set `Price`), `OrderTypeStopLoss`
(set `StopPrice`; books at the trigger), `OrderTypeStopLossLimit` (set `StopPrice`
*and* `Price`), and `OrderTypeTakeProfitLimit`. Stops work on both sides: a SELL stop
triggers when the bar trades at or below `StopPrice`, a BUY stop at or above — so a
short's protective stop is expressible. **An unknown order type is rejected**; it
previously became a plain limit order, which behaves as the opposite of a stop.

`Reason` and `Logs` are forwarded to the backtester engine and persisted alongside the order. Use them — they are returned by the orders endpoint/tool so a run can be reviewed and explained after the fact. They are capped exactly as live telemetry is: a `Reason` over 4 KB is stored as `{"_truncated":true,"_original_size":N}`, and `Logs` keep at most 31 lines of up to 1 KB (16 KB in all) plus a `[truncated N more lines]` line. The order itself is never refused for this. See `## limits`.

### fill semantics in backtest

Market orders fill **synchronously**: `PlaceOrder` returns an already-`FILLED`
order priced at the **current candle's close** (`paper_wallet.go` →
`CreateOrderMarket`). Limit orders fill at the limit price. Taker fees are deducted
from the quote balance (reported on the order as `Fee`).

The returned order now populates `AveragePrice`, `FilledQty`, `ID` and `Symbol`
for a filled order (`AveragePrice == Price`, `FilledQty == Quantity`). Filled orders
are *also* re-dispatched asynchronously to `SetOnOrderUpdate` on the following tick,
so make order handling **idempotent** (dedupe on `order.ID`).

Resting stop orders fill **intrabar**, not on the close: a SELL stop triggers on the
bar's low, a BUY stop on its high. There is no longer any reason to simulate stops
strategy-side, and doing so is now less accurate than placing a real one.

A stop that **gapped through** its trigger books the bar's open rather than the stop
price, because the stop price was never available once the order was live. Disable
this with `Simulation.GapFills = false` if you need the old behaviour.

When a single bar's range contains several resting orders — a stop *and* a
take-profit, say — the **fill policy** decides which fills first. It defaults to
`pessimistic` (the adverse one), which never flatters a result. `optimistic` and
`creation_order` exist mainly so the difference can be measured;
`creation_order` reproduces results produced before the policy existed.

### brackets

`SDK.PlaceBracket` places a take-profit and a protective stop as a **mutually
cancelling pair** sharing one fund reservation: when either fills, the other is
cancelled, and both legs are persisted so the outcome is auditable.

It is a *pair*, not a ladder. The paper wallet reserves funds per order, so several
independent take-profits against one position cannot rest simultaneously — the second
returns `INSUFFICIENT_FUNDS`. A strategy that wants a laddered exit should place
bracket 1 for the first tranche, then re-bracket the remainder when it fills. For a
bar-driven backtest the difference is nil.

Live, **Binance futures** implements `PlaceBracket`: both legs are reduce-only, the
take-profit rests as a limit order and the stop goes through the algo-order endpoint
(see "Conditional orders on Binance futures" under perpetual futures). Binance does not
link the legs, so the adapter cancels the survivor itself when the first one fills, and
logs an orphan if that cancel fails. Futures legs reserve no funds, so both rest at
once. Without `StopLimitPrice` the stop is market-on-trigger.

The spot adapters (Binance, Bybit) do not implement brackets and return
`dev_sdk.ErrUnsupportedByAdapter`; check for it if your strategy runs on both.

### fees and the fill model

Backtests now charge fees by default (0.1% maker and taker, Binance spot). **Every
session recorded before 2026-08 ran fee-free**, so historical results are not
comparable to new ones — at 500 round trips a 0.1% taker fee is 100% of turnover
notional. Override via `Backtest.Simulation`:

```go
Simulation: &types.SimulationOptions{
    FillPolicy: "pessimistic",   // or "optimistic" / "creation_order"
    TakerFee:   ptr(0.001),      // pointer: nil means default, 0 means genuinely free
},
```

The resolved fill policy, gap-fill setting and both fees are stored on the session
row, so any stored result can be read back together with the assumptions that
produced it.

### Short positions

On a **spot** wallet the backtest simulates shorts: a `SELL` exceeding your current
long opens one. Live spot adapters cannot — spot is a cash balance, so a short-based
strategy that backtests cleanly on spot will be rejected when run live. If you want
shorts that work in both places, use perpetuals.

On a **perpetual** wallet shorting is native and symmetric with going long. See the
futures section below.

## perpetual futures

Perpetuals are a **separate exchange**, not a mode of the spot one. `binance_futures`
has its own candle data, funding rates, mark-price series and maintenance-margin
ladder, all collected and stored separately from `binance`. Name it and you get a
futures wallet:

```go
Backtest: &types.BacktestOptions{
    RequestedExchanges: []string{"binance_futures"},
    Assets:             []string{"BTC/USDT"},
    WalletsByExchange:  map[string]map[string]float64{"binance_futures": {"USDT": 10000}},
    LeverageByExchange: map[string]float64{"binance_futures": 5},
    // MarketTypeByExchange is optional: an exchange whose name ends in
    // _futures defaults to linear_perp. Set it explicitly only to override.
    StartTime: ..., EndTime: ...,
}
```

Live is the same strategy against a different environment:

```go
Environment: types.EnvTestBinanceFutures,
Live: &types.LiveOptions{
    RequestedExchanges: []string{"binance_futures"},
    Assets:             []string{"BTCUSDT"},
    Leverage:           map[string]float64{"BTCUSDT": 5},
    MaxOrderNotional:   500,  // required — no default
    MaxLeverage:        10,   // required — no default
},
```

### What v1 supports

**Isolated margin, one-way positions, linear (USDT-margined) contracts.** Cross
margin, hedge mode and inverse contracts are rejected at session creation rather
than half-supported. The live adapter refuses to start against an exchange account
configured differently, because running it would produce different semantics from
the backtest that justified the strategy.

Available exchanges: `binance_futures`, `bybit_futures` (backtest); `binance_futures`
(live).

### Futures-only API

These are optional capabilities: an adapter that lacks them returns
`sdk.ErrUnsupportedByAdapter`, which you can test for and fall back on.

```go
err := s.SetLeverage(ctx, "binance_futures", "BTC/USDT", 5)
positions, err := s.GetPositions(ctx, "binance_futures") // empty exchange = all wallets
orders, err := s.PlaceBracket(ctx, &types.BracketRequest{...})
```

- **`SetLeverage`** is refused while a position is open, on both the simulator and
  the live venue. Re-levering an open position rewrites its liquidation price, which
  no exchange lets you do either.
- **`GetPositions`** returns `types.Position`: `Side` ("LONG"/"SHORT"), `Size`
  (always positive — flat is the absence of a position, not a zero-size one),
  `EntryPrice` (size-weighted), `Leverage`, `IsolatedMargin` (which under isolated
  margin is also the maximum loss), `MarkPrice`, `UnrealizedPnL`. In backtest,
  `RealizedPnL` and `FundingPaid` accumulate over the position's life; live leaves
  them at zero, because the venue does not attribute them per position lifetime and
  inventing the number would misattribute payments after a flip.
- **`PlaceBracket`** places a take-profit and a protective stop as a pair. On
  futures the legs reserve nothing — margin is already posted against the position —
  so both rest at once, unlike on spot.

### `ReduceOnly`

`OrderRequest.ReduceOnly` restricts an order to shrinking a position: it can never
open one, nor flip an existing one. Set it on every protective exit.

It exists for a specific failure. In one-way mode a stop sized larger than the
position — a stale bracket, or sizing computed off intended rather than filled
exposure — does not stop at flat: the surplus opens a position on the other side, so
you end up with the same exposure inverted on exactly the bar you wanted none.

It is futures-only. A spot wallet rejects it rather than ignoring it.

### Stops trigger on the mark, not the last trade

Liquidation and stop triggers use the venue's mark-price series by default
(`SimulationOptions.MarkPriceSource: "mark_series"`). This is not a detail: measured
on production data the mark and the trade price diverge by up to 1.3% at the bar low,
and on ~3% of bars the mark wicks below the trade low — liquidations a trade-price
model misses entirely. The live adapter sets `workingType=MARK_PRICE` for the same
reason, so live and backtest agree about when a stop fires.

### Funding

Charged every settlement (8h on Binance) while a position is open, at
`signedSize × mark × rate`. A long pays when the rate is positive. Over a long hold
funding can exceed the trading PnL, so it is on by default; `funding_enabled: false`
is allowed for isolating its effect but is a directional bias, not a simplification.

Read the ledger back with the `get_session_funding` tool, and positions with
`get_session_positions`.

### Liquidation

A position is force-closed when the mark crosses its liquidation price, computed from
the venue's own captured maintenance-margin tiers:

```
LONG:  (Size·Entry − IM − cum) / (Size·(1 − MMR))
SHORT: (Size·Entry + IM + cum) / (Size·(1 + MMR))
```

The margin is forfeited, the position row records `liquidated = true` with
`isolated_margin = 0`, and a `LIQUIDATION` order appears in the log. Live, the same
event arrives through `SetOnOrderUpdate`. Pin `contract_specs_version` if you need a
stored result to stay reproducible after the exchange revises its tiers.

### Live futures safety gates

A mainnet futures order passes four independent gates, all of which must hold:

1. `Environment` is `real_binance_futures` — testnet is a different value, not a flag.
2. `LiveOptions.Armed` is explicitly true; it defaults to false.
3. `KDRAIGO_LIVE_ARMED=1` is set in the process environment. Config alone cannot arm
   a machine, because a config can be committed or copied.
4. The order's notional is under `MaxOrderNotional` and leverage under `MaxLeverage`.
   Both are **required with no default**, so a forgotten cap is a refusal rather than
   an unlimited one. These apply on testnet too.

`LiveOptions.DryRun` logs the exact request and returns a synthetic ack, which is how
you exercise a strategy against real market data without touching the account.

Failures come back as `*live.ErrNotArmed`, naming the gate.

### Conditional orders on Binance futures

Since dev_sdk v1.2.0, stops go through Binance's algo-order endpoint
(`/fapi/v1/algoOrder`). The ordinary `/fapi/v1/order` endpoint refuses every
conditional type with `-4120`, even though the instrument's `exchangeInfo` advertises
them. What that means for a strategy:

- A stop's order id carries an **`algo:` prefix**. Pass it to `CancelOrder` unchanged.
- When a stop triggers, Binance creates an ordinary order to fill it. The SDK reports
  that fill under the stop's own `algo:` id and type, so `SetOnOrderUpdate` sees the
  stop you placed fill, not a market order you never placed.
- Triggers use `workingType=MARK_PRICE`, as in the backtest. A stop is reduce-only when
  you set `ReduceOnly` (bracket legs always do).
- If an environment refuses conditional orders anyway, the SDK returns
  `live.ErrConditionalOrdersUnavailable` and does **not** fall back to a market order:
  a protective stop that executes immediately is worse than one that fails loudly.
  Check for it, and fall back deliberately if you must (for example, watch the mark and
  close at market).

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
- Call `IndicatorManagerFor` inside the callback, as above. Before dev_sdk v1.3.1 the managers only exist once `Start()` runs, so a manager taken between `New` and `Start` is nil and the first indicator call panics.
- Latest value is the **last** slice element: `series[len(series)-1]`. TA-Lib zero-fills the leading lookback region.
- On insufficient data (`len(points) <= period`) or unknown exchange/symbol the call returns an error — return early, it means warm-up isn't finished.
- `pt` (`pointType`) selects the input series for single-input indicators: `"close"` (default), `"open"`, `"high"`, `"low"`, `"volume"`. Indicators needing OHLC/HL/HLC/HLCV derive them internally and take **no** `pt`.
- `maType` uses re-exported constants: `indicators.TypeSMA`, `TypeEMA`, `TypeWMA`, `TypeDEMA`, `TypeTEMA`, `TypeTRIMA`, `TypeKAMA`, `TypeMAMA`, `TypeT3MA`.

### warm-up (skip the ramp-up period)

By default indicators only see candles the SDK has streamed, so early bars return
errors until enough history accumulates. To get real values on the **first** bar,
fetch history with `GetCandles` — the fetched range is **fed into the SDK's own
indicator manager** for that timeframe automatically:

```go
primed := false
s.SetOnCandleFor(types.Timeframe1h, func(ctx *types.Context, c *types.Candle) {
    if !primed {
        // Fetching history primes IndicatorManagerFor(tf) for this timeframe.
        // Do it once, from the first callback.
        if _, err := s.GetCandles(ctx.Ctx, "binance", "BTC/USDT", 300, types.Timeframe1h); err != nil {
            log.Printf("warmup fetch: %v", err)
        }
        primed = true
    }
    atr, err := s.IndicatorManagerFor(types.Timeframe1h).ATR("binance", "BTC/USDT", 14)
    // ... atr is populated on the very first bar
})
```

`GetCandles`/`GetCandlesFromTo` feed the same manager `IndicatorManagerFor(tf)` reads
from — you do **not** need to construct your own `indicators.NewIndicatorManager`. The
manager keys points by candle OpenTime and de-duplicates, so this is **idempotent**:
fetching overlapping ranges, or a bar already streamed in, replaces the point rather
than duplicating it — history is never corrupted no matter how often you call it. No
look-ahead: in backtest the fetch is a pure read served by the engine, which rejects
any range past the simulated clock — so a session can start trading on the first bar
instead of burning a warm-up window with orders suppressed.

### resuming an interrupted run

A dropped connection no longer ends a backtest. The engine holds the session —
wallets, positions, and its place in the candle stream — for a resume window
(one hour by default), and the **same API key** that created it may reattach and
carry on.

Two levels, and most strategies need neither:

**1. In-process reconnects are automatic.** If the socket drops mid-run the SDK
redials with backoff and continues. Ticks carry a sequence number, so a candle
the dropped connection never delivered is re-requested rather than skipped —
delivery is exactly-once across any number of reconnects. Indicators and the
in-flight aggregate bar are untouched, because the process never died. Nothing
to write.

**2. Cold resume, after the bot's process restarted.** Pass the session id:

```go
cfg.Backtest.SessionID = "0f2c…"   // empty means "create a new session"
```

`PrepareSession` then skips session creation and attaches instead. This is the
case where your own memory is gone — indicators, partial bars, whatever you
tracked about your position — so rebuild it from the engine's view:

```go
s.SetOnResume(func(ctx *types.Context, st *types.SessionState) {
    if !st.ColdStart {
        return // a mere reconnect: indicators are intact, nothing to do
    }
    // Warm indicators up to the playhead — the same idiom as above, bounded
    // by where the run actually stopped.
    _, _ = s.GetCandlesFromTo(ctx.Ctx, "binance", "BTC/USDT",
        cfg.Backtest.StartTime, st.Playhead, types.Timeframe1h)

    // The engine is authoritative about what you hold. Reconcile against it
    // rather than trusting anything you remember.
    inPosition = len(st.Positions) > 0
    openOrders = st.OpenOrders
})
```

`OnResume` fires **before** any candle from the resumed connection reaches your
strategy, and runs synchronously on the stream goroutine — a long history fetch
delays the first bar, which is the right trade: the alternative lets a bar land
before your indicators are warm.

**What the SDK will not do:** silently start a fresh session. If the session is
gone — expired, evicted, or the engine restarted — `Start` returns an error and
`OnComplete` does not fire. Starting over quietly would hand you results you
would read as a continuation of a run that no longer exists. Decide yourself
whether to begin again.

Three failure codes are worth telling apart: `session_unknown` (gone — do not
retry), `session_key_mismatch` (right user, wrong API key — a configuration
error), and `session_terminal` (that run already finished).

**Limits.** A planned engine redeploy hands every session, running or parked,
over to the new process, and the SDK reattaches by itself. An engine crash does
not: its parked sessions are gone and `Start` returns `session_unknown`. A run's
budget (see `## limits`) counts only time actually spent driving it — an hour
parked costs nothing.

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

## plots

A strategy can draw its own values on the session chart at kdraigo.com, backtest and live: its moving averages over the candles, an oscillator in a panel below them, a marker on the bar a signal fired. Needs dev_sdk v1.4.0+ for backtests and v1.5.0+ for live.

```go
import (
    sdk "github.com/kdraigo/dev_sdk"
    "github.com/kdraigo/dev_sdk/plots"
    "github.com/kdraigo/dev_sdk/types"
)

// Declare once, before Start.
fast, _ := s.Plot(plots.Def{Key: "ema_fast", Title: "EMA 50"})              // over the candles
rsi, _ := s.Plot(plots.Def{Key: "rsi", Title: "RSI 14", Pane: "RSI",         // a panel of its own
    Levels: []plots.Level{{Value: 70}, {Value: 30}}})
hist, _ := s.Plot(plots.Def{Key: "macd_hist", Pane: "MACD", Kind: plots.Histogram, SignColors: true})
bb, _ := s.Plot(plots.Def{Key: "bb", Title: "BB 20", Kind: plots.Band})
cross, _ := s.Plot(plots.Def{Key: "cross", Kind: plots.Markers,
    Marker: &plots.Marker{Shape: plots.ArrowUp, Position: plots.Below}})

// Add a value per bar.
s.SetOnCandleFor(types.Timeframe1h, func(ctx *types.Context, c *types.Candle) {
    fast.Add(c, emaFast)
    rsi.Add(c, rsiValue)
    hist.Add(c, macd-signal)
    bb.AddBand(c, upper, lower)
    if crossedUp {
        cross.Mark(c, "cross")
    }
})
```

- **Key**: lowercase letters, digits, `_ . -`, at most 64. The chart remembers a viewer's choices by it.
- **Pane**: empty draws over the candles on their price scale (values in price units). Any other name is a panel below the candles; plots with the same `Pane` share it.
- **Kind**: `plots.Line` (default), `Step`, `Histogram`, `Area`, `Band` (two values per bar, `AddBand`; the space between is filled) or `Markers` (`Mark`). `Levels` are dashed horizontal lines in the plot's panel; `SignColors` colours a histogram green at or above zero and red below.
- **Colour**: `Color: "#rrggbb"`, or leave it empty for the chart's palette. `Hidden: true` starts a plot switched off. The viewer can show or hide any plot and change its colour, kind and width on the chart; the strategy's choices are the defaults.
- A value belongs to its candle's exchange, pair and bar open time, so one plot serves every pair; the chart shows the selected pair's. Use `AddAt(exchange, pair, openTime, v)` for bars the strategy keeps itself, and set `Def.Timeframe`.
- NaN and infinite values are skipped, so a warming-up indicator can be added unchecked. Adding a value twice for the same bar keeps the last.
- `Plot` returns an error for a bad declaration, and a nil series that ignores every call, so plotting never breaks trading code. Declaring the same key again with the same `Def` returns the same series (one per-pair bot each may declare).
- Values ride on the tick requests the SDK already sends, so they cost no extra round trip. They are stored with the session and count toward its storage.
- On a chart coarser than the plot, each bar shows the plot's last value in it. On a finer chart a value is drawn on the last bar of its own bar, so the chart never shows a value before it existed.

**Live** (dev_sdk v1.5.0+): the same code, with `LiveOptions.TelemetryURL` set (`## live telemetry`). Without telemetry the series accept values and drop them.
- The SDK sends what the strategy added every 2 s, as one signed `plots` event per send (up to 256 KB), so a portfolio's pairs closing the same bar cost one request.
- A send that cannot reach the platform is kept and retried with backoff (up to a minute apart); one the platform refuses (4xx) is dropped. Declarations are re-sent every 10 minutes. On shutdown the SDK flushes before `session_stopped`.
- The live console re-reads the newest bars every 15 s, so a bar's values appear shortly after it closes.

Limits: 32 plots and 8 panels per session, 8 levels per plot, marker text 32 characters. Plot storage per session is 8 MB basic / 64 MB verified / 256 MB admin (about 16 bytes a value: 500,000 values on basic). Past it a **backtest** keeps running, one response carries a `warning`, and later values are not stored; a **live** session keeps its newest values and drops its oldest (the warning is logged by the SDK once).

## cloud workspace

The cloud workspace builds and runs a strategy on the platform, so nothing is needed locally: no Go, no checkout, no keys. The `workspace_*` tools drive it. The same program runs as it would with `go run .`; only where it runs changes.

**Shape.** A workspace is flat `.go` files in package `main`: no folders, no `go.mod`, no `go.sum`. The platform owns the module and pins dev_sdk to the workspace's version. A workspace starts from a template (`basic`, `with-indicators`, `with-risk-manager`). Limits: 50 files, 256 KB each, 1 MB in all.

**Editing alongside the user.** The user can open the same workspace in the website's Lab, edit its files there and watch your writes, builds and runs as they happen.
- **Versions:** each file's version goes up with every write. `workspace_read_file` and `workspace_files` show it, and who wrote it (the Lab or API).
- **Pass the version you read** as `base_version` to `workspace_write_file` (0 for a new file), and as `version` to `workspace_delete_file`.
- **If the file changed since, nothing is written** and the tool says who changed it. Read it again, keep that change, and write again with the new version.
- **Without `base_version`** a write replaces whatever the file holds, the user's edits included.

**Imports.**
- **Allowed:** `github.com/kdraigo/dev_sdk/...` and this part of the standard library:
  - `bufio` `bytes` `cmp` `context` `embed` `errors` `flag` `fmt` `html` `io` `io/fs` `iter` `log` `log/slog` `maps`;
  - `os` `path` `path/filepath` `reflect` `regexp` `runtime` `runtime/debug` `slices` `sort` `strconv` `strings`;
  - `sync` `sync/atomic` `testing` `time` `unique` `unicode` `unicode/utf8` `unicode/utf16`;
  - `math` `math/big` `math/bits` `math/cmplx` `math/rand` `math/rand/v2`;
  - `container/heap|list|ring`;
  - `encoding` `encoding/base64|binary|csv|hex|json`;
  - `hash` `hash/crc32|fnv|maphash`;
  - `crypto/hmac|md5|rand|sha1|sha256|sha512`;
  - `text/scanner|tabwriter|template`.
- **Refused:** everything else, among them `os/exec`, `net/...`, `unsafe`, `syscall`, `plugin`, cgo and any other module, plus `//go:linkname` and other `//go:` directives except `build`, `embed`, `generate` and `noinline`.
- **Refused functions:** `os.StartProcess` and `os.FindProcess`. The rest of `os` stays: environment, exit, output.
- **When it's checked:** at build time, and a refused build lists every problem as `file:line: message`.

**How a run goes.**
1. `workspace_build` (or the build inside `workspace_run`) compiles offline on a sandbox machine. A machine that has to start takes about 40 s; later builds take seconds.
   - **Admin accounts** (and paid ones, later) get a machine of their own.
   - **Verified accounts, and accounts signed in with Google,** share machines with other users' runs, each run in its own isolated container. When they are full, a run waits in a queue (`queued`; `workspace_run_status` shows its place) for at most 30 minutes, and its time counts only once it starts. A shared machine keeps no binary after its run.
2. `workspace_run` then runs the binary with:
   - `KDRAIGO_KEY_ID` and `KDRAIGO_PRIVATE_KEY`: a key minted for this run alone, which can only open and stream backtest sessions and is revoked when the run ends;
   - `KDRAIGO_BACKTESTER_URL`: the backtester to use.
   The templates read all three; keep them that way rather than hard-coding an endpoint.
3. The run's backtest sessions are listed by its key. `workspace_run` and `workspace_run_status` show them; pass one to `get_session_report`.

**What the program can reach.** It runs as an unprivileged user under gVisor, on a read-only filesystem with a small `/tmp` that cannot execute. Its only network access is the platform API through dev_sdk: no internet, no DNS, no exchanges. **Backtests only**: live trading does not run here.

**Your own settings** go in `workspace_run`'s `env`: upper-case names, at most 32 of them, values up to 4 KB with no line breaks, and never a `KDRAIGO_` name. A run records the names it was given, never the values.

**Limits per run.**
- **Wall-clock time:** at most 1 h (verified, Google sign-in), admin 12 h, and never more than the account's machine time left today.
- **Runs per day:** 200 (UTC), builds and refused ones included; admin has no cap.
- **Stalls:** a run that prints nothing and moves (almost) no network traffic for **5 minutes** is stopped as `stalled`. That catches a strategy that sleeps or deadlocks (for example a mutex locked twice); a backtest that is streaming candles is never quiet that long.
- **Output:** kept up to 1 MB.

| | basic, wallet | basic, Google sign-in | verified | admin |
|---|---|---|---|---|
| Cloud workspace | no (link Telegram) | yes | yes | yes |
| Runs at once | — | 1 | 1 | 4 |
| Machine time per day (UTC) | — | 1 h | 4 h | 24 h |
| Workspaces | — | 3 | 10 | 50 |
| Time per run | — | 1 h | 1 h | 12 h |
| Machine | — | shared, queued | shared, queued | own |

Linking Telegram on the website's Account page makes an account verified. An account's newest 50 runs keep their source and output (`workspace_logs`); older runs keep only their record, and records go after 30 days.

## limits

The hosted backtester enforces these per session and per account. Normal strategies never come near them (the largest real session so far placed about 3,000 orders).

What an account may use depends on its tier. **basic** is a Google account or a wallet alone (a Google account also gets a small cloud workspace, above); **verified** has a Telegram account linked on the website's account page (one Telegram account verifies one kdraigo account, and after it is unlinked it cannot verify another account for 30 days); **admin** is set by the operator. The website's Account page shows the account's tier, its limits and the storage its sessions use; each finished session in `list_sessions` carries its `storage_bytes`.

| Per account or session | basic | verified | admin | When reached |
|---|---|---|---|---|
| Storage of finished sessions | 50 MB | 512 MB | no account cap | Create returns 403, code `storage_quota_exceeded`: delete old sessions (retrying does not help) |
| Sessions at once (running, or waiting to be resumed) | 1 | 3 | 20 | Create returns 429, code `too_many_sessions`: let one finish or close it |
| Pairs per session (`Assets` × `RequestedExchanges`) | 2 | 5 | no cap | Create returns 403, code `backtest_too_large`: split the pairs over several sessions |
| Period per session, in bars of its smallest timeframe, per pair | 50,000 (about 34 days of 1m, 5.7 years of 1h) | 150,000 (about 104 days of 1m, 17.1 years of 1h) | no cap | Create returns 403, code `backtest_too_large`: shorten `StartTime`–`EndTime` or use a longer timeframe |
| Orders per session | 20,000 | 100,000 | 1,000,000 | That order is refused with code `order_limit_reached` and the session ends `FAILED`; orders placed before it are kept |
| Stored reasoning per session | 8 MB | 64 MB | 256 MB of `Reason` + `Logs` | Orders keep executing; one response carries `warning`, and later reasoning is not stored |
| Stored plot values per session (`## plots`) | 8 MB | 64 MB | 256 MB | The run continues with a `warning`. A backtest stores no later values; a live session drops its oldest |
| Run time per session (parked time not counted) | 2 h | 4 h | 12 h | The session ends `FAILED`: "session run budget exceeded" |

All basic accounts together share 4 running sessions and 10 GB of storage; when the pool is full, a basic account's create returns 503, code `capacity_exceeded`, until sessions finish or are deleted. Verified and admin accounts do not use the pool.

| For everyone | Value | When reached |
|---|---|---|
| One WebSocket message | 64 KB | The connection closes (1009) and the session ends `FAILED` |
| Streams per session | 1–50 | Create returns 400, code `invalid_request` |
| Bars per history / candles request | 50,000 (about a month of 1m) | 400 `too many bars`: use a larger timeframe or split the range |
| Platform capacity | — | Create returns 503, code `capacity_exceeded`: retry later |

A session's storage is measured when it finishes. A session that is created but never driven waits up to an hour for a client and counts toward the sessions at once meanwhile, so drive or close what you create.

## auth

Strategies authenticate to the platform with the same Ed25519 keypair the MCP server uses. Set `KDRAIGO_KEY_ID` and `KDRAIGO_PRIVATE_KEY` in env; the scaffolded templates read them. Never commit either value.

Requests are signed with signing version 2 (dev_sdk v1.3.0+ and this MCP server). The signature covers the method, path, query string, a one-time nonce and a hash of the body, and the platform accepts each signature once. Backtest WebSockets send their credentials as headers, not in the URL. **Version 1 is refused since 2026-10-11**: an older dev_sdk gets `401` with "request signing v1 is no longer accepted: update to dev_sdk v1.3.0+ or kdraigo-mcp v1.0.9+". Pin `github.com/kdraigo/dev_sdk v1.3.0` or later in `go.mod`.

## endpoints

`BacktestOptions.Endpoint` is the **backtester_engine** base URL:
`https://api.kdraigo.com` for the hosted engine, or `http://localhost:4000` for a
local one. This is **not** the `endpoint:` in `~/.kdraigo/config.yaml` — that is the
MCP gateway base (`https://kdraigo.com`) for the data/analytics tools. The
backtester is a separate host: the `kdraigo.com` gateway does not proxy it (the
session POST 405s), so the SDK and MCP target `api.kdraigo.com` directly.

## live telemetry

A live strategy reports its orders, balances and heartbeats to the platform when `LiveOptions.TelemetryURL` is set:

```go
Live: &types.LiveOptions{
    RequestedExchanges: []string{"binance_futures"},
    Assets:             []string{"BTC/USDT"},
    TelemetryURL:       "https://api.kdraigo.com/telemetry",
    StrategyName:       "ema_trend",      // shown on kdraigo.com and in alerts
    SessionID:          os.Getenv("KD_SESSION_ID"), // optional: keep one session across restarts
},
```

- Use `https://api.kdraigo.com/telemetry` exactly. Before dev_sdk v1.3.1 the bare `https://api.kdraigo.com` was completed to a path the gateway does not route, and every event got a 404.
- Events are signed with the same `Credentials.KeyID` / `PrivateKey` as backtests. Telemetry for a session that belongs to another account is refused (403).
- Telemetry powers the **Live strategies** page on kdraigo.com and, when the account has linked Telegram, alerts from **@kdraigo_bot** (position opened, SL/TP moves, partly closed, closed, bot offline, stopped, daily summary) and the Telegram Mini App dashboard.
- Reason and logs are truncated as described under `## placing orders`; the order itself is never refused for it.

## known gaps

- `CancelOrder` WS round-trip in backtest engine: implemented, and cancel timestamps now come from the simulated clock, so two runs over identical input produce an identical order log.
- The `ctx.GetIndicator(name)` string-map API (used with `Config.Indicators`) returns only a single pre-registered scalar. For the full TA-Lib surface use the `IndicatorManagerFor(tf)` methods documented under `## indicators` — those return the whole series and cover ~95 functions.
