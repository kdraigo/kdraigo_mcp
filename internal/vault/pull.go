package vault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	knowledge "github.com/kdraigo/workspaces_knowledge"
)

// planned is a file the platform's side of the lab should have.
type planned struct {
	key     string // kind:slug, the file's identity across renames
	path    string // slash-separated, relative to the vault
	kind    string
	slug    string
	version int64
	// body renders the file; it may ask the platform for a note's text.
	body func(ctx context.Context) ([]byte, error)
}

func keyOf(kind, slug string) string { return kind + ":" + slug }

// foreign are entries a new vault's folder may already hold: Obsidian's and
// git's own.
var foreign = map[string]bool{".obsidian": true, ".git": true, ".DS_Store": true, stateDir: true}

// Pull writes the lab into dir: the first time into an empty folder, then
// over its own files. labRef names the lab the first time; later the vault
// knows it. A file edited here is never overwritten: when the platform
// changed it too, the platform's text goes to .kdraigo/conflicts.
func Pull(ctx context.Context, api API, dir, labRef string, now time.Time) (*Report, error) {
	st, err := loadState(dir)
	if err != nil {
		return nil, err
	}
	switch {
	case st == nil && labRef == "":
		return nil, errors.New("this folder is not a vault yet: name the lab to pull into it")
	case st == nil:
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if !foreign[e.Name()] {
				return nil, fmt.Errorf("%s holds other files (%s): pull a lab into an empty or new folder", dir, e.Name())
			}
		}
		st = &State{Files: map[string]*File{}}
	case labRef != "" && labRef != st.Lab.Slug && labRef != st.Lab.ID:
		return nil, fmt.Errorf("this folder holds the lab %q (%s): pull %q into another folder", st.Lab.Name, st.Lab.Slug, labRef)
	}
	if labRef == "" {
		labRef = st.Lab.ID
	}

	var lab Lab
	if err := api.Do(ctx, http.MethodGet, "/labs/"+url.PathEscape(labRef), nil, nil, &lab); err != nil {
		return nil, fmt.Errorf("the lab: %w", err)
	}
	var notes, lessons []Note
	var exps []Experiment
	var runs []Run
	labPath := "/labs/" + url.PathEscape(lab.ID)
	for _, get := range []struct {
		path string
		q    url.Values
		out  any
	}{
		{labPath + "/notes", nil, &notes},
		{"/lessons", nil, &lessons},
		{labPath + "/experiments", url.Values{"limit": {"200"}}, &exps},
		{labPath + "/runs", url.Values{"limit": {"200"}}, &runs},
	} {
		if err := api.Do(ctx, http.MethodGet, get.path, get.q, nil, get.out); err != nil {
			return nil, fmt.Errorf("%s: %w", get.path, err)
		}
	}
	st.Lab = lab

	plan := planFiles(api, lab, notes, lessons, exps, runs, now)
	rep := &Report{Lab: lab}
	byKey := map[string]string{}
	for p, f := range st.Files {
		byKey[keyOf(f.Kind, f.Slug)] = p
	}
	// Texts are read four at a time, only where the version moved or the file is new.
	if err := prefetch(ctx, plan, st, byKey); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	for _, f := range plan {
		seen[f.key] = true
		if err := apply(ctx, dir, st, byKey, f, rep); err != nil {
			return nil, err
		}
	}
	// What the platform no longer has.
	for p, f := range st.Files {
		if seen[keyOf(f.Kind, f.Slug)] {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		sum, err := fileHash(full)
		if err != nil {
			return nil, err
		}
		switch {
		case sum == "" || sum == f.SHA || !editable(f.Kind):
			if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			delete(st.Files, p)
			rep.did("removed %s (gone from the platform)", p)
		default:
			// Edited here, deleted there: the edit stays, and is sent as a new note.
			delete(st.Files, p)
			rep.kept("%s was deleted on the platform; your edits stay here, and a push sends it as a new note", p)
		}
	}
	st.PulledAt = now
	return rep, st.save(dir)
}

// prefetch reads the text of every note whose file must be written.
func prefetch(ctx context.Context, plan []*planned, st *State, byKey map[string]string) error {
	var todo []*planned
	for _, f := range plan {
		if !editable(f.kind) && f.kind != kindHandoff {
			continue
		}
		if p, ok := byKey[f.key]; !ok || st.Files[p].Version != f.version || p != f.path {
			todo = append(todo, f)
		}
	}
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, f := range todo {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := f.body(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && first == nil {
				first = err
			}
			if err == nil {
				f.body = func(context.Context) ([]byte, error) { return b, nil }
			}
		})
	}
	wg.Wait()
	return first
}

// apply brings one file of the platform's into the vault.
func apply(ctx context.Context, dir string, st *State, byKey map[string]string, f *planned, rep *Report) error {
	full := filepath.Join(dir, filepath.FromSlash(f.path))
	prevPath, known := byKey[f.key]
	if !known {
		data, err := f.body(ctx)
		if err != nil {
			return err
		}
		sum, err := fileHash(full)
		if err != nil {
			return err
		}
		if sum != "" && sum != hash(data) && editable(f.kind) {
			// A file of the same name made here: both stay.
			if err := write(filepath.Join(dir, conflictsDir, filepath.FromSlash(f.path)), data); err != nil {
				return err
			}
			st.Files[f.path] = &File{Kind: f.kind, Slug: f.slug, Version: f.version, SHA: sum, Remote: f.version}
			rep.kept("%s exists here and on the platform: the platform's text is in %s/%s; merge, delete that copy, push", f.path, conflictsDir, f.path)
			return nil
		}
		if err := write(full, data); err != nil {
			return err
		}
		st.Files[f.path] = &File{Kind: f.kind, Slug: f.slug, Version: f.version, SHA: hash(data)}
		rep.did("wrote %s", f.path)
		return nil
	}

	prev := st.Files[prevPath]
	prevFull := filepath.Join(dir, filepath.FromSlash(prevPath))
	sum, err := fileHash(prevFull)
	if err != nil {
		return err
	}
	localEdit := sum != prev.SHA
	moved := f.version != prev.Version || prevPath != f.path || !editable(f.kind)

	switch {
	case f.kind == kindHandoff && !localEdit && f.version == prev.Version && prevPath == f.path:
		// A handoff never changes: its text is not read again.
		return nil
	case !editable(f.kind):
		data, err := f.body(ctx)
		if err != nil {
			return err
		}
		if sum == hash(data) && prevPath == f.path {
			return nil
		}
		if localEdit && sum != "" {
			rep.kept("%s is a read-only copy: your edit was replaced (change it on the platform)", prevPath)
		}
		return replace(dir, st, prevPath, f, data, rep)
	case sum == "" && f.version == prev.Version && f.kind != kindBrief:
		rep.kept("%s was deleted here: a push with delete deletes it on the platform", prevPath)
		return nil
	case sum == "" && f.kind == kindBrief:
		// The brief cannot be deleted: it comes back.
		data, err := f.body(ctx)
		if err != nil {
			return err
		}
		return replace(dir, st, prevPath, f, data, rep)
	case sum == "":
		data, err := f.body(ctx)
		if err != nil {
			return err
		}
		rep.kept("%s was deleted here but changed on the platform since: restored", f.path)
		return replace(dir, st, prevPath, f, data, rep)
	case !localEdit && !moved:
		return nil
	case !localEdit:
		data, err := f.body(ctx)
		if err != nil {
			return err
		}
		return replace(dir, st, prevPath, f, data, rep)
	case f.version == prev.Version:
		rep.kept("%s has edits of yours: push sends them", prevPath)
		return nil
	default:
		data, err := f.body(ctx)
		if err != nil {
			return err
		}
		if err := write(filepath.Join(dir, conflictsDir, filepath.FromSlash(prevPath)), data); err != nil {
			return err
		}
		prev.Remote = f.version
		rep.kept("%s changed here and on the platform (now v%d): the platform's text is in %s/%s; merge, delete that copy, push",
			prevPath, f.version, conflictsDir, prevPath)
		return nil
	}
}

// replace writes the platform's text at the file's path, moving it when its
// title moved.
func replace(dir string, st *State, prevPath string, f *planned, data []byte, rep *Report) error {
	if err := write(filepath.Join(dir, filepath.FromSlash(f.path)), data); err != nil {
		return err
	}
	if prevPath != f.path {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(prevPath))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(st.Files, prevPath)
		rep.did("moved %s to %s", prevPath, f.path)
	} else {
		rep.did("updated %s", f.path)
	}
	st.Files[f.path] = &File{Kind: f.kind, Slug: f.slug, Version: f.version, SHA: hash(data)}
	return nil
}

// namer gives each file in a folder its title's name, and a second file of
// the same name (as a case-insensitive file system sees it) its slug too.
type namer map[string]bool

func (n namer) name(folder, title, slug string) string {
	base := knowledge.FileName(title)
	p := path.Join(folder, base+".md")
	if n[strings.ToLower(p)] {
		p = path.Join(folder, knowledge.FileName(base+" "+slug)+".md")
	}
	n[strings.ToLower(p)] = true
	return p
}

// planFiles lays the lab out as files.
func planFiles(api API, lab Lab, notes, lessons []Note, exps []Experiment, runs []Run, now time.Time) []*planned {
	labPath := "/labs/" + url.PathEscape(lab.ID)
	text := func(apiPath string, render func(Note) []byte) func(context.Context) ([]byte, error) {
		return func(ctx context.Context) ([]byte, error) {
			var n Note
			if err := api.Do(ctx, http.MethodGet, apiPath, nil, nil, &n); err != nil {
				if isNotFound(err) {
					return nil, fmt.Errorf("%s vanished during the pull: pull again", apiPath)
				}
				return nil, fmt.Errorf("%s: %w", apiPath, err)
			}
			return render(n), nil
		}
	}
	names := namer{}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].CreatedAt.Before(notes[j].CreatedAt) })
	sort.SliceStable(lessons, func(i, j int) bool { return lessons[i].CreatedAt.Before(lessons[j].CreatedAt) })
	var out []*planned
	for _, n := range notes {
		f := &planned{key: keyOf(n.Kind, n.Slug), kind: n.Kind, slug: n.Slug, version: n.Version}
		switch n.Kind {
		case kindBrief:
			f.path = BriefFile
			names[strings.ToLower(BriefFile)] = true
		case kindHandoff:
			f.path = names.name(HandoffsDir, n.CreatedAt.UTC().Format("2006-01-02 15-04"), n.Slug)
		default:
			f.path = names.name(NotesDir, n.Title, n.Slug)
		}
		f.body = text(labPath+"/notes/"+url.PathEscape(n.Slug), renderNote)
		out = append(out, f)
	}
	for _, n := range lessons {
		out = append(out, &planned{key: keyOf(kindLesson, n.Slug), kind: kindLesson, slug: n.Slug, version: n.Version,
			path: names.name(LessonsDir, n.Title, n.Slug), body: text("/lessons/"+url.PathEscape(n.Slug), renderNote)})
	}
	titles := map[string]string{}
	for _, e := range exps {
		titles[e.ID] = e.Title
	}
	for _, e := range exps {
		data := renderExperiment(e, titles, runs)
		out = append(out, &planned{key: keyOf(kindExperiment, e.Slug), kind: kindExperiment, slug: e.Slug, version: e.Version,
			path: names.name(ExperimentsDir, e.Title, e.Slug), body: func(context.Context) ([]byte, error) { return data, nil }})
	}
	runsData := renderRuns(runs, titles)
	readme := renderReadme(lab, exps, len(notes), now)
	out = append(out,
		&planned{key: keyOf(kindGenerated, "runs"), kind: kindGenerated, slug: "runs", path: RunsFile,
			body: func(context.Context) ([]byte, error) { return runsData, nil }},
		&planned{key: keyOf(kindGenerated, "readme"), kind: kindGenerated, slug: "readme", path: ReadmeFile,
			body: func(context.Context) ([]byte, error) { return readme, nil }})
	return out
}
