package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/config"
	"github.com/kdraigo/kdraigo_mcp/internal/tools"
)

// version may be stamped at build time via -ldflags "-X main.version=<tag>" (see
// Makefile). Left unset it resolves from the module version Go embeds in the
// binary, so `go install github.com/kdraigo/kdraigo_mcp/...@latest` reports the
// real release tag automatically — no hardcoded constant to drift (D7).
var version = ""

// resolveVersion prefers an explicit ldflags stamp, else the module version Go
// records at install time (e.g. "v1.0.4"), else a dev fallback.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install-skill":
			if err := installSkill(); err != nil {
				fmt.Fprintln(os.Stderr, "install-skill:", err)
				os.Exit(1)
			}
			return
		case "version", "-v", "--version":
			fmt.Println("kdraigo-mcp", resolveVersion())
			return
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	signer, err := auth.NewSigner(cfg.Auth.KeyID, cfg.Auth.PrivateKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "signer:", err)
		os.Exit(1)
	}
	httpClient := client.NewHTTP(cfg.Endpoint, cfg.BacktesterEndpoint, signer)
	deps := tools.Deps{HTTP: httpClient}

	s := server.NewMCPServer("kdraigo", resolveVersion())
	tools.RegisterDocs(s)
	tools.RegisterScaffold(s)
	tools.RegisterSessions(s, deps)
	tools.RegisterAnalytics(s, deps)
	tools.RegisterCandles(s, deps)
	tools.RegisterBacktest(s, deps)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, "stdio:", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`kdraigo-mcp — MCP server for the kdraigo trading platform.

Usage:
  kdraigo-mcp                  Start the stdio MCP server (default; called by Claude Code).
  kdraigo-mcp install-skill    Copy the kdraigo-strategy skill into ~/.claude/skills/.
  kdraigo-mcp version          Print version.
  kdraigo-mcp help             Show this help.

Configuration:
  Reads ~/.kdraigo/config.yaml (override with KDRAIGO_CONFIG env var). Required:
    auth:
      key_id: <uuid>
      private_key: <ed25519-hex>
    endpoint: https://kdraigo.com              # optional; gateway for data/analytics
    backtester_endpoint: https://api.kdraigo.com  # optional; backtester_engine host`)
}
