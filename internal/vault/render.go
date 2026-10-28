package vault

import (
	"fmt"
	"sort"
	"strings"
	"time"

	knowledge "github.com/kdraigo/workspaces_knowledge"
)

// renderNote is a brief, note, lesson or handoff as its file: what it is in
// the frontmatter, then its markdown. The title is the file's name; it is in
// the frontmatter too only when a file name cannot hold it.
func renderNote(n Note) []byte {
	front := map[string]any{"kind": n.Kind, "slug": n.Slug}
	if len(n.Tags) > 0 {
		front["tags"] = n.Tags
	}
	if n.Kind == kindHandoff {
		front["created"] = n.CreatedAt.UTC().Format(time.RFC3339)
	} else if n.Kind != kindBrief && knowledge.FileName(n.Title) != n.Title {
		front["title"] = n.Title
	}
	return mustRender(knowledge.Doc{Front: front, Body: n.Body})
}

func mustRender(d knowledge.Doc) []byte {
	b, err := d.Render()
	if err != nil {
		// Maps of strings and lists always encode; a body never fails.
		panic(err)
	}
	return b
}

const readOnly = "> A read-only copy from kdraigo: edits here are not sent back."

// renderExperiment is an experiment's card: its fields in the frontmatter
// (Obsidian shows them as properties), the hypothesis, why, the verdict and
// the runs filed under it.
func renderExperiment(e Experiment, titles map[string]string, runs []Run) []byte {
	front := map[string]any{"kind": kindExperiment, "slug": e.Slug, "status": e.Status}
	if e.RegisteredAt != nil {
		front["registered"] = day(*e.RegisteredAt)
	}
	if e.DecidedAt != nil {
		front["decided"] = day(*e.DecidedAt)
	}
	if e.RuleHash != "" {
		front["rule_hash"] = e.RuleHash
	}
	if t := titles[e.DerivedFrom]; t != "" {
		front["derived_from"] = "[[" + knowledge.FileName(t) + "]]"
	}
	if len(e.Tags) > 0 {
		front["tags"] = e.Tags
	}
	if len(e.Protocol) > 0 {
		front["protocol"] = e.Protocol
	}
	if len(e.Scope) > 0 {
		front["scope"] = e.Scope
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s Change it with the experiment tools (experiment_save, experiment_register, experiment_decide).\n\n", e.Title, readOnly)
	if e.Verdict != "" {
		when := ""
		if e.DecidedAt != nil {
			when = ", " + day(*e.DecidedAt)
		}
		fmt.Fprintf(&b, "## Verdict\n**%s**%s. %s\n\n", e.Status, when, strings.TrimSpace(e.Verdict))
	}
	fmt.Fprintf(&b, "## Hypothesis\n%s\n\n", strings.TrimSpace(e.Hypothesis))
	if strings.TrimSpace(e.Mechanism) != "" {
		fmt.Fprintf(&b, "## Why it should work\n%s\n\n", strings.TrimSpace(e.Mechanism))
	}
	var own []Run
	for _, r := range runs {
		if r.ExperimentID == e.ID {
			own = append(own, r)
		}
	}
	fmt.Fprintf(&b, "## Runs (%d)\n", len(own))
	if len(own) == 0 {
		b.WriteString("None filed under it.\n")
	} else {
		writeRuns(&b, own, nil)
	}
	return mustRender(knowledge.Doc{Front: front, Body: b.String()})
}

// renderRuns is every run the lab recorded, newest first.
func renderRuns(runs []Run, titles map[string]string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Runs\n\n%s The runs of the lab's workspaces, newest first, with the experiment each is filed under.\n\n", readOnly)
	if len(runs) == 0 {
		b.WriteString("None yet: runs of a workspace in this lab are recorded as they end.\n")
	} else {
		writeRuns(&b, runs, titles)
	}
	return mustRender(knowledge.Doc{Front: map[string]any{"kind": kindGenerated}, Body: b.String()})
}

// writeRuns is a markdown table of runs; titles adds their experiment.
func writeRuns(b *strings.Builder, runs []Run, titles map[string]string) {
	b.WriteString("| when | status |")
	if titles != nil {
		b.WriteString(" experiment |")
	}
	b.WriteString(" pairs | window | settings | return | max DD | trades | PF | session |\n|---|---|")
	if titles != nil {
		b.WriteString("---|")
	}
	b.WriteString("---|---|---|---:|---:|---:|---:|---|\n")
	for _, r := range runs {
		fmt.Fprintf(b, "| %s | %s |", r.CreatedAt.UTC().Format("2006-01-02 15:04"), r.Status)
		if titles != nil {
			exp := "unfiled"
			if t := titles[r.ExperimentID]; t != "" {
				exp = "[[" + knowledge.FileName(t) + "]]"
			}
			fmt.Fprintf(b, " %s |", exp)
		}
		window := "—"
		if r.WindowStart != nil && r.WindowEnd != nil {
			window = day(*r.WindowStart) + " → " + day(*r.WindowEnd)
		}
		session := "—"
		if len(r.SessionIDs) > 0 {
			session = "`" + r.SessionIDs[0] + "`"
		}
		m := r.Metrics
		fmt.Fprintf(b, " %s %s | %s | %s | %s | %s | %s | %s | %s |\n", strings.Join(r.Pairs, ", "), r.Timeframe, window,
			cell(params(r.Params)), pct(m.ReturnPct, true), pct(m.MaxDrawdownPct, false), count(m.Trades), num(m.ProfitFactor), session)
	}
}

// renderReadme says what the vault is and what is where.
func renderReadme(lab Lab, exps []Experiment, notes int, now time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nA kdraigo research lab (%s), pulled %s UTC. %s\n\n", lab.Name, orNone(lab.Market), now.UTC().Format("2006-01-02 15:04"), readOnly)
	b.WriteString(`Edit [[Brief]], the notes in Notes/ and your lessons in Lessons/ here, then send them back with
` + "`kdraigo-mcp vault push`" + ` (or ask your AI to push the vault). A note changed on the platform since your
pull is refused, never overwritten: pull again, and merge what lands in .kdraigo/conflicts/.
Experiments, handoffs and runs are copies: your AI records them through the platform.

`)
	open, decided := []Experiment{}, []Experiment{}
	for _, e := range exps {
		if e.DecidedAt != nil {
			decided = append(decided, e)
		} else {
			open = append(open, e)
		}
	}
	sort.SliceStable(decided, func(i, j int) bool { return decided[i].DecidedAt.After(*decided[j].DecidedAt) })
	list := func(title string, es []Experiment) {
		if len(es) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n", title)
		for _, e := range es {
			fmt.Fprintf(&b, "- [[%s]]: %s\n", knowledge.FileName(e.Title), e.Status)
		}
		b.WriteString("\n")
	}
	list("Under test", open)
	list("Decided (null and failed are dead ends)", decided)
	fmt.Fprintf(&b, "%d note(s) in Notes/. [[Runs]] lists every recorded run.\n", notes)
	return mustRender(knowledge.Doc{Front: map[string]any{"kind": kindGenerated}, Body: b.String()})
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

func orNone(s string) string {
	if s == "" {
		return "no market set"
	}
	return s
}

func params(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + p[k]
	}
	return strings.Join(parts, " ")
}

// cell keeps text inside a table cell.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", "\\|")
}

func pct(v *float64, signed bool) string {
	if v == nil {
		return "—"
	}
	if signed && *v > 0 {
		return fmt.Sprintf("+%.1f%%", *v)
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func num(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

func count(v *int) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}
