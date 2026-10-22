package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdraigo/kdraigo_mcp/skills"
)

// installSkill writes the embedded skills where Claude Code looks for them:
// ~/.claude/skills/<name>/SKILL.md, or <dir>/.claude/skills/<name>/SKILL.md
// with --project <dir>.
func installSkill(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("install-skill", flag.ContinueOnError)
	flags.SetOutput(out)
	project := flags.String("project", "", "install into `dir`/.claude/skills instead of ~/.claude/skills")
	if err := flags.Parse(args); err != nil {
		return err
	}

	var root string
	if *project != "" {
		dir, err := filepath.Abs(*project)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("--project %s is not a directory", dir)
		}
		root = filepath.Join(dir, ".claude", "skills")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		root = filepath.Join(home, ".claude", "skills")
	}
	return writeSkills(skills.FS, root, out)
}

func writeSkills(src fs.FS, root string, out io.Writer) error {
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		data, err := fs.ReadFile(src, e.Name())
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		dst := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		fmt.Fprintln(out, "installed", dst)

		// Releases before v1.1.0 wrote <name>.md straight into the skills
		// folder, which Claude Code does not load.
		if old := filepath.Join(root, e.Name()); fileExists(old) {
			fmt.Fprintf(out, "  %s is an older install that Claude Code never loaded; remove it\n", old)
		}
	}
	fmt.Fprintln(out, "Restart Claude Code to load them.")
	return nil
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
