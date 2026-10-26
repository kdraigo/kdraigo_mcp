# kdraigo_mcp

Model Context Protocol server for the [kdraigo](https://kdraigo.com) algorithmic trading platform. Drop one binary into your `mcpServers` config and Claude Code (or any MCP client) can scaffold strategies, run backtests, fetch analytics, and iterate end-to-end against the kdraigo backend.

Two ways to use it:

- **Hosted** at `https://mcp.kdraigo.com/mcp`: add that URL to claude.ai, ChatGPT, Gemini CLI or Claude Code as a remote MCP server, sign in with your wallet on kdraigo.com, and nothing runs on your machine. See [Hosted server](#hosted-server).
- **Local**, over **stdio**: the binary is launched as a subprocess by your MCP client. No hosting, no port, no public surface. It signs each outbound request to `kdraigo.com` with your Ed25519 key.

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

Restart the client. The tools below appear under the `kdraigo` server: 29 by default, 31 with the replay switch.

The two replay tools, `create_backtest_session` and `run_backtest_stream`, are off unless the server runs with `KDRAIGO_MCP_ENABLE_REPLAY=1`. They step candles through a session without running any strategy code, so they are not how a strategy is backtested. A strategy runs as its own program through dev_sdk: locally with `go run .`, or in the cloud workspace with `workspace_run`. A replay left parked also holds one of the account's session slots for up to an hour.

## Install the skills

```bash
kdraigo-mcp install-skill                    # ~/.claude/skills/<name>/SKILL.md
kdraigo-mcp install-skill --project <dir>    # <dir>/.claude/skills/<name>/SKILL.md
```

The skills are built into the binary, so they always match its tools. Restart Claude Code to load them. Releases before v1.1.0 wrote flat `~/.claude/skills/<name>.md` files that Claude Code never loaded; the installer names any it finds so you can delete them.

- [`kdraigo-strategy`](skills/kdraigo-strategy.md) — write the hypothesis → run (`go run .` or the cloud workspace) → report → drill down → record → iterate loop, how to judge a result, and the pitfalls.
- [`kdraigo-indicators`](skills/kdraigo-indicators.md) — choose the right technical indicator by intent and wire it via the SDK.

---

## Tools

Results come back as a few header lines and tab-separated tables, with no JSON, so a page of rows fits the client's tool-output limit. A result stops at about 60 KB and its last line says how to get the rest (`offset`, or `save_to` for orders).

| Tool | What it does |
| ---- | ------------ |
| `get_sdk_docs` | Returns the embedded dev_sdk reference. Pass `section` to scope. |
| `get_indicator_docs` | Deep per-indicator reference (purpose, calculation, signals, usage). Pass `name` to scope to one indicator or category. |
| `scaffold_strategy` | `git clone`s a template from [`example_strategy`](https://github.com/kdraigo/example_strategy) into a target dir. |
| `get_session_report` | A backtest's or live session's results in one call: return, drawdown, return/DD, P&L split, trades and win rate per side, average R with its standard error, net without the 5 best trades, losing streak, time underwater, daily Sharpe/Sortino/Calmar, a table per instrument, P&L per month, and the platform's warnings (unfinished run, missing candles, few trades, no stops, open positions, concentration). |
| `get_trades` | Every trade with entry, exit, net, initial stop and R; filter, sort by net or R, or `save_to` a CSV. Needs analytics_service with report version 2. |
| `get_breakdown` | Trades grouped by period, instrument, side, exit kind, holding time, weekday or hour, each group with win rate, net, profit factor and R. |
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
| `workspace_create` | Starts a cloud workspace from a template (`basic`, `with-indicators`, `with-risk-manager`) on a dev_sdk version. |
| `workspace_list` | The caller's workspaces. |
| `workspace_files` | A workspace's files with sizes, versions and who wrote each (the Lab or API). |
| `workspace_read_file` | A file's full content, then its version. |
| `workspace_write_file` | Creates or replaces a file (flat `.go` files; the platform owns `go.mod`). With `base_version`, a file the user changed in the Lab since is refused, not overwritten. |
| `workspace_delete_file` | Deletes a file; with `version`, only that version. |
| `workspace_build` | Compiles on a sandbox machine; returns import-check or compiler errors as `file:line: message`. |
| `workspace_run` | Builds and runs on the sandbox machine with a key minted for the run; waits up to `wait` s and names the backtest sessions it made. Takes your own `env`. |
| `workspace_run_status` | A build's or run's status, sessions and last lines of output; `wait` follows one still going. |
| `workspace_logs` | A run's output, paged by byte offset (capped at 1 MB per run). |
| `workspace_stop` | Stops a run still going; its key is revoked. |
| `workspace_runs` | A workspace's builds and runs, newest first. |
| `workspace_delete` | Deletes a workspace with its files and run history; its sessions stay. |
| `create_backtest_session` | Replay only (`KDRAIGO_MCP_ENABLE_REPLAY=1`): creates a session on backtester_engine. |
| `run_backtest_stream` | Replay only: steps a session to its end over WS without running strategy code. |

## Cloud workspace

The `workspace_*` tools write, build and run a strategy on the platform: no local Go, checkout or keys. The workspace service compiles it offline on a sandbox machine (the caller's own on admin accounts, shared with other users' runs, each in its own container, on verified ones), then runs it with a key minted for that run alone, which can only open backtest sessions and is revoked when the run ends. Under gVisor the program can reach nothing but the platform API. `get_sdk_docs section="cloud workspace"` has the rules (flat `.go` files, allowed imports, `env`, limits). It is in beta for admin accounts first.

## Hosted server

`kdraigo-mcp serve --http` is the same tool set as a remote MCP server (streamable HTTP, stateless), as run at `mcp.kdraigo.com`. It leaves out `scaffold_strategy`, which writes to the caller's disk, and the replay tools; strategies run in the cloud workspace.

- **Sign-in** is OAuth 2.1 against the platform's authorization server (users_service), per the MCP authorization spec: an unauthenticated request gets `401` with `WWW-Authenticate: Bearer resource_metadata=…`, and the server serves its protected-resource metadata (RFC 9728) at `/.well-known/oauth-protected-resource[/mcp]`. Apps register with a Client ID Metadata Document or Dynamic Client Registration, use PKCE S256, and get tokens bound to the server's URL.
- **Each request's token** is introspected on users_service's internal port (cached for a minute) and must name this server as its audience.
- **Platform calls** are signed with an API key minted for that connected app (24 h, renewed): the app's token never leaves the server. Disconnecting the app on kdraigo.com's Account page revokes both.
- **Configuration:** `KDRAIGO_MCP_RESOURCE`, `KDRAIGO_MCP_ISSUER`, `KDRAIGO_MCP_USERS_INTERNAL_URL`, `KDRAIGO_MCP_INTERNAL_TOKEN`, `KDRAIGO_MCP_ADDR` (see `cmd/kdraigo-mcp/serve.go`). The `Dockerfile` builds the image; the `image` workflow publishes it on a release tag.

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
    hosted/                the remote server: OAuth resource, introspection, per-app keys
    render/                header + TSV rendering under the output cap
    tools/                 the MCP tools, embedded SDK + indicator docs
  skills/                  the skills, embedded into the binary
```

## Templates

The [`example_strategy`](https://github.com/kdraigo/example_strategy) repo holds three starter templates: `basic`, `with-indicators`, `with-risk-manager`. They're cloned at scaffold time so updates to templates ship without binary releases.

## License

See [LICENSE](LICENSE).
