package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/hosted"
	"github.com/kdraigo/kdraigo_mcp/internal/tools"
	"github.com/kdraigo/kdraigo_mcp/skills"
)

// hostedPreface opens the instructions a web client gets: claude.ai and
// ChatGPT do not load skill files, so the workflow travels with the server.
const hostedPreface = `This is the hosted kdraigo MCP server. Strategies are written, built and run in the user's
cloud workspace with the workspace_* tools; there is no local mode here (no scaffold_strategy, no
go run). Results are read with the session tools. What earlier sessions did is kept in the user's
research labs (lab_* and experiment_* tools): open the lab first. The guide below is the
kdraigo-strategy skill.

`

// serve runs the hosted remote MCP server:
//
//	KDRAIGO_MCP_ADDR                (:8090)
//	KDRAIGO_MCP_RESOURCE            https://mcp.kdraigo.com/mcp
//	KDRAIGO_MCP_ISSUER              https://mcp.kdraigo.com
//	KDRAIGO_MCP_USERS_INTERNAL_URL  (http://users_service:4002)
//	KDRAIGO_MCP_INTERNAL_TOKEN      users_service's INTERNAL_MINT_TOKEN
//	KDRAIGO_ENDPOINT                (https://kdraigo.com)
//	KDRAIGO_BACKTESTER_ENDPOINT     (https://api.kdraigo.com)
func serve(args []string) error {
	if len(args) != 1 || args[0] != "--http" {
		return errors.New("usage: kdraigo-mcp serve --http")
	}
	cfg, err := hosted.ConfigFromEnv()
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "kdraigo-mcp ", log.LstdFlags)

	httpClient := client.NewHTTP(envOr("KDRAIGO_ENDPOINT", "https://kdraigo.com"),
		envOr("KDRAIGO_BACKTESTER_ENDPOINT", "https://api.kdraigo.com"), nil)
	s := server.NewMCPServer("kdraigo", resolveVersion(), server.WithInstructions(hostedInstructions()))
	tools.RegisterHosted(s, tools.Deps{HTTP: httpClient})
	// Stateless: each request stands alone and is authorized on its own; no
	// session outlives its request.
	mcpHandler := server.NewStreamableHTTPServer(s, server.WithStateLess(true))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           hosted.Handler(cfg, mcpHandler, hosted.NewIntrospector(cfg), hosted.NewKeyring(cfg), logger),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Printf("%s serving %s on %s (issuer %s)", resolveVersion(), cfg.Resource, cfg.Addr, cfg.Issuer)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// In-flight calls finish first: a workspace_run holds one for up to
	// tools.HostedWaitCap, and one cut by a redeploy reads as a failure.
	shutdown, cancel := context.WithTimeout(context.Background(), tools.HostedWaitCap+30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// hostedInstructions is the preface and the strategy skill without its front
// matter.
func hostedInstructions() string {
	b, err := fs.ReadFile(skills.FS, "kdraigo-strategy.md")
	if err != nil {
		return strings.TrimSpace(hostedPreface)
	}
	body := string(b)
	if strings.HasPrefix(body, "---") {
		if i := strings.Index(body[3:], "\n---"); i >= 0 {
			body = body[3+i+4:]
		}
	}
	return hostedPreface + strings.TrimSpace(body)
}
