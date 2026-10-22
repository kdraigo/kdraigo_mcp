# kdraigo_mcp

Model Context Protocol server for the [kdraigo](https://kdraigo.com) algorithmic trading platform. Drop one binary into your `mcpServers` config and Claude Code (or any MCP client) can scaffold strategies, run backtests, fetch analytics, and iterate end-to-end against the kdraigo backend.

Transport is **stdio** — the binary is launched as a subprocess by your MCP client. No hosting, no port, no public surface. The binary signs each outbound request to `kdraigo.com` with your Ed25519 key.

---

## Install

```bash
go install github.com/kdraigo/kdraigo_mcp/cmd/kdraigo-mcp@latest
```

`go install` reports the installed release tag automatically. From a clone, use
`make install` (or `make build`) to stamp the version from your local git tag.

Verify (prints the release tag):

```bash
kdraigo-mcp version
```

## Configure

You need an Ed25519 API key registered with `users_service`. Use the kdraigo dashboard or the `users_service/cmd/test_client` helper to get a `key_id` (UUID) and a hex-encoded `private_key`.

Create `~/.kdraigo/config.yaml`:

```yaml
auth:
  key_id: "00000000-0000-0000-0000-000000000000"
  private_key: "<128-hex-char-ed25519-private-key>"
endpoint: "https://kdraigo.com"   # optional; this is the default
```

Override the config path with `KDRAIGO_CONFIG` if you need a non-default location.

## Wire into Claude Code

```bash
claude mcp add --scope user kdraigo -- kdraigo-mcp
```

Other MCP clients (or a project's `.mcp.json`) take the usual entry:

```json
{
  "mcpServers": {
    "kdraigo": {
      "command": "kdraigo-mcp"
    }
  }
}
```

Restart the client. The tools below appear under the `kdraigo` server: 14 by default, 16 with the replay switch.

The two replay tools, `create_backtest_session` and `run_backtest_stream`, are off unless the server runs with `KDRAIGO_MCP_ENABLE_REPLAY=1`. They step candles through a session without running any strategy code, so they are not how a strategy is backtested: a strategy runs as its own program (`go run .`) through dev_sdk. A replay left parked also holds one of the account's session slots for up to an hour.

## Install the skills

```bash
kdraigo-mcp install-skill                    # ~/.claude/skills/<name>/SKILL.md
kdraigo-mcp install-skill --project <dir>    # <dir>/.claude/skills/<name>/SKILL.md
```

The skills are built into the binary, so they always match its tools. Restart Claude Code to load them. Releases before v1.1.0 wrote flat `~/.claude/skills/<name>.md` files that Claude Code never loaded; the installer names any it finds so you can delete them.

- [`kdraigo-strategy`](skills/kdraigo-strategy.md) — write the hypothesis → `go run .` → report → drill down → record → iterate loop, how to judge a result, and the pitfalls.
- [`kdraigo-indicators`](skills/kdraigo-indicators.md) — choose the right technical indicator by intent and wire it via the SDK.

---

## Tools

Results come back as a few header lines and tab-separated tables, with no JSON, so a page of rows fits the client's tool-output limit. A result stops at about 60 KB and its last line says how to get the rest (`offset`, or `save_to` for orders).

| Tool | What it does |
| ---- | ------------ |
| `get_sdk_docs` | Returns the embedded dev_sdk reference. Pass `section` to scope. |
| `get_indicator_docs` | Deep per-indicator reference (purpose, calculation, signals, usage). Pass `name` to scope to one indicator or category. |
| `scaffold_strategy` | `git clone`s a template from [`example_strategy`](https://github.com/kdraigo/example_strategy) into a target dir. |
| `get_session_report` | A backtest's or live session's results in one call: return, drawdown, return/DD, P&L split, trades and win rate per side, a table per instrument, P&L per month, and warnings (unfinished run, missing candles, few trades, open positions, concentration, costs). |
| `list_sessions` | The caller's sessions, newest first, with status and progress. |
| `get_session_detail` | One session's record: window, progress, starting wallets, instruments with fills and fees. |
| `get_session_orders` | Orders filtered by instrument, status, role and time; `with_reason` adds the strategy's reason and logs; `save_to` writes every match to a local CSV. |
| `get_session_positions` | Futures positions a session held, paged, with open and liquidated counts (empty for spot). |
| `get_session_funding` | Funding ledger, one row per settlement, paged, with totals (empty for spot). |
| `update_session_metadata` | Patches `name`, `notes`, `tags`, `favorite`. |
| `delete_session` | Removes a session and its data. |
| `get_analytics_types` | Lists available metric types (equity, drawdown, trading_frequency, etc.). |
| `get_analytics_series` | Time-bucketed series for a session metric, whole session or one instrument; at most 2,000 buckets. |
| `get_candles` | OHLCV from data_provider (public endpoint, no signing). |
| `create_backtest_session` | Replay only (`KDRAIGO_MCP_ENABLE_REPLAY=1`): creates a session on backtester_engine. |
| `run_backtest_stream` | Replay only: steps a session to its end over WS without running strategy code. |

## Authentication model

Requests are signed with **signing version 2**, the only version the platform accepts since 2026-10-11 (kdraigo-mcp v1.0.9+, dev_sdk v1.3.0+). Every signed request carries:

- `X-Key-ID`: your Ed25519 key UUID
- `X-Timestamp`: Unix seconds, within 5 minutes of the server clock
- `X-Nonce`: 32 hex characters, new for every request
- `X-Signature`: hex(ed25519(`KDRAIGO-SIG-V2\nMETHOD\nPATH\nCANONICAL_QUERY\nTIMESTAMP\nNONCE\nhex(sha256(BODY))`))

Each signature is accepted once, so a retry signs again. The backtester_engine uses uppercased variants (`X-API-KEY` / `X-SIGNATURE` / `X-TIMESTAMP`, with the same `X-Nonce`), and its WebSocket sends them as headers on the upgrade, not in the URL. The binary handles all of this.

## Layout

```text
kdraigo_mcp/
  cmd/kdraigo-mcp/         CLI entry, install-skill subcommand
  internal/
    config/                config.yaml loader
    auth/                  Ed25519 signer
    client/                signed HTTP + WS clients
    render/                header + TSV rendering under the output cap
    tools/                 the MCP tools, embedded SDK + indicator docs
  skills/                  the skills, embedded into the binary
```

## Templates

The [`example_strategy`](https://github.com/kdraigo/example_strategy) repo holds three starter templates: `basic`, `with-indicators`, `with-risk-manager`. They're cloned at scaffold time so updates to templates ship without binary releases.

## License

See [LICENSE](LICENSE).
