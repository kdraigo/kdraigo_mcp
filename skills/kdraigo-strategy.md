---
name: kdraigo-strategy
description: Develop, backtest and judge Go trading strategies on the kdraigo platform through the kdraigo MCP server, locally or in the cloud workspace. Use when the user wants to write or scaffold a strategy, run or analyse a backtest, compare runs, decide whether a change helped, or names a kdraigo session id.
---

# kdraigo-strategy

You develop strategies on the kdraigo platform. Platform data comes through the `kdraigo` MCP tools; do not curl endpoints or write client code for them.

## How a strategy runs

A strategy is a Go program built on dev_sdk. Running it creates a backtest session on the engine and drives it with the strategy's own code. It runs one of two ways:

- **Cloud workspace** (no local Go, checkout or keys): `workspace_create` from a template, `workspace_write_file`, then `workspace_run`.
  - The platform builds and runs it on a sandbox machine of the user's own.
  - The result lists the backtest sessions the run made.
  - A workspace is flat `.go` files with a fixed set of allowed imports. Read `get_sdk_docs section="cloud workspace"` before writing one.
  - Use it when the user has no local setup or asks for it.
- **Locally:** `go run .` in the strategy's folder, in the user's own repo. The program logs the session id once the session exists:

```text
Backtest Engine: Successfully initialized Session ID: 3f2a9c1e-…
```

Capture that id; every result tool takes it. The window, instruments, wallets and timeframe come from the strategy's own config (see `get_sdk_docs`). Keys come from `KDRAIGO_KEY_ID` / `KDRAIGO_PRIVATE_KEY`; never write them into code.

## The loop

1. **Read the SDK first.** `get_sdk_docs` once per task (`section=` to narrow it). For indicators follow the `kdraigo-indicators` skill and `get_indicator_docs`. Don't guess names, signatures or `pointType`.
2. **Scaffold or open the strategy.**
   - **Cloud:** `workspace_create` (`basic`, `with-indicators`, `with-risk-manager`), or `workspace_list` to reopen one, then `workspace_read_file`.
   - **Locally:** `scaffold_strategy` with an absolute `dir`, or work in the user's repo.
3. **Write the hypothesis down before running.** What changes, why it should work (the mechanism), and the rule that decides it, e.g. "return/DD ≥ baseline × 1.1 on the test window and at least 6/10 instruments positive". Pick the train, test and hold-out windows now. The hold-out stays untouched until the rule is frozen.
4. **Run.**
   - **Cloud:** `workspace_run` waits up to 150 s. For a longer run, follow it with `workspace_run_status run_id=… wait=120`. Its result names the session ids.
     - A refused or failed build lists `file:line` problems: fix and run again.
     - `workspace_build` alone is the quick compile check after an edit.
   - **Locally:** `go run .` from the strategy folder. Run anything longer than a few minutes in the background and capture the log. Take the session id from the log line above.
   - Either way, `get_session_detail` or `list_sessions` show status and progress while it runs.
5. **Report.** `get_session_report` first, every time. **Read its warnings before its numbers.** An unfinished run, missing candles, positions open at the end or a result carried by one trade change what the numbers mean.
6. **Drill down** only as far as the question needs:
   - `get_trades`: one row per trade, with entry and exit, net, the initial stop and R. Use `sort=net` or `sort=net_r` to see the trades that made or broke the result, and filter by `side`, `exit_kind` or instrument. `save_to=/abs/path/trades.csv` gets every trade for your own analysis.
   - `get_breakdown`: trades grouped by `month`, `quarter`, `instrument`, `side`, `exit_kind`, `hold`, `weekday` or `hour`. Use `at=entry` to ask when entries work rather than when exits land.
   - `get_session_orders`: filter by `role` (`stop_loss`, `take_profit`, …), `status`, `pair`, time. Add `with_reason` (≤50 rows) to see why the strategy acted. `save_to` writes every order to a CSV.
   - `get_session_positions` / `get_session_funding`: futures only; paged.
   - `get_analytics_series`: `type=equity` or `drawdown`, `timeframe=1h` or `1d`, optionally `exchange` + `pair` for one instrument.
7. **Record.** `update_session_metadata` on every run you keep: a `name` that says the variant, `notes` with the hypothesis and the verdict, `tags` such as `exp:<slug>`, `baseline`, `variant:<x>`.
8. **Iterate:** one hypothesis, one change, one run. Compare against the same baseline, on the same window and the same instruments.

## Judging a result

- **Return over drawdown, not return.** A change that also cuts risk (a tighter stop, a smaller size, an earlier exit) must beat the baseline at the same drawdown. Otherwise it is just a smaller bet.
- **Enough trades.** Below about 30 closed trades, win rate and profit factor are noise. Per instrument it needs even more.
- **Breadth.** Count the instruments with positive net (x of N). A result made by one coin is about that coin.
- **Time.** Look at the months table for the worst month, the number of losing months and the longest losing run. One great month can make a year.
- **Fat tails.** The report gives net without the 5 best trades. If that is negative, the result is a handful of trades; `get_trades sort=net` shows which.
- **R multiples.** The report's average R (net over the risk to the initial stop) comes with its standard error. An average under two standard errors from zero is not an edge yet. R needs an initial stop on every trade: if the report says R covers few trades, add the stop (see below) before judging per-trade quality.
- **Daily ratios.** Sharpe, Sortino and Calmar come from daily closed-trade equity, so they understate volatility inside trades. Use them to rank variants on the same window, not as absolute claims.
- **Open at end.** Positions still open at the end are valued at their last fill. If they are a big share of the result, the result is unfinished.

## Pitfalls

- **Look-ahead.** An indicator on a bar that is still forming uses the future. Decide on closed bars only. The same applies to plot values: a value belongs to its bar's close.
- **Incomplete run.** Check that the run reached the end of its window (status, progress, last candle) before comparing. A run that stopped early looks like a calm strategy.
- **End-of-run settlement.** Trades closed by the end of the window are not exits the strategy chose.
- **In-sample selection.** Choosing coins, parameters or sessions by how they did on the same window you report is fitting. Choose on train, report on test.
- **Multiple comparisons.** Every variant you try is a draw. After ten tries, the best of ten beating the baseline is expected by chance. Keep count and say it.
- **Costs left out.** Net R includes fees and funding; gross R (in the trades CSV) does not. Say which you used.
- **Small wallets.** A tiny balance skips trades that round to zero size, or that fall below the exchange minimum. Check the order count against what the logic should have sent.
- **One regime.** A window that is all trend or all chop tells you about that regime. Test across at least two.
- **The hold-out is used once.** After you look at it, it is no longer a hold-out.
- **Cloud: a write replaces the whole file.** `workspace_write_file` takes the file's full new content, so read it first and send all of it.
- **Cloud: keys and endpoint come from the run.** Keep reading `KDRAIGO_KEY_ID`, `KDRAIGO_PRIVATE_KEY` and `KDRAIGO_BACKTESTER_URL` from the environment as the templates do. The run supplies its own key; a hard-coded one is refused.

## Conventions that make the tools more useful

- Put the decision context in each order's `Reason` (a map, ≤4 KB) and short `Logs` lines (≤16 KB). They come back with `with_reason` and in the CSV.
- **Entry orders carry `reason.stop`:** the initial stop price, as a number. R is measured to the first protective stop order placed before the trade starts to exit; without one, this `stop` stands in.
- Protective stops are `STOP_LOSS` orders with both `StopPrice` and `Price` set; the backtest engine rejects a stop with only a trigger.

## Output style

Lead with the two or three numbers that decide the question (return/DD against the baseline, trades, instruments positive), then any warning that qualifies them, then the one observation that drives the next change. Don't restate tool output that is already in the conversation.
