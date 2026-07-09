# Indicator Reference — Full Documentation

This document describes **every** indicator exposed by the SDK's
`IndicatorManager` in depth: what each one is for, how it is calculated, what
market situation it reveals, and how to use it in a strategy. For a compact
parameter cheat-sheet see [README.md](README.md); this file is the long-form
companion.

All indicators are TA-Lib implementations (plus a few SDK helpers) computed over
the candle history the SDK has streamed for a given timeframe.

---

## Table of contents

- [Core concepts](#core-concepts)
  - [The `pointType` parameter](#the-pointtype-parameter)
  - [Multi-input indicators (OHLC / HL / HLC / HLCV)](#multi-input-indicators)
  - [The `maType` parameter](#the-matype-parameter)
  - [Return values and warm-up](#return-values-and-warm-up)
- [Overlap studies / moving averages](#overlap-studies--moving-averages)
- [Momentum indicators](#momentum-indicators)
- [Volume indicators](#volume-indicators)
- [Volatility indicators](#volatility-indicators)
- [Price transform](#price-transform)
- [Cycle indicators (Hilbert Transform)](#cycle-indicators-hilbert-transform)
- [Statistic functions](#statistic-functions)
- [Math transform & operators](#math-transform--operators)

---

## Core concepts

### The `pointType` parameter

Many indicators are **single-input**: they run over one price series. The
`pointType` argument selects *which* series of the OHLCV candle that is:

| `pointType` | Series used | Typical use |
|---|---|---|
| `"close"` (default) | Close price | The standard input for almost every indicator. |
| `"open"` | Open price | Opening-driven models, gap studies. |
| `"high"` | High price | Breakout / resistance envelopes. |
| `"low"` | Low price | Support envelopes, downside studies. |
| `"volume"` | Volume | Applying MAs / oscillators to volume itself. |

So the same indicator can be measured against different parts of the candle.
For example RSI is normally taken on closes, but you can measure it on highs to
study the momentum of the intrabar peaks:

```go
rsiClose, _ := calc.RSI("binance", "BTC/USDT", "close", 14) // classic RSI
rsiHigh,  _ := calc.RSI("binance", "BTC/USDT", "high", 14)  // RSI of the highs
```

Any value other than `open` / `high` / `low` / `volume` falls back to **close**.
This is handled in `getPoints` (see [manager_helpers.go](manager_helpers.go)).

A few statistical / math operators take **two** point types (`pt0`, `pt1`) so
you can relate two series — e.g. `Correl("...", "high", "low", 30)` measures the
correlation between the highs and the lows.

### Multi-input indicators

Some indicators are defined over several candle fields at once and therefore
take **no** `pointType` — they derive the series they need internally:

- **HL** (high, low): `MidPrice`, `SAR`, `SARExt`, `Aroon`, `AroonOsc`,
  `MinusDM`, `PlusDM`, `MedPrice`.
- **HLC** (high, low, close): `ADX`, `ADXR`, `CCI`, `DX`, `MinusDI`, `PlusDI`,
  `Stoch`, `StochF`, `UltOsc`, `WilliamsR`, `ATR`, `NATR`, `TRANGE`, `TypPrice`,
  `WCLPrice`.
- **HLCV** (+ volume): `MFI`, `Ad`, `AdOsc`.
- **OHLC**: `BOP`, `AvgPrice`.

If you see no `pointType` in the signature, the indicator is one of these.

### The `maType` parameter

Indicators that smooth with a moving average expose a selectable MA type via the
re-exported constants:

`indicators.TypeSMA`, `TypeEMA`, `TypeWMA`, `TypeDEMA`, `TypeTEMA`, `TypeTRIMA`,
`TypeKAMA`, `TypeMAMA`, `TypeT3MA`.

### Return values and warm-up

- Every method returns `([]float64, error)` (or multiple `[]float64` for
  multi-output indicators). **The latest value is the last element**:
  `series[len(series)-1]`.
- The returned slice is aligned to candle history; TA-Lib zero-fills the leading
  "lookback" region before the indicator has enough data to produce a real value.
- An `error` means either the exchange/symbol is unknown or there is **not enough
  history yet** (`len(points) <= period`). Treat it as "warm-up not finished" and
  return early from the callback.

---

## Overlap studies / moving averages

Overlap studies are plotted **on top of price**. They smooth price to reveal
trend direction and act as dynamic support/resistance.

### `BB` — Bollinger Bands
`BB(pt, period int, deviation float64, maType) -> (upper, middle, lower, error)`

- **For:** measuring volatility and relative price extremes around a moving
  average. One of the most widely used envelope indicators.
- **Calculates:** a middle band = MA(price, period); the upper/lower bands sit
  `deviation` standard deviations above/below the middle
  (`middle ± deviation·σ` over the same window).
- **Shows:** when bands **widen**, volatility is rising; when they **squeeze**,
  volatility is contracting and often precedes a breakout. Price touching/piercing
  the upper band = stretched to the upside (potentially overbought); the lower
  band = stretched to the downside. Mean-reversion strategies fade the bands;
  trend strategies trade band "walks."
- **Usage:**
  ```go
  up, mid, low, err := calc.BB("binance", "BTC/USDT", "close", 20, 2.0, indicators.TypeSMA)
  i := len(mid) - 1
  if price > up[i] { /* stretched high */ }
  ```

### `DEMA` — Double Exponential Moving Average
`DEMA(pt, period int) -> series`
- **For:** a faster-reacting trend line than EMA. **Calculates:**
  `2·EMA − EMA(EMA)`, which cancels much of the lag. **Shows:** trend direction
  with less delay; good for responsive crossovers but noisier in chop.

### `EMA` — Exponential Moving Average
`EMA(pt, period int) -> series`
- **For:** the standard weighted trend line. **Calculates:** exponentially
  weighted average giving more weight to recent prices. **Shows:** trend
  direction; slope = momentum. Faster than SMA, common in crossover systems.

### `HTTrendline` — Hilbert Transform Instantaneous Trendline
`HTTrendline(pt) -> series`
- **For:** an adaptive, low-lag trendline based on the dominant market cycle.
  **Calculates:** uses the Hilbert Transform to smooth out the dominant cycle,
  leaving the underlying trend. **Shows:** trend without a fixed period; adapts
  to changing cycle length.

### `KAMA` — Kaufman Adaptive Moving Average
`KAMA(pt, period int) -> series`
- **For:** an MA that speeds up in trends and slows down in chop. **Calculates:**
  adjusts its smoothing constant by an "efficiency ratio" (directional move vs.
  total volatility). **Shows:** trend while filtering noise/whipsaws better than a
  fixed-period MA.

### `MA` — Generic Moving Average
`MA(pt, period int, maType) -> series`
- **For:** any MA type via one call. **Calculates:** dispatches to the chosen
  `maType`. **Shows:** trend; behaviour depends on the selected type.

### `MAMA` — MESA Adaptive Moving Average
`MAMA(pt, fastLimit, slowLimit float64) -> (mama, fama, error)`
- **For:** adaptive trend following using cycle measurement. **Calculates:** MAMA
  adapts its alpha between `fastLimit` and `slowLimit` based on the measured
  cycle phase; FAMA (Following Adaptive MA) is a smoother companion line.
  **Shows:** MAMA/FAMA crossovers are used as trend entry/exit signals.

### `MaVp` — Variable Period Moving Average
`MaVp(pt, periods []float64, minPeriod, maxPeriod int, maType) -> series`
- **For:** an MA whose period varies **per bar** (e.g. driven by volatility).
  **Calculates:** at each bar uses the period from `periods` clamped to
  `[minPeriod, maxPeriod]`. **Shows:** adaptive smoothing when you already have a
  per-bar period series to feed it.

### `MidPoint` — Midpoint over period
`MidPoint(pt, period int) -> series`
- **For:** the center of the recent range. **Calculates:**
  `(highest(pt, period) + lowest(pt, period)) / 2`. **Shows:** the mid of the
  N-bar channel; a simple mean-reversion reference.

### `MidPrice` — Midpoint Price (HL)
`MidPrice(period int) -> series`
- **For:** the midpoint of the high/low range over a period. **Calculates:**
  `(highest(high, period) + lowest(low, period)) / 2`. **Shows:** same idea as
  MidPoint but always across the true high/low channel.

### `SAR` — Parabolic SAR (HL)
`SAR(acceleration, maximum float64) -> series`
- **For:** trailing stop-and-reverse levels. **Calculates:** a parabola that
  accelerates toward price by `acceleration` up to `maximum`. **Shows:** dots
  flip from below price (uptrend) to above price (downtrend); each flip is a
  reversal / stop signal. Excellent for trailing stops in trends, poor in chop.

### `SARExt` — Extended Parabolic SAR (HL)
`SARExt(startValue, offsetOnReverse, accelerationInitLong, accelerationLong, accelerationMaxLong, accelerationInitShort, accelerationShort, accelerationMaxShort float64) -> series`
- **For:** SAR with independent long/short acceleration settings and a reverse
  offset. **Calculates:** as SAR but with separate parameters per direction.
  **Shows:** same flip signals with finer control; positive values are long,
  negative values indicate short SAR levels.

### `SMA` — Simple Moving Average
`SMA(pt, period int) -> series`
- **For:** the plain trend baseline. **Calculates:** arithmetic mean of the last
  `period` values. **Shows:** trend direction; slower/smoother than EMA. Golden/
  death cross systems use SMA(50)/SMA(200).

### `T3` — Tillson T3
`T3(pt, period int, vFactor float64) -> series`
- **For:** a very smooth yet responsive MA. **Calculates:** multiple cascaded
  EMAs blended with the volume factor `vFactor` (0–1). **Shows:** smooth trend
  with reduced lag; higher `vFactor` = more responsive.

### `TEMA` — Triple Exponential Moving Average
`TEMA(pt, period int) -> series`
- **For:** even lower lag than DEMA. **Calculates:**
  `3·EMA − 3·EMA(EMA) + EMA(EMA(EMA))`. **Shows:** fast trend tracking; very
  responsive, sensitive to noise.

### `TRIMA` — Triangular Moving Average
`TRIMA(pt, period int) -> series`
- **For:** a doubly-smoothed average that weights the middle of the window most.
  **Calculates:** an MA of an MA (triangular weighting). **Shows:** very smooth
  trend, more lag; good for filtering noise.

### `WMA` — Weighted Moving Average
`WMA(pt, period int) -> series`
- **For:** a linearly-weighted trend line. **Calculates:** weights increase
  linearly toward the most recent bar. **Shows:** trend with less lag than SMA,
  more than EMA in some regimes.

---

## Momentum indicators

Momentum indicators measure the **speed and strength** of price moves. Many are
oscillators bounded in a range (e.g. 0–100) used to spot overbought/oversold
conditions and divergences.

### `ADX` — Average Directional Index (HLC)
`ADX(period int) -> series`
- **For:** measuring **trend strength** (not direction). **Calculates:** the
  smoothed average of the directional index DX. **Shows:** values > 25 typically
  indicate a strong trend; < 20 indicates a weak/ranging market. Pair with
  +DI/−DI for direction.

### `ADXR` — ADX Rating (HLC)
`ADXR(period int) -> series`
- **For:** a smoothed ADX. **Calculates:** `(ADX_now + ADX_nPeriodsAgo) / 2`.
  **Shows:** slower confirmation of trend-strength changes.

### `APO` — Absolute Price Oscillator
`APO(pt, fastPeriod, slowPeriod int, maType) -> series`
- **For:** momentum from the spread of two MAs. **Calculates:**
  `MA(fast) − MA(slow)` in **price units**. **Shows:** > 0 bullish momentum,
  < 0 bearish; zero-line crossings mirror MA crossovers.

### `Aroon` — Aroon (HL)
`Aroon(period int) -> (aroonDown, aroonUp, error)`
- **For:** identifying trend starts and strength via time-since-extreme.
  **Calculates:** AroonUp = how recently the period high occurred; AroonDown =
  how recently the low occurred (both 0–100). **Shows:** AroonUp near 100 with
  AroonDown low = strong uptrend; crossovers signal trend changes.

### `AroonOsc` — Aroon Oscillator (HL)
`AroonOsc(period int) -> series`
- **For:** a single-line version of Aroon. **Calculates:** `AroonUp − AroonDown`
  (−100…+100). **Shows:** positive = up-trend bias, negative = down-trend bias.

### `BOP` — Balance of Power (OHLC)
`BOP() -> series`
- **For:** buyer vs. seller strength within each bar. **Calculates:**
  `(close − open) / (high − low)`. **Shows:** positive = buyers dominate the bar,
  negative = sellers; often smoothed with an MA.

### `CMO` — Chande Momentum Oscillator
`CMO(pt, period int) -> series`
- **For:** pure momentum, similar to RSI. **Calculates:**
  `100·(sumUp − sumDown)/(sumUp + sumDown)` over the period (−100…+100).
  **Shows:** overbought > +50, oversold < −50; zero-line for momentum direction.

### `CCI` — Commodity Channel Index (HLC)
`CCI(period int) -> series`
- **For:** deviation of price from its statistical mean. **Calculates:**
  `(typicalPrice − SMA) / (0.015·meanDeviation)`. **Shows:** typically ranges
  ±100; > +100 = unusually strong (overbought/breakout), < −100 = unusually weak.

### `DX` — Directional Movement Index (HLC)
`DX(period int) -> series`
- **For:** the raw directional index behind ADX. **Calculates:**
  `100·|+DI − −DI| / (+DI + −DI)`. **Shows:** trend strength (noisier than ADX,
  which smooths it).

### `MACD` — Moving Average Convergence Divergence
`MACD(pt, fastPeriod, slowPeriod, signalPeriod int) -> (macd, signal, hist, error)`
- **For:** trend + momentum in one tool. **Calculates:**
  `macd = EMA(fast) − EMA(slow)`; `signal = EMA(macd, signalPeriod)`;
  `hist = macd − signal`. **Shows:** macd crossing above signal = bullish; below
  = bearish; the histogram shows momentum building/fading; zero-line crossings =
  trend change. Divergence vs. price warns of exhaustion.
  ```go
  macd, sig, hist, err := calc.MACD("binance", "BTC/USDT", "close", 12, 26, 9)
  i := len(macd) - 1
  if macd[i] > sig[i] && hist[i] > 0 { /* bullish crossover */ }
  ```

### `MACDExt` — MACD with selectable MA types
`MACDExt(pt, fastPeriod int, fastMAType, slowPeriod int, slowMAType, signalPeriod int, signalMAType) -> (macd, signal, hist, error)`
- **For:** MACD where each leg uses a chosen MA type. **Shows:** same signals as
  MACD with customizable smoothing.

### `MACDFix` — MACD fixed 12/26
`MACDFix(pt, signalPeriod int) -> (macd, signal, hist, error)`
- **For:** the classic 12/26 MACD with only the signal period configurable.

### `MinusDI` / `PlusDI` — Directional Indicators (HLC)
`MinusDI(period int) -> series`, `PlusDI(period int) -> series`
- **For:** trend direction. **Calculates:** the smoothed down (−DM) and up (+DM)
  movement normalized by ATR. **Shows:** +DI above −DI = uptrend; the reverse =
  downtrend. Crossovers are entry signals; combine with ADX for strength.

### `MinusDM` / `PlusDM` — Directional Movement (HL)
`MinusDM(period int) -> series`, `PlusDM(period int) -> series`
- **For:** the raw up/down movement before normalization. **Calculates:** the
  portion of each bar's range that is directional. **Shows:** building blocks of
  DI/ADX; rarely traded directly.

### `MFI` — Money Flow Index (HLCV)
`MFI(period int) -> series`
- **For:** a **volume-weighted RSI**. **Calculates:** RSI-style formula on
  `typicalPrice · volume` (money flow). **Shows:** overbought > 80, oversold < 20;
  because it uses volume it confirms whether momentum is backed by participation.

### `Momentum`
`Momentum(pt, period int) -> series`
- **For:** raw rate of movement. **Calculates:** `price − price[n periods ago]`.
  **Shows:** > 0 rising, < 0 falling; magnitude = speed.

### `PPO` — Percentage Price Oscillator
`PPO(pt, fastPeriod, slowPeriod int, maType) -> series`
- **For:** MACD expressed in **percent** so it's comparable across assets/prices.
  **Calculates:** `100·(MA(fast) − MA(slow)) / MA(slow)`. **Shows:** same as
  MACD but normalized.

### `ROC` / `ROCP` / `ROCR` / `ROCR100` — Rate of Change family
`ROC(pt, period int)`, `ROCP(pt, period int)`, `ROCR(pt, period int)`, `ROCR100(pt, period int)`
- **For:** momentum as change over N bars. **Calculates:**
  - `ROC` = `100·(p − p_n)/p_n` (percent),
  - `ROCP` = `(p − p_n)/p_n` (proportion),
  - `ROCR` = `p / p_n` (ratio),
  - `ROCR100` = `100·p / p_n`.
  **Shows:** acceleration/deceleration of price; zero (or 100 for ratios) is the
  no-change line.

### `RSI` — Relative Strength Index
`RSI(pt, period int) -> series`
- **For:** the classic bounded momentum oscillator. **Calculates:**
  `100 − 100/(1 + avgGain/avgLoss)` over the period → 0–100. **Shows:**
  > 70 overbought, < 30 oversold; 50 is the momentum midline; divergence vs.
  price is a strong reversal warning.
  ```go
  rsi, err := calc.RSI("binance", "BTC/USDT", "close", 14)
  if err == nil && rsi[len(rsi)-1] < 30 { /* oversold */ }
  ```

### `Stoch` — Stochastic Oscillator (HLC)
`Stoch(fastKPeriod, slowKPeriod int, slowKMAType, slowDPeriod int, slowDMAType) -> (slowK, slowD, error)`
- **For:** where close sits within the recent range. **Calculates:** %K =
  position of close in the high/low range, then smoothed into slowK/slowD.
  **Shows:** > 80 overbought, < 20 oversold; slowK crossing slowD = signal.

### `StochF` — Stochastic Fast (HLC)
`StochF(fastKPeriod, fastDPeriod int, fastDMAType) -> (fastK, fastD, error)`
- **For:** the un-smoothed, faster stochastic. **Shows:** more responsive, noisier
  than `Stoch`.

### `StochRSI` — Stochastic RSI
`StochRSI(pt, period, fastKPeriod, fastDPeriod int, fastDMAType) -> (fastK, fastD, error)`
- **For:** applying the stochastic formula **to RSI** for a hyper-sensitive
  oscillator. **Shows:** faster overbought/oversold turns than plain RSI; best in
  ranging markets.

### `Trix` — TRIX
`Trix(pt, period int) -> series`
- **For:** filtered momentum via triple-smoothed EMA. **Calculates:** rate of
  change of a triple-EMA. **Shows:** zero-line crossings = momentum shifts;
  filters out short-term noise, good for divergence.

### `UltOsc` — Ultimate Oscillator (HLC)
`UltOsc(period1, period2, period3 int) -> series`
- **For:** momentum across **three timeframes** to reduce false divergences.
  **Calculates:** a weighted blend of buying pressure over the three periods
  (0–100). **Shows:** > 70 overbought, < 30 oversold; divergences are the primary
  signal.

### `WilliamsR` — Williams %R (HLC)
`WilliamsR(period int) -> series`
- **For:** an inverted stochastic. **Calculates:** close relative to the N-bar
  high/low, scaled −100…0. **Shows:** > −20 overbought, < −80 oversold.

---

## Volume indicators

Volume indicators combine price with traded volume to confirm the conviction
behind moves.

### `Ad` — Chaikin A/D Line (HLCV)
`Ad() -> series`
- **For:** cumulative accumulation/distribution. **Calculates:** running sum of a
  volume-weighted "close location value" within each bar's range. **Shows:**
  rising = accumulation (buying pressure), falling = distribution; divergence vs.
  price flags weakening trends.

### `AdOsc` — Chaikin A/D Oscillator (HLCV)
`AdOsc(fastPeriod, slowPeriod int) -> series`
- **For:** momentum of the A/D line. **Calculates:** `EMA(AD, fast) − EMA(AD, slow)`.
  **Shows:** zero-line crossings signal shifts between accumulation and
  distribution.

### `OBV` — On Balance Volume
`OBV(pt) -> series`
- **For:** cumulative volume flow. **Calculates:** adds the bar's volume when
  price rises, subtracts it when price falls. **Shows:** a rising OBV confirms an
  uptrend; OBV/price divergence warns the move lacks volume support.

---

## Volatility indicators

### `ATR` — Average True Range (HLC)
`ATR(period int) -> series`
- **For:** the standard volatility gauge. **Calculates:** the smoothed average of
  True Range. **Shows:** absolute (price-unit) volatility; used to size stops and
  positions (e.g. stop = entry − 2·ATR). Not directional.

### `NATR` — Normalized ATR (HLC)
`NATR(period int) -> series`
- **For:** ATR as a **percent of price** so it's comparable across assets.
  **Calculates:** `100·ATR / close`. **Shows:** relative volatility.

### `TRANGE` — True Range (HLC)
`TRANGE() -> series`
- **For:** the per-bar volatility building block. **Calculates:**
  `max(high−low, |high−prevClose|, |low−prevClose|)`. **Shows:** single-bar range
  including gaps; ATR is its average.

---

## Price transform

Simple per-bar composite prices, often used as inputs to other indicators.

| Method | Calculates | Use |
|---|---|---|
| `AvgPrice()` (OHLC) | `(O+H+L+C)/4` | A balanced representative price for the bar. |
| `MedPrice()` (HL) | `(H+L)/2` | The bar's midpoint. |
| `TypPrice()` (HLC) | `(H+L+C)/3` | "Typical price"; input to CCI, MFI, etc. |
| `WCLPrice()` (HLC) | `(H+L+2C)/4` | "Weighted close"; emphasizes the close. |

---

## Cycle indicators (Hilbert Transform)

These use John Ehlers' Hilbert Transform to measure the market's dominant cycle.

### `HTDcPeriod` — Dominant Cycle Period
`HTDcPeriod(pt) -> series`
- **Shows:** the measured length (in bars) of the current dominant cycle — useful
  to adapt other indicators' periods.

### `HTDcPhase` — Dominant Cycle Phase
`HTDcPhase(pt) -> series`
- **Shows:** the phase angle within the current cycle (in degrees).

### `HTPhasor` — Phasor Components
`HTPhasor(pt) -> (inPhase, quadrature, error)`
- **Shows:** the in-phase and quadrature components of the price wave; building
  blocks for cycle analysis.

### `HTSine` — SineWave
`HTSine(pt) -> (sine, leadSine, error)`
- **Shows:** a sine and lead-sine line; their crossovers time cycle turns and
  help distinguish trending from cycling markets.

### `HTTrendMode` — Trend vs. Cycle Mode
`HTTrendMode(pt) -> series`
- **Shows:** `1` when the market is trending, `0` when it is cycling — a regime
  filter to switch strategy logic.

---

## Statistic functions

Rolling statistical measures over a price series.

### `Beta` — Beta of two series
`Beta(pt0, pt1, period int) -> series`
- **For:** sensitivity of `pt0` relative to `pt1`. **Shows:** > 1 = pt0 moves
  more than pt1; < 1 = less. (Both series are from the same symbol's candles —
  e.g. high vs. low.)

### `Correl` — Pearson Correlation
`Correl(pt0, pt1, period int) -> series`
- **For:** the linear correlation between two point series over the window
  (−1…+1). **Shows:** how tightly the two series move together.

### `LinearReg` — Linear Regression
`LinearReg(pt, period int) -> series`
- **For:** the value of the least-squares regression line at the current bar.
  **Shows:** a smoothed trend estimate (the regression "forecast" at t).

### `LinearRegAngle`
`LinearRegAngle(pt, period int) -> series`
- **Shows:** the slope of the regression line as an **angle in degrees** — steep
  positive/negative = strong trend.

### `LinearRegIntercept`
`LinearRegIntercept(pt, period int) -> series`
- **Shows:** the intercept of the regression line over the window.

### `LinearRegSlope`
`LinearRegSlope(pt, period int) -> series`
- **Shows:** the raw slope (price change per bar) — trend direction and speed.

### `StdDev` — Standard Deviation
`StdDev(pt, period int, nbDev float64) -> series`
- **For:** dispersion of price around its mean (scaled by `nbDev`). **Shows:**
  volatility; the width term inside Bollinger Bands.

### `TSF` — Time Series Forecast
`TSF(pt, period int) -> series`
- **For:** projecting the regression line one bar ahead. **Shows:** an estimated
  "next" value; comparing price to TSF gauges over/under-extension.

### `Var` — Variance
`Var(pt, period int) -> series`
- **For:** the variance (StdDev²) of the series over the window. **Shows:**
  volatility in squared units.

---

## Math transform & operators

Low-level element-wise helpers. Useful for building custom indicators from the
price series.

### Math transform (unary, `pt` only)
`Acos`, `Asin`, `Atan`, `Ceil`, `Cos`, `Cosh`, `Exp`, `Floor`, `Ln`, `Log10`,
`Sin`, `Sinh`, `Sqrt`, `Tan`, `Tanh`.

Each applies the named math function element-wise to the selected series and
returns a same-length series. E.g. `Ln("close")` gives log prices (handy for log
returns via `Sub` of successive values).

### Math operators

| Method | Signature | Calculates |
|---|---|---|
| `Add` | `(pt0, pt1)` | element-wise `pt0 + pt1` |
| `Sub` | `(pt0, pt1)` | element-wise `pt0 − pt1` |
| `Mult` | `(pt0, pt1)` | element-wise `pt0 × pt1` |
| `Div` | `(pt0, pt1)` | element-wise `pt0 ÷ pt1` |
| `Max` | `(pt, period)` | highest value over the period |
| `Min` | `(pt, period)` | lowest value over the period |
| `MaxIndex` | `(pt, period)` | index (offset) of the highest over the period |
| `MinIndex` | `(pt, period)` | index (offset) of the lowest over the period |
| `MinMax` | `(pt, period)` → `(min, max, error)` | lowest & highest over the period |
| `MinMaxIndex` | `(pt, period)` → `(minIdx, maxIdx, error)` | indices of the lowest & highest |
| `Sum` | `(pt, period)` | rolling sum over the period |

Example — a custom spread of the high vs. low channel:
```go
hi, _ := calc.Max("binance", "BTC/USDT", "high", 20)
lo, _ := calc.Min("binance", "BTC/USDT", "low", 20)
// or directly:
spread, _ := calc.Sub("binance", "BTC/USDT", "high", "low")
```

---

## Related

- [README.md](README.md) — compact parameter cheat-sheet.
- [manager.go](manager.go) — manager lifecycle & history retention.
- [manager_helpers.go](manager_helpers.go) — how `pointType` maps to series.
- [manager_talib_gen.go](manager_talib_gen.go) — the full method surface.
</content>
</invoke>
