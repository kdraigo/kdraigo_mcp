package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kdraigo/kdraigo_mcp/skills"
)

func TestInstallSkillWritesWhereClaudeCodeLooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".claude", "skills")
	// A flat file from a release before v1.1.0.
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "kdraigo-strategy.md"), []byte("old"), 0o644))

	var out strings.Builder
	require.NoError(t, installSkill(nil, &out))

	for _, name := range []string{"kdraigo-strategy", "kdraigo-indicators"} {
		data, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		require.NoError(t, err, name)
		assert.True(t, strings.HasPrefix(string(data), "---\nname: "+name+"\n"), "%s front matter", name)
	}
	assert.Contains(t, out.String(), filepath.Join(root, "kdraigo-strategy.md")+" is an older install")
	assert.NotContains(t, out.String(), "kdraigo-indicators.md is an older install")
}

func TestInstallSkillIntoAProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	var out strings.Builder
	require.NoError(t, installSkill([]string{"--project", project}, &out))
	assert.FileExists(t, filepath.Join(project, ".claude", "skills", "kdraigo-strategy", "SKILL.md"))

	err := installSkill([]string{"--project", filepath.Join(project, "missing")}, &out)
	assert.ErrorContains(t, err, "is not a directory")
}

// Claude Code finds a skill by its folder, which install-skill names after
// the file; the front matter has to agree.
func TestEverySkillIsNamedAfterItsFile(t *testing.T) {
	entries, err := fs.ReadDir(skills.FS, ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		data, err := fs.ReadFile(skills.FS, e.Name())
		require.NoError(t, err)
		name := strings.TrimSuffix(e.Name(), ".md")
		assert.True(t, strings.HasPrefix(string(data), "---\nname: "+name+"\ndescription: "), "%s front matter", e.Name())
	}
}
