---
name: kdraigo-strategy
description: End-to-end strategy development loop on the kdraigo trading platform. Use when the user wants to scaffold, backtest, analyze, or iterate on a Go trading strategy that runs against kdraigo.
---

# kdraigo-strategy

You are operating against the kdraigo algorithmic trading platform via the `kdraigo` MCP server. Every platform interaction goes through MCP tools; do not curl endpoints or invent client code.

## When to invoke

The user says any of: "scaffold a strategy", "run a backtest on kdraigo", "analyze this session", "iterate on my bot", or names a kdraigo session ID.

## The loop

1. **Recall the SDK surface.** Call `get_sdk_docs` once at the start of a fresh task. If the user is asking about one area (clock, history fetch, order placement), pass `section` to scope it.
2. **Scaffold when starting from scratch.** Call `scaffold_strategy` with `template=basic` for new bots, `with-indicators` when the user mentions RSI/MA/etc., and `with-risk-manager` when they mention position sizing, stop-loss, or take-profit. Always pass an absolute `dir` and a kebab-case `name`. Show the user the generated `main.go` before editing.
3. **Create the session.** Call `create_backtest_session` with the exchange, pair, timeframe, date range, asset, and initial balance. Confirm date range with the user if they were vague — drift here wastes a real run.
4. **Run.** Call `run_backtest_stream` with the returned `session_id`. This blocks until the engine reports done; for long runs (multi-month 1m) warn the user before invoking.
5. **Pull results.** Call `get_session_detail` for orders + summary metrics, then `get_analytics_series` with `type=equity` for the equity curve. Use `timeframe=1h` for multi-week runs, `1m` for short runs.
6. **Reason.** Before proposing changes, summarise: total trades, win rate, max drawdown, profit factor, and where the curve went sideways. If results are weak, name the *mechanism* (e.g. "entries are correct but exits are too eager — 70% of winning trades give back >40% of peak unrealized profit") rather than offering generic advice.
7. **Iterate.** Propose specific code edits to the scaffolded file. Re-run by jumping back to step 3 with a new `session_id`. Use `update_session_metadata` after each meaningful run to label it (`name`, `notes`, `tags=[v1, v2, ...]`).

## Rules

- Never hardcode `KeyID` or `PrivateKey` in scaffolded code — the templates already read them from env.
- After a backtest, always assign a name + notes via `update_session_metadata` before moving on. Future-you will need to find this run again.
- When the user asks "why did this trade happen", check the order's `Reason` and `Logs` fields in `get_session_detail` before guessing. The strategy author put them there for a reason.
- Don't propose multiple unrelated changes at once. Single hypothesis per iteration → single run → measure.
- `get_candles` is for ad-hoc inspection of OHLCV data. It does not run a strategy. Don't use it to "simulate" anything.

## Output style

Be concrete. After a run, lead with the two or three numbers that matter (final equity, max drawdown, trades) and the one observation that drives the next change. Don't restate the whole tool output — it's already in the conversation.
