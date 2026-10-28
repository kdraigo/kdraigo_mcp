package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledge "github.com/kdraigo/workspaces_knowledge"
)

// fakeLab is research_service for one lab, in memory, with its version rules.
type fakeLab struct {
	notes   map[string]*Note // the lab's, by slug
	lessons map[string]*Note
	exps    []Experiment
	runs    []Run
	clock   time.Time
	writes  int
}

func newFake() *fakeLab {
	t0 := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	f := &fakeLab{notes: map[string]*Note{}, lessons: map[string]*Note{}, clock: t0}
	f.put(f.notes, &Note{Kind: kindBrief, Slug: "brief", Title: "Brief", Body: "# Lab\n\n## Goal\nBeat [[EMA cross]].\n"})
	f.put(f.notes, &Note{Kind: kindNote, Slug: "funding-drag", Title: "Funding drag", Body: "Shorts pay. See [[EMA cross]].\n", Tags: []string{"funding"}})
	f.put(f.notes, &Note{Kind: kindNote, Slug: "rsi-14-30", Title: "RSI 14 / 30", Body: "Thresholds.\n"})
	f.put(f.notes, &Note{Kind: kindHandoff, Slug: "handoff-1", Title: "Handoff", Body: "Next: hold-out.\n"})
	f.put(f.lessons, &Note{Kind: kindLesson, Slug: "fees-kill-1m", Title: "Fees kill 1m", Body: "Below 15m fees eat it.\n"})
	reg := t0.Add(-48 * time.Hour)
	dec := t0.Add(-24 * time.Hour)
	ret, dd, pf, n := 12.4, 6.1, 1.7, 41
	f.exps = []Experiment{
		{ID: "E1", Slug: "ema-cross", Title: "EMA cross", Hypothesis: "Trends persist.", Status: "registered", RegisteredAt: &reg, Version: 2,
			Protocol: map[string]any{"pass_rule": "PF > 1.2", "test": map[string]any{"from": "2025-01-01", "to": "2025-07-01"}}},
		{ID: "E2", Slug: "rsi-exit", Title: "RSI exit", Hypothesis: "Exit on RSI.", Status: "null", Verdict: "Cut the trends.", DecidedAt: &dec, DerivedFrom: "E1", Version: 3},
	}
	run := Run{ID: "R1", ExperimentID: "E1", SessionIDs: []string{"s-1"}, Pairs: []string{"BTC/USDT"}, Timeframe: "4h", Status: "succeeded",
		Params: map[string]string{"EMA_FAST": "20"}, CreatedAt: t0.Add(-time.Hour)}
	run.Metrics.ReturnPct, run.Metrics.MaxDrawdownPct, run.Metrics.ProfitFactor, run.Metrics.Trades = &ret, &dd, &pf, &n
	f.runs = []Run{run}
	return f
}

func (f *fakeLab) put(m map[string]*Note, n *Note) {
	f.clock = f.clock.Add(time.Minute)
	n.ID = "id-" + n.Slug
	n.Version++
	n.CreatedAt, n.UpdatedAt = f.clock, f.clock
	m[n.Slug] = n
}

func reply(v, out any) error {
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func (f *fakeLab) Do(_ context.Context, method, p string, q url.Values, body, out any) error {
	var in struct {
		Kind  string   `json:"kind"`
		Title string   `json:"title"`
		Body  string   `json:"body"`
		Tags  []string `json:"tags"`
		Base  *int64   `json:"base_version"`
	}
	if body != nil {
		b, _ := json.Marshal(body)
		_ = json.Unmarshal(b, &in)
	}
	collection := func(prefix string, m map[string]*Note) (bool, error) {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			return false, nil
		}
		slug := strings.TrimPrefix(rest, "/")
		switch {
		case method == http.MethodGet && slug == "":
			list := []Note{}
			for _, n := range m {
				c := *n
				c.Body = ""
				list = append(list, c)
			}
			return true, reply(list, out)
		case method == http.MethodGet:
			n, ok := m[slug]
			if !ok {
				return true, &StatusError{Code: 404, Msg: "not found"}
			}
			return true, reply(n, out)
		case method == http.MethodPost:
			slug = knowledge.Slug(in.Title)
			fallthrough
		case method == http.MethodPut:
			n, exists := m[slug]
			switch {
			case exists && in.Base != nil && *in.Base != n.Version, !exists && in.Base != nil && *in.Base != 0:
				cur := int64(0)
				if exists {
					cur = n.Version
				}
				return true, &StatusError{Code: 409, Msg: "conflict", Version: cur}
			case !exists:
				n = &Note{Kind: in.Kind, Slug: slug}
			}
			if n.Kind == "" || prefix == "/lessons" {
				n.Kind = map[bool]string{true: kindLesson, false: in.Kind}[prefix == "/lessons"]
			}
			n.Title, n.Body, n.Tags = in.Title, in.Body, in.Tags
			created := n.CreatedAt
			f.put(m, n)
			if !created.IsZero() {
				n.CreatedAt = created
			}
			f.writes++
			return true, reply(n, out)
		case method == http.MethodDelete:
			n, ok := m[slug]
			if !ok {
				return true, &StatusError{Code: 404, Msg: "not found"}
			}
			if v, _ := strconv.ParseInt(q.Get("base_version"), 10, 64); v != n.Version {
				return true, &StatusError{Code: 409, Msg: "conflict", Version: n.Version}
			}
			delete(m, slug)
			f.writes++
			return true, nil
		}
		return true, fmt.Errorf("fake: %s %s", method, p)
	}
	switch p {
	case "/labs/lab", "/labs/L1":
		return reply(Lab{ID: "L1", Slug: "lab", Name: "BTC trend", Market: "futures"}, out)
	case "/labs/L1/experiments":
		return reply(f.exps, out)
	case "/labs/L1/runs":
		return reply(f.runs, out)
	}
	if ok, err := collection("/labs/L1/notes", f.notes); ok {
		return err
	}
	if ok, err := collection("/lessons", f.lessons); ok {
		return err
	}
	return fmt.Errorf("fake: no route %s %s", method, p)
}

func read(t *testing.T, dir, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	require.NoError(t, err)
	return string(b)
}

func edit(t *testing.T, dir, p, text string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, filepath.FromSlash(p))), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(text), 0o644))
}

func exists(dir, p string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
	return err == nil
}

var now = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

func pull(t *testing.T, api API, dir string) *Report {
	t.Helper()
	rep, err := Pull(context.Background(), api, dir, "", now)
	require.NoError(t, err)
	return rep
}

func push(t *testing.T, api API, dir string, opts PushOptions) *Report {
	t.Helper()
	rep, err := Push(context.Background(), api, dir, opts)
	require.NoError(t, err)
	return rep
}

func TestPull_WritesTheLabAsObsidianReadsIt(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".obsidian"), 0o755)) // Obsidian's own: allowed
	_, err := Pull(context.Background(), f, dir, "", now)
	assert.ErrorContains(t, err, "name the lab")
	_, err = Pull(context.Background(), f, dir, "lab", now)
	require.NoError(t, err)

	brief := read(t, dir, BriefFile)
	assert.Contains(t, brief, "kind: brief")
	assert.Contains(t, brief, "Beat [[EMA cross]]")
	note := read(t, dir, "Notes/Funding drag.md")
	assert.True(t, strings.HasPrefix(note, "---\nkind: note\nslug: funding-drag\ntags:\n  - funding\n---\nShorts pay."), note)
	odd := read(t, dir, "Notes/RSI 14 30.md")
	assert.Contains(t, odd, "title: RSI 14 / 30", "a title no file name can spell is kept")
	assert.Contains(t, read(t, dir, "Lessons/Fees kill 1m.md"), "Below 15m")
	assert.True(t, exists(dir, "Handoffs/2026-10-28 10-04.md"))

	exp := read(t, dir, "Experiments/RSI exit.md")
	assert.Contains(t, exp, "derived_from: '[[EMA cross]]'")
	assert.Contains(t, exp, "## Verdict\n**null**, 2026-10-27. Cut the trends.")
	ema := read(t, dir, "Experiments/EMA cross.md")
	assert.Contains(t, ema, "pass_rule: PF > 1.2")
	assert.Contains(t, ema, "| +12.4% | 6.1% | 41 | 1.70 | `s-1` |")
	assert.Contains(t, read(t, dir, RunsFile), "| [[EMA cross]] |")
	readme := read(t, dir, ReadmeFile)
	assert.Contains(t, readme, "# BTC trend")
	assert.Contains(t, readme, "- [[EMA cross]]: registered")

	// Another lab into the same folder is refused; a non-empty folder too.
	_, err = Pull(context.Background(), f, dir, "spot", now)
	assert.ErrorContains(t, err, "holds the lab")
	other := t.TempDir()
	edit(t, other, "mine.md", "x")
	_, err = Pull(context.Background(), f, other, "lab", now)
	assert.ErrorContains(t, err, "holds other files")

	// A second pull with nothing changed writes nothing.
	assert.Empty(t, pull(t, f, dir).Done)
}

func TestRoundTrip(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	_, err := Pull(context.Background(), f, dir, "lab", now)
	require.NoError(t, err)

	edit(t, dir, "Notes/Funding drag.md", "---\nkind: note\nslug: funding-drag\ntags: [funding, carry]\n---\nShorts pay more.\n")
	edit(t, dir, BriefFile, "---\nkind: brief\n---\n# Lab\nNew goal.\n")
	edit(t, dir, "Lessons/Fees kill 1m.md", "Below 15m fees eat it all.\n")
	rep := push(t, f, dir, PushOptions{})
	assert.Len(t, rep.Done, 3, "%v", rep)
	n := f.notes["funding-drag"]
	assert.Equal(t, "Shorts pay more.\n", n.Body)
	assert.Equal(t, []string{"funding", "carry"}, n.Tags)
	assert.Equal(t, int64(2), n.Version)
	assert.Equal(t, "# Lab\nNew goal.\n", f.notes["brief"].Body)
	assert.Equal(t, "Below 15m fees eat it all.\n", f.lessons["fees-kill-1m"].Body)

	writes := f.writes
	assert.Empty(t, push(t, f, dir, PushOptions{}).Done, "nothing new to send")
	assert.Equal(t, writes, f.writes)
	assert.Empty(t, pull(t, f, dir).Kept, "what was pushed is not a local edit any more")
	assert.Contains(t, read(t, dir, "Notes/Funding drag.md"), "Shorts pay more.")

	// The platform moves on (the AI wrote): a pull brings it.
	f.notes["funding-drag"].Body = "AI's view.\n"
	f.put(f.notes, f.notes["funding-drag"])
	rep = pull(t, f, dir)
	assert.Contains(t, rep.Done, "updated Notes/Funding drag.md")
	assert.Contains(t, read(t, dir, "Notes/Funding drag.md"), "AI's view.")
}

func TestConflict_BothSidesKept(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	_, err := Pull(context.Background(), f, dir, "lab", now)
	require.NoError(t, err)
	f.notes["funding-drag"].Body = "Theirs.\n"
	f.put(f.notes, f.notes["funding-drag"]) // v2
	edit(t, dir, "Notes/Funding drag.md", "---\nslug: funding-drag\n---\nMine.\n")

	rep := push(t, f, dir, PushOptions{})
	require.Len(t, rep.Kept, 1)
	assert.Contains(t, rep.Kept[0], "changed on the platform since your pull (now v2)")
	assert.Equal(t, "Theirs.\n", f.notes["funding-drag"].Body, "not overwritten")

	rep = pull(t, f, dir)
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "changed here and on the platform (now v2)")
	assert.Contains(t, read(t, dir, "Notes/Funding drag.md"), "Mine.", "the local edit stays")
	assert.Contains(t, read(t, dir, ".kdraigo/conflicts/Notes/Funding drag.md"), "Theirs.")

	rep = push(t, f, dir, PushOptions{})
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "resolve the conflict first")

	edit(t, dir, "Notes/Funding drag.md", "---\nslug: funding-drag\n---\nMine and theirs.\n")
	require.NoError(t, os.Remove(filepath.Join(dir, ".kdraigo/conflicts/Notes/Funding drag.md")))
	rep = push(t, f, dir, PushOptions{})
	assert.Empty(t, rep.Kept, "%v", rep)
	assert.Equal(t, "Mine and theirs.\n", f.notes["funding-drag"].Body)
	assert.Equal(t, int64(3), f.notes["funding-drag"].Version)
}

func TestRenameAndNewNotes(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	_, err := Pull(context.Background(), f, dir, "lab", now)
	require.NoError(t, err)
	// Obsidian renames the file (and the links to it).
	require.NoError(t, os.Rename(filepath.Join(dir, "Notes/Funding drag.md"), filepath.Join(dir, "Notes/Funding cost.md")))
	edit(t, dir, "Notes/Ideas/New idea.md", "A plain file, no frontmatter. See [[Funding cost]].\n")
	edit(t, dir, "Lessons/Use 4h.md", "Lower timeframes lose to fees.\n")
	edit(t, dir, "scratch.md", "not synced\n")

	rep := push(t, f, dir, PushOptions{})
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "scratch.md is outside Notes/")
	n := f.notes["funding-drag"]
	assert.Equal(t, "Funding cost", n.Title, "renamed, same note")
	require.Contains(t, f.notes, "new-idea")
	assert.Equal(t, "New idea", f.notes["new-idea"].Title)
	require.Contains(t, f.lessons, "use-4h")

	rep = pull(t, f, dir)
	assert.Empty(t, rep.Kept, "%v", rep)
	assert.True(t, exists(dir, "Notes/Funding cost.md"))
	assert.False(t, exists(dir, "Notes/Funding drag.md"))
	assert.True(t, exists(dir, "Notes/New idea.md"), "the platform has no folders: a pull puts it in Notes/")
	assert.Empty(t, push(t, f, dir, PushOptions{}).Done)

	// A name taken on the platform is refused, not merged.
	edit(t, dir, "Notes/Funding cost copy.md", "---\ntitle: RSI 14 / 30\n---\nx\n")
	edit(t, dir, "Notes/RSI 14 30 new.md", "x\n")
	f.put(f.notes, &Note{Kind: kindNote, Slug: "rsi-14-30-new", Title: "RSI 14 30 new", Body: "theirs"})
	rep = push(t, f, dir, PushOptions{})
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "a note of that name is on the platform already")
}

func TestDeletes(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	_, err := Pull(context.Background(), f, dir, "lab", now)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, "Notes/RSI 14 30.md")))
	require.NoError(t, os.Remove(filepath.Join(dir, BriefFile)))

	rep := push(t, f, dir, PushOptions{})
	kept := strings.Join(rep.Kept, "\n")
	assert.Contains(t, kept, "Notes/RSI 14 30.md was deleted here: push with delete")
	assert.Contains(t, kept, "the brief cannot be deleted")
	assert.Contains(t, f.notes, "rsi-14-30")

	rep = push(t, f, dir, PushOptions{Delete: true, DryRun: true})
	assert.Contains(t, rep.Done, "would have deleted Notes/RSI 14 30.md on the platform")
	assert.Contains(t, f.notes, "rsi-14-30", "a dry run sends nothing")

	push(t, f, dir, PushOptions{Delete: true})
	assert.NotContains(t, f.notes, "rsi-14-30")
	pull(t, f, dir)
	assert.True(t, exists(dir, BriefFile), "the brief comes back")
	assert.False(t, exists(dir, "Notes/RSI 14 30.md"))
}

func TestPlatformDeletesAndReadOnlyCopies(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	_, err := Pull(context.Background(), f, dir, "lab", now)
	require.NoError(t, err)

	delete(f.notes, "rsi-14-30")
	edit(t, dir, "Experiments/EMA cross.md", "my edit\n")
	rep := push(t, f, dir, PushOptions{})
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "Experiments/EMA cross.md is a read-only copy")

	rep = pull(t, f, dir)
	assert.False(t, exists(dir, "Notes/RSI 14 30.md"), "gone from the platform, unchanged here: removed")
	assert.Contains(t, read(t, dir, "Experiments/EMA cross.md"), "## Hypothesis", "the copy is the platform's again")
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "your edit was replaced")

	// Edited here, deleted there: the edit stays and goes back as a new note.
	edit(t, dir, "Notes/Funding drag.md", "Still mine.\n")
	delete(f.notes, "funding-drag")
	rep = pull(t, f, dir)
	assert.Contains(t, strings.Join(rep.Kept, "\n"), "deleted on the platform; your edits stay")
	push(t, f, dir, PushOptions{})
	require.Contains(t, f.notes, "funding-drag")
	assert.Equal(t, "Still mine.\n", f.notes["funding-drag"].Body)
}
