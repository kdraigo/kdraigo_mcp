package tools

import (
	"os"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// ReplayEnv set to 1 adds the replay tools, create_backtest_session and
// run_backtest_stream. They are off by default because they look like the way
// to backtest and are not: they step candles through a session without running
// any strategy code (a strategy runs as its own program, `go run .`, through
// dev_sdk), and a replay left parked holds one of the account's session slots
// for up to an hour.
const ReplayEnv = "KDRAIGO_MCP_ENABLE_REPLAY"

// HostedWaitCap is the longest a hosted tool call waits for a build or run;
// workspace_run_status follows it from there.
const HostedWaitCap = 60 * time.Second

// Register adds every tool.
// RegisterHosted adds what the hosted server (kdraigo-mcp serve) offers: the
// same tools less those that touch the caller's own disk (scaffold_strategy,
// vault_pull, vault_push) and the candle replay.
func RegisterHosted(s *server.MCPServer, d Deps) {
	if d.WaitCap == 0 {
		d.WaitCap = HostedWaitCap
	}
	RegisterDocs(s)
	RegisterSessions(s, d)
	RegisterAnalytics(s, d)
	RegisterCandles(s, d)
	RegisterWorkspace(s, d)
	RegisterResearch(s, d)
	RegisterLibrary(s, d)
	RegisterBoards(s, d)
}

func Register(s *server.MCPServer, d Deps) {
	RegisterDocs(s)
	RegisterScaffold(s)
	RegisterVault(s, d)
	RegisterSessions(s, d)
	RegisterAnalytics(s, d)
	RegisterCandles(s, d)
	RegisterWorkspace(s, d)
	RegisterResearch(s, d)
	RegisterLibrary(s, d)
	RegisterBoards(s, d)
	if os.Getenv(ReplayEnv) == "1" {
		RegisterBacktest(s, d)
	}
}
