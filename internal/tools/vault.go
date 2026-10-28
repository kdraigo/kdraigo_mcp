package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/vault"
)

// RegisterVault adds vault_pull and vault_push: a research lab as a folder of
// markdown files on this machine (Obsidian opens it with its graph). Local
// only: the hosted server has no disk of the user's.
func RegisterVault(s *server.MCPServer, d Deps) {
	pull := mcp.NewTool("vault_pull",
		mcp.WithDescription("Write a research lab into a folder on this machine as markdown: Brief.md, Notes/, Lessons/, and read-only copies of "+
			"Experiments/, Handoffs/ and Runs.md, with [[links]] Obsidian follows. The first pull names the lab and needs an empty folder; later "+
			"pulls bring the platform's changes and never overwrite an edit made here (both sides of a conflict are kept)."),
		mcp.WithString("dir", mcp.Required(), mcp.Description("Absolute path of the vault folder")),
		mcp.WithString("lab", mcp.Description("Lab slug or id; needed the first time only")),
		additive(true),
	)
	addTimeout(s, pull, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dir, err := vaultDir(req)
		if err != nil {
			return toolErr(err), nil
		}
		rep, err := vault.Pull(ctx, vault.NewAPI(d.HTTP), dir, req.GetString("lab", ""), time.Now())
		if err != nil {
			return toolErr(fmt.Errorf("vault_pull: %w", err)), nil
		}
		return toolText(rep.String()), nil
	})

	push := mcp.NewTool("vault_push",
		mcp.WithDescription("Send the brief, notes and lessons edited in a vault folder back to the lab, each over the version it was pulled at: a note "+
			"changed on the platform since is refused and stays as it is here (pull, merge, push). A file renamed here renames its note; a new file "+
			"in Notes/ makes a new note. Experiments, handoffs and runs are not sent: use the lab tools for those."),
		mcp.WithString("dir", mcp.Required(), mcp.Description("Absolute path of the vault folder")),
		mcp.WithBoolean("delete", mcp.Description("Also delete on the platform the notes deleted here")),
		mcp.WithBoolean("dry_run", mcp.Description("Only say what a push would send")),
		destructive(false),
	)
	addTimeout(s, push, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dir, err := vaultDir(req)
		if err != nil {
			return toolErr(err), nil
		}
		rep, err := vault.Push(ctx, vault.NewAPI(d.HTTP), dir, vault.PushOptions{Delete: req.GetBool("delete", false), DryRun: req.GetBool("dry_run", false)})
		if err != nil {
			return toolErr(fmt.Errorf("vault_push: %w", err)), nil
		}
		return toolText(rep.String()), nil
	})
}

func vaultDir(req mcp.CallToolRequest) (string, error) {
	dir, err := req.RequireString("dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("dir must be an absolute path, got %q", dir)
	}
	return filepath.Clean(dir), nil
}
