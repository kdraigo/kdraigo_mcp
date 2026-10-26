package tools

import (
	"os"
	"regexp"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/config"
)

// The workspace tools against the real platform, as Claude Code calls them:
// list, run a workspace (a cold machine start if none is up), then report on
// the session the run names. Uses ~/.kdraigo/config.yaml; costs about one
// machine-hour and leaves one backtest session.
//
//	KDRAIGO_MCP_LIVE=1 KDRAIGO_MCP_LIVE_WORKSPACE=<workspace id> go test ./internal/tools/ -run Live -v
func TestWorkspaceLive(t *testing.T) {
	ws := os.Getenv("KDRAIGO_MCP_LIVE_WORKSPACE")
	if os.Getenv("KDRAIGO_MCP_LIVE") == "" || ws == "" {
		t.Skip("KDRAIGO_MCP_LIVE and KDRAIGO_MCP_LIVE_WORKSPACE not set")
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	signer, err := auth.NewSigner(cfg.Auth.KeyID, cfg.Auth.PrivateKey)
	require.NoError(t, err)
	s := server.NewMCPServer("live", "0")
	Register(s, Deps{HTTP: client.NewHTTP(cfg.Endpoint, cfg.BacktesterEndpoint, signer)})

	out, isErr := call(t, s, "workspace_list", nil)
	t.Logf("workspace_list:\n%s", out)
	require.False(t, isErr)

	out, isErr = call(t, s, "workspace_run", map[string]any{"workspace_id": ws})
	t.Logf("workspace_run:\n%s", out)
	require.False(t, isErr)
	require.Contains(t, out, ": succeeded, exit 0")

	session := regexp.MustCompile(`(?m)^([0-9a-f-]{36})\t`).FindStringSubmatch(out)
	require.NotNil(t, session, "the run names its session")
	out, isErr = call(t, s, "get_session_report", map[string]any{"session_id": session[1]})
	t.Logf("get_session_report:\n%s", out)
	require.False(t, isErr)
}
