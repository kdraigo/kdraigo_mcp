package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const exampleStrategyRepo = "https://github.com/kdraigo/example_strategy.git"

var nameSafe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// RegisterScaffold adds scaffold_strategy. Clones example_strategy and isolates the chosen template.
func RegisterScaffold(s *server.MCPServer) {
	tool := mcp.NewTool("scaffold_strategy",
		mcp.WithDescription("Clone a starter strategy from github.com/kdraigo/example_strategy into a target directory. The resulting Go program uses dev_sdk and reads KDRAIGO_KEY_ID + KDRAIGO_PRIVATE_KEY from env."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Project name; becomes the destination dir basename. Letters, digits, _ and -.")),
		mcp.WithString("template", mcp.Description("Template to use; defaults to 'basic'"), mcp.DefaultString("basic"), mcp.Enum("basic", "with-indicators", "with-risk-manager")),
		mcp.WithString("dir", mcp.Required(), mcp.Description("Absolute path to the parent directory where the project will be created")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return toolErr(err), nil
		}
		if !nameSafe.MatchString(name) {
			return toolErr(fmt.Errorf("name %q must match %s", name, nameSafe.String())), nil
		}
		template := req.GetString("template", "basic")
		dir, err := req.RequireString("dir")
		if err != nil {
			return toolErr(err), nil
		}
		if !filepath.IsAbs(dir) {
			return toolErr(fmt.Errorf("dir must be an absolute path, got %q", dir)), nil
		}

		dest := filepath.Join(dir, name)
		if _, err := os.Stat(dest); err == nil {
			return toolErr(fmt.Errorf("destination %s already exists", dest)), nil
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return toolErr(fmt.Errorf("mkdir %s: %w", dir, err)), nil
		}

		tmp, err := os.MkdirTemp("", "kdraigo-scaffold-*")
		if err != nil {
			return toolErr(fmt.Errorf("mkdtemp: %w", err)), nil
		}
		defer os.RemoveAll(tmp)

		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", exampleStrategyRepo, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			return toolErr(fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(string(out)))), nil
		}

		src := filepath.Join(tmp, template)
		if _, err := os.Stat(src); err != nil {
			return toolErr(fmt.Errorf("template %q not present in example_strategy repo", template)), nil
		}
		if err := copyDir(src, dest); err != nil {
			return toolErr(fmt.Errorf("copy template: %w", err)), nil
		}

		return toolText(fmt.Sprintf(`{"scaffolded_at":%q,"template":%q,"next_steps":["cd %s","go mod tidy","export KDRAIGO_KEY_ID=...","export KDRAIGO_PRIVATE_KEY=...","go run ."]}`, dest, template, dest)), nil
	})
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
