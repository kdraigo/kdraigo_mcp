---
name: kdraigo-indicators
description: How to choose and wire technical indicators when building a kdraigo Go strategy. Maps trading intent (trend, momentum, volatility, volume, reversal timing) to the right SDK indicator and its IndicatorManager call. Use whenever a strategy needs an indicator — RSI, MACD, Bollinger Bands, ATR, etc.
---

# kdraigo-indicators

Pick indicators by the **question you're trying to answer**, not by name. This
skill maps that intent to the right indicator and the exact SDK call. The kdraigo
dev_sdk exposes ~95 TA-Lib indicators through a timeframe-scoped manager.

> **Deep reference:** for the full "what it's for / how it's calculated / what it
> signals" writeup of any indicator, call the MCP tool **`get_indicator_docs`**
> (optionally `name=RSI` for one indicator or `name=momentum` for a category).
> For signatures/params, `get_sdk_docs section=indicators`. Read these before
> guessing a name or argument order.

## Access pattern (always the same)

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
        return // not enough history yet — treat every error as "warm-up not finished"
    }
    latest := rsi[len(rsi)-1] // most recent value is the LAST element
})
```

Rules that trip people up:
- **Register the timeframe** in `Config.Timeframes` first, then read it with
  `IndicatorManagerFor(tf)`.
- **Latest value = last element**, `series[len(series)-1]`. TA-Lib zero-fills the
  leading lookback region, so early elements are 0, not signal.
- **Error = warm-up.** `len(points) <= period` (or unknown exchange/symbol)
  returns an error. Return early; don't treat 0 as a value.
- **`pointType`** picks the input series for single-input indicators:
  `"close"` (default), `"open"`, `"high"`, `"low"`, `"volume"`. You can run the
  same indicator on different parts of the candle (e.g. RSI of the highs).
  Indicators that need OHLC/HL/HLC/HLCV derive them internally and take **no**
  `pointType`.
- **`maType`** uses the re-exported constants: `indicators.TypeSMA`, `TypeEMA`,
  `TypeWMA`, `TypeDEMA`, `TypeTEMA`, `TypeTRIMA`, `TypeKAMA`, `TypeMAMA`,
  `TypeT3MA`.

## Choose by intent

### "Which way is the trend, and how strong?"
- **Direction** — moving averages: `EMA`/`SMA` (baseline), `KAMA`/`MAMA`
  (adaptive, quieter in chop), `DEMA`/`TEMA`/`T3` (lower lag). Slope = momentum;
  fast/slow crossovers = entries.
- **Strength (not direction)** — `ADX` (>25 strong trend, <20 ranging). Gate your
  trend logic on it: only take MA-crossover entries when `ADX > 25`.
- **Direction from a directional system** — `PlusDI`/`MinusDI` cross, `Aroon`,
  `AroonOsc`.
```go
emaFast, _ := calc.EMA("binance", "BTC/USDT", "close", 20)
emaSlow, _ := calc.EMA("binance", "BTC/USDT", "close", 50)
adx, _     := calc.ADX("binance", "BTC/USDT", 14)
i := len(emaFast) - 1
if emaFast[i] > emaSlow[i] && adx[i] > 25 { /* trend-following long bias */ }
```

### "Is momentum overbought/oversold or diverging?"
- `RSI` (14): >70 overbought, <30 oversold, 50 = midline; divergence vs. price =
  reversal warning.
- `Stoch` / `StochF` / `StochRSI`: position within range; best in ranging markets.
- `CCI`, `WilliamsR`, `CMO`, `MFI` (volume-weighted RSI), `UltOsc` (multi-period,
  fewer false divergences).
- `MACD`: trend + momentum together — signal-line cross, histogram, zero-line.
```go
macd, sig, hist, _ := calc.MACD("binance", "BTC/USDT", "close", 12, 26, 9)
i := len(macd) - 1
if macd[i] > sig[i] && hist[i] > 0 { /* bullish momentum */ }
```

### "How volatile is it? (stops & position sizing)"
- `ATR` — absolute volatility in price units; size stops off it
  (`stop = entry - 2*ATR`). Not directional.
- `NATR` — ATR as % of price, comparable across assets.
- `BB` — envelope; a **squeeze** (narrow bands) precedes breakouts, a band
  **walk** confirms a strong trend, band **touches** flag stretched price.
- `StdDev` / `Var` — raw dispersion.
```go
atr, _ := calc.ATR("binance", "BTC/USDT", 14)
up, mid, low, _ := calc.BB("binance", "BTC/USDT", "close", 20, 2.0, indicators.TypeSMA)
i := len(atr) - 1
stop := entryPrice - 2*atr[i]
```

### "Is volume confirming the move?"
- `OBV`, `Ad` (A/D line), `AdOsc` (A/D momentum). Rising with price = confirmation;
  divergence = the move lacks participation.

### "Where's the channel / support-resistance?"
- `Max`/`Min`/`MinMax` over a period (Donchian-style channel), `MidPrice`,
  `MidPoint`, `SAR`/`SARExt` (trailing stop-and-reverse dots).

### "I need a composite price or a custom indicator"
- Price transforms: `AvgPrice`, `MedPrice`, `TypPrice`, `WCLPrice`.
- Math operators (`Add`/`Sub`/`Mult`/`Div`, `Max`/`Min`/`Sum`) and transforms
  (`Ln`, `Log10`, …) let you build custom series from the OHLCV points — e.g.
  `Sub("high","low")` for the per-bar range, `Ln("close")` for log prices.
- Regression/forecast: `LinearReg`, `LinearRegSlope`, `LinearRegAngle`, `TSF`.
- Cycle regime: `HTTrendMode` (1 = trending, 0 = cycling) as a strategy switch;
  `HTDcPeriod` to adapt other periods.

## Combining indicators (the useful part)

Don't fire on a single indicator. Standard compositions:
- **Trend filter + momentum trigger:** enter only in the direction of `EMA(200)` /
  positive `ADX`, time the entry with `RSI`/`MACD` crossing.
- **Volatility-scaled risk:** decide direction with anything above, size the stop
  and target off `ATR`.
- **Volume confirmation:** require `OBV`/`Ad` to agree with the price move before
  entering.
- **Regime switch:** use `HTTrendMode`/`ADX` to choose between a trend module
  (MA cross) and a mean-reversion module (fade `BB`/`RSI` extremes).

## Common mistakes to avoid

- Reading `series[0]` or a fixed index instead of `series[len(series)-1]`.
- Treating a warm-up error as a zero signal — always `return` early on error.
- Using `pointType` on an OHLC/HLC indicator (it doesn't take one) or omitting it
  on a single-input one.
- Trading a bounded oscillator (RSI/Stoch) mechanically in a strong trend — it
  stays pinned; add a trend/`ADX` filter.
- Requesting a `period` close to the retained history window
  (`WithMaxPoints`, default 1500) so the manager never has enough data.

## Where to get more detail

- `get_indicator_docs` — deep per-indicator reference (`name=` to scope).
- `get_sdk_docs section=indicators` — exact signatures and return tuples.
- The `kdraigo-strategy` skill — the scaffold → backtest → analyze loop that
  wraps all of this.
</content>
