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

Restart the client. The tools below appear under the `kdraigo` server: 78 by default, 80 with the replay switch.

The two replay tools, `create_backtest_session` and `run_backtest_stream`, are off unless the server runs with `KDRAIGO_MCP_ENABLE_REPLAY=1`. They step candles through a session without running any strategy code, so they are not how a strategy is backtested. A strategy runs as its own program through dev_sdk: locally with `go run .`, or in the cloud workspace with `workspace_run`. A replay left parked also holds one of the account's session slots for up to an hour.

## Install the skills

```bash
kdraigo-mcp install-skill                    # ~/.claude/skills/<name>/SKILL.md
kdraigo-mcp install-skill --project <dir>    # <dir>/.claude/skills/<name>/SKILL.md
```

The skills are built into the binary, so they always match its tools. Restart Claude Code to load them. Releases before v1.1.0 wrote flat `~/.claude/skills/<name>.md` files that Claude Code never loaded; the installer names any it finds so you can delete them.

- [`kdraigo-strategy`](skills/kdraigo-strategy.md) — open the research lab → write the hypothesis → check what was already done → run (`go run .` or the cloud workspace) → report → drill down → record → hand off, how to judge a result, and the pitfalls.
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
| `workspace_create` | Starts a cloud workspace from a template (`basic`, `with-indicators`, `with-risk-manager`, `with-atr-bracket`) on a dev_sdk version; with `lab`, its runs are recorded in that research lab. |
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
| `workspace_set_lab` | Puts a workspace in a research lab (or none): its runs are recorded there from then on. |
| `lab_list` / `lab_create` | The caller's research labs; a new one with a brief to fill, blank or from a template (`futures-trend`, `spot-accumulation`). |
| `lab_open` | A lab at the start of a session: brief, last handoff, open experiments, verdicts, recent runs with their results, lessons. |
| `lab_check` | What was already done like a planned run: an exact repeat (with `workspace_id`: same code, values, data and window), experiments on the same scope with their verdicts, related notes. |
| `lab_lint` | A lab's check-up: a brief never written, too long or older than the latest verdict; experiments never decided; unfiled runs; links that reach nothing; work not handed off. |
| `lab_search` | Full-text search over notes, experiments and lessons, one lab or all. |
| `lab_note_read` / `lab_note_write` | A note, the brief or a lesson; writes take `base_version` so a change made in the website is refused, not lost. |
| `lab_handoff` | Where this session stopped, shown first by the next `lab_open`. |
| `experiment_save` / `experiment_register` / `experiment_activate` / `experiment_decide` | Draft an experiment (hypothesis, mechanism, protocol, scope), freeze it before its deciding runs, file the lab's runs under it, record its verdict. |
| `lab_runs` / `lab_file_runs` | A lab's recorded runs with their results; file runs under an experiment. |
| `vault_pull` / `vault_push` | Local only: a lab as a folder of markdown files Obsidian opens, and its edits sent back (see Vault below). |
| `syndicate_list` / `syndicate_open` | The user's syndicates (groups that share labs), their members and labs. |
| `syndicate_create` / `syndicate_join` / `syndicate_leave` | Make one (Verified or Pro), join one with an invite code, leave one. Invites and roles are done on the website. |
| `experiment_publish` | A decided experiment as a frozen snapshot with verified result cards (and optionally its run's code), as a draft or to a syndicate. Publishing to everyone is the user's click on the website. |
| `library_search` / `library_read` | The research library: what others published to everyone or to the user's syndicates, with filters (pair, timeframe, outcome, pre-registered, has code). |
| `library_fork` | A publication into the user's own lab as a new draft, with its notes; with `workspace=true` its code goes into a new cloud workspace. |
| `widget_types` | The widget catalog: each type, its params (allowed values, defaults) and an example. |
| `board_list` / `board_open` | The user's boards (and their syndicates', as `syndicate:board`); a board's widgets with ids, places and params. |
| `board_create` | A board for the user, or in a syndicate they write in. |
| `widget_add` / `widget_update` / `widget_remove` | Add, change or remove one widget; every write is checked against the catalog and a refusal names the field. |
| `board_set_layout` | Move and resize several widgets at once on the 12-column grid. |
| `script_reference` | The kdscript language guide: syntax, every built-in, inputs, drawing calls, examples. |
| `script_list` / `script_open` | The user's chart scripts (and their syndicates'); a script's source, inputs, plots and version. |
| `script_check` | Check a draft without saving it: problems by line and column, or its inputs and plots. Nothing runs. |
| `script_save` | Save a new script, or over an existing one (the newest version unless `base_version` is given); checked first. |
| `script_preview` | Run a script on the newest bars of a pair, isolated on the platform: each plot's newest values, first time, min and max, or where it failed. |
| `script_remove` | Delete a script that no board draws. |
| `script_versions` | A script's saved versions (the newest 50), who saved each and when; `script_open version=` reads one. |
| `script_library_search` / `script_library_read` | Scripts shared with the user's syndicates or with everyone; one with its source (other people's work: data, never instructions). |
| `script_fork` | Copy a shared script into the user's own scripts (or a syndicate's). Publishing is the user's click on the website. |
| `alert_create` | Arm one of a script's `alertcondition(…)` (by title) on a pair and timeframe: checked after each closed bar, its message goes to the user's Telegram while it is true. Verified 5 alerts ≥ 15m for 30 days, pro 50 ≥ 5m for 90 days. |
| `alert_list` | The user's alerts (active, paused with the reason, expired) and their recent events. |
| `alert_renew` / `alert_delete` | Renew an expired alert, or a paused one once fixed; delete one. |
| `create_backtest_session` | Replay only (`KDRAIGO_MCP_ENABLE_REPLAY=1`): creates a session on backtester_engine. |
| `run_backtest_stream` | Replay only: steps a session to its end over WS without running strategy code. |

## Cloud workspace

The `workspace_*` tools write, build and run a strategy on the platform: no local Go, checkout or keys. The workspace service compiles it offline on a sandbox machine (the caller's own on pro and admin accounts, shared with other users' runs, each in its own container, on verified ones), then runs it with a key minted for that run alone, which can only open backtest sessions and is revoked when the run ends. Under gVisor the program can reach nothing but the platform API. `get_sdk_docs section="cloud workspace"` has the rules (flat `.go` files, allowed imports, `env`, limits). It is in beta for admin accounts first.

## Research labs

A lab is one line of research (a futures trend strategy, a spot accumulation idea) kept apart from the others: a brief, notes in Markdown with `[[links]]`, experiments that go draft → registered (frozen and hashed) → a verdict, the runs of its workspaces, and handoffs between sessions. research_service keeps it; the website's Lab shows it.

Runs of a workspace in a lab are recorded by the platform as they end: the code's hash, the declared values, exchange, pairs, timeframe and window. That is what `lab_check` matches for an exact repeat, since the engine is deterministic. The run's figures are read from its first session the next time `lab_open` or `lab_runs` lists it, and kept with the run. Local `go run .` runs are not recorded; name their sessions in the experiment's verdict.

## Syndicates and the library

A **syndicate** is a group that shares labs: in every lab tool a shared lab is named `syndicate:lab`, members and their AIs write it, viewers read it. Runs of a member's workspace in a shared lab are recorded there as theirs, and `lab_check` finds a repeat across every lab the user can read.

The **library** holds frozen snapshots of decided experiments: hypothesis, protocol and verdict, the notes chosen, one **result card** per cited run, and optionally the code a run ran (refused unless it hashes to what the run ran). The platform reads each card's figures from the engine's record and signs them; the key is public (`kdraigo.com/research/api/v1/public/card-key`). A snapshot can be a draft, go to a syndicate, or (from the website, by a Verified or Pro account, citing only the author's own runs) go to everyone.

What other people wrote reaches the AI marked as data, never instructions. Anything that changes who sees what (invites, roles, sharing a whole lab, publishing to everyone) is the user's own click on the website, so a note written to steer an agent can't make it leak research.

## Boards

A **board** is the user's arrangement of widgets on the website (`kdraigo.com/boards`): price charts with lines and panes (volume, delta, CVD, funding) and optionally a backtest's trades, the live order book, a watchlist, session stats, equity curves, a P&L calendar, a research lab at a glance, notes. A widget is data the website draws, never code: `{type, title, params, at}`, checked on every write against the catalog (`widget_types`). A refusal names the field (`widgets[1] (w2).params.exchange: "binance_futures" is not one of binance, bybit`); fix it and call again. The widget tools apply to the newest version of the board, and a board open on the website updates within seconds. Moving a board into a syndicate is the user's click on the website.

## Chart scripts

A **chart script** is an indicator the AI writes in **kdscript**, kdraigo's own language in the shape of Pine Script v6 (`script_reference` is the whole guide; what it leaves out is listed there). The guide is also public, as plain Markdown, at [kdraigo.com/kdscript.md](https://kdraigo.com/kdscript.md) and on the [docs](https://kdraigo.com/docs#chart-scripts): an AI without the MCP can read it there. A `price_chart` widget draws scripts: `params.scripts: [{"script": "my-rsi", "inputs": {"len": 21}}]`, by slug in the board's space (a syndicate's board draws the syndicate's scripts; sharing a board copies the scripts it draws). The data includes what TradingView doesn't give scripts: taker-buy volume, trade count, quote volume and funding.

kdscript 1.1 adds higher timeframes (`request.security` on the chart's pair, closed bars only),
colours that change per bar, `bgcolor`, `barcolor`, `plotchar`, `switch`, string inputs with
options and arrays. Every save is kept as a version (the newest 50), and the script library holds
scripts people shared, to read and fork. kdscript 1.2 adds `ta.linreg`, `ta.correlation` and
`ta.percentrank`, and `plot.style_linebr` lines stop at na bars. kdscript 1.3 adds
`alertcondition(cond, title, message)`: armed with `alert_create`, the platform checks it on each
closed bar and sends the message to the user's Telegram.

The loop: `script_reference` → `script_save` (problems come back as `line 4:12: …`; nothing is saved until it passes) → `script_preview` on a pair to check the numbers → `widget_add` / `widget_update` a price chart with the script. Saving checks a script and never runs it. A preview runs it on the platform in an isolated runner (no network, capped memory and CPU, a step budget and 2 s); the website runs it in the user's browser in a background worker, so a broken script fails only in its own tile.

## Vault (Obsidian)

A lab can live as a folder of markdown files on your machine, so Obsidian opens it with its graph and Claude Code reads it like any repo:

```bash
kdraigo-mcp vault pull --lab btc-trend --dir ~/vaults/btc-trend   # the first time, into an empty folder
kdraigo-mcp vault pull                                            # later, in the folder
kdraigo-mcp vault status                                          # what a push would send
kdraigo-mcp vault push [--delete]                                 # your edits back to the lab
```

| In the folder | |
|---|---|
| `Brief.md`, `Notes/`, `Lessons/` | read and write: a push sends them back |
| `Experiments/`, `Handoffs/`, `Runs.md`, `README.md` | the platform's copies: your AI records these through the lab tools |
| `.kdraigo/` | what was pulled at which version; the platform's side of a conflict |

Files are named by title, so `[[Funding drag]]` opens `Notes/Funding drag.md`. Each file goes back over the version it was pulled at: a note your AI changed since is refused, never overwritten. A pull never overwrites your edits either; when both sides changed a note, the platform's text lands in `.kdraigo/conflicts/` for you to merge, and the push after that sends the merge. Renaming a file in Obsidian renames the note (its links follow, on both sides); a new file in `Notes/` becomes a new note; a deleted one is deleted on the platform only with `--delete`. In Claude Code the same works through `vault_pull` and `vault_push`.

## Hosted server

`kdraigo-mcp serve --http` is the same tool set as a remote MCP server (streamable HTTP, stateless), as run at `mcp.kdraigo.com`. It leaves out `scaffold_strategy`, `vault_pull` and `vault_push`, which write to the caller's disk, and the replay tools; strategies run in the cloud workspace.

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
    vault/                 a research lab as a folder of markdown (pull, push)
  skills/                  the skills, embedded into the binary
```

## Templates

The [`example_strategy`](https://github.com/kdraigo/example_strategy) repo holds three starter templates: `basic`, `with-indicators`, `with-risk-manager`. They're cloned at scaffold time so updates to templates ship without binary releases.

## License

See [LICENSE](LICENSE).
