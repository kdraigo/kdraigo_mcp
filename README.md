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

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "kdraigo": {
      "command": "kdraigo-mcp"
    }
  }
}
```

Restart Claude Code. The 12 tools below appear under the `kdraigo` server.

## Install the skills

```bash
kdraigo-mcp install-skill
```

This fetches the skills below from this repo and writes them to `~/.claude/skills/`. Reload Claude Code to see them.

- [`kdraigo-strategy`](skills/kdraigo-strategy.md) — the scaffold → backtest → analyze → iterate loop.
- [`kdraigo-indicators`](skills/kdraigo-indicators.md) — choose the right technical indicator by intent and wire it via the SDK.

---

## Tools

| Tool | What it does |
| ---- | ------------ |
| `get_sdk_docs` | Returns the embedded dev_sdk reference. Pass `section` to scope. |
| `get_indicator_docs` | Deep per-indicator reference (purpose, calculation, signals, usage). Pass `name` to scope to one indicator or category. |
| `scaffold_strategy` | `git clone`s a template from [`example_strategy`](https://github.com/kdraigo/example_strategy) into a target dir. |
| `create_backtest_session` | POSTs `/api/v1/dev/session` on backtester_engine, returns a session ID. |
| `run_backtest_stream` | Drives a session to completion via WS. Returns aggregate stats. |
| `list_sessions` | Paginated session list from frontend_api. |
| `get_session_detail` | Single session with orders + analytics summary. |
| `update_session_metadata` | Patches `name`, `notes`, `tags`, `favorite`. |
| `delete_session` | Removes a session and its data. |
| `get_analytics_types` | Lists available metric types (equity, trading_frequency, etc.). |
| `get_analytics_series` | Time-bucketed series for a session metric. |
| `get_candles` | OHLCV from data_provider (public endpoint, no signing). |

## Authentication model

Every signed request carries:

- `X-Key-ID` — your Ed25519 key UUID
- `X-Signature` — hex(ed25519(`METHOD\nPATH\nTIMESTAMP\nBODY`))
- `X-Timestamp` — Unix seconds

The backtester_engine endpoint uses uppercased variants (`X-API-KEY` / `X-SIGNATURE` / `X-TIMESTAMP`); the binary handles this automatically.

## Layout

```text
kdraigo_mcp/
  cmd/kdraigo-mcp/         CLI entry, install-skill subcommand
  internal/
    config/                config.yaml loader
    auth/                  Ed25519 signer
    client/                signed HTTP + WS clients
    tools/                 the 12 MCP tools, embedded SDK + indicator docs
  skills/                  open-source skills (canonical source)
```

## Templates

The [`example_strategy`](https://github.com/kdraigo/example_strategy) repo holds three starter templates: `basic`, `with-indicators`, `with-risk-manager`. They're cloned at scaffold time so updates to templates ship without binary releases.

## License

See [LICENSE](LICENSE).
