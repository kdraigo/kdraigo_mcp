package vault

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	knowledge "github.com/kdraigo/workspaces_knowledge"
)

// PushOptions: Delete deletes on the platform the notes deleted here; DryRun
// only says what a push would send.
type PushOptions struct {
	Delete bool
	DryRun bool
}

// local is an editable file found in the vault.
type local struct {
	path string
	kind string
	data []byte
}

// Push sends the brief, notes and lessons edited here, each over the version
// it was pulled at: a note the platform changed since is refused, and stays
// as it is here. A file renamed here (Obsidian renames its links with it)
// renames its note; a new file makes a new note.
func Push(ctx context.Context, api API, dir string, opts PushOptions) (*Report, error) {
	st, err := loadState(dir)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("this folder is not a vault: pull a lab into it first")
	}
	rep := &Report{Lab: st.Lab}
	files, err := scan(dir, rep)
	if err != nil {
		return nil, err
	}
	// Read-only copies edited here are said so, not sent.
	for p, f := range st.Files {
		if editable(f.Kind) {
			continue
		}
		if sum, err := fileHash(filepath.Join(dir, filepath.FromSlash(p))); err == nil && sum != "" && sum != f.SHA {
			rep.kept("%s is a read-only copy: your edit is not sent (the next pull replaces it)", p)
		}
	}

	bySlug := map[string]string{}
	for p, f := range st.Files {
		if editable(f.Kind) {
			bySlug[keyOf(f.Kind, f.Slug)] = p
		}
	}
	present := map[string]bool{}
	for _, l := range files {
		present[l.path] = true
	}
	claimed := map[string]bool{}
	labPath := "/labs/" + url.PathEscape(st.Lab.ID)
	for _, l := range files {
		sum := hash(l.data)
		doc, err := knowledge.Parse(l.data)
		if err != nil {
			rep.kept("%s: %v; not sent", l.path, err)
			continue
		}
		entry, from := st.Files[l.path], l.path
		if entry == nil || entry.Kind != l.kind {
			entry = nil
			// Renamed here: its slug is in the frontmatter, and its old file is gone.
			if old, ok := bySlug[keyOf(l.kind, doc.String("slug"))]; ok && !present[old] && !claimed[old] {
				entry, from = st.Files[old], old
			}
		}
		claimed[from] = true
		if entry != nil && from == l.path && sum == entry.SHA {
			continue
		}
		if entry != nil && entry.Remote != 0 {
			if _, err := os.Stat(filepath.Join(dir, conflictsDir, filepath.FromSlash(from))); err == nil {
				rep.kept("%s: resolve the conflict first: merge %s/%s into it, delete that copy, push", l.path, conflictsDir, from)
				continue
			}
		}

		title := titleOf(l, doc)
		body := map[string]any{"kind": l.kind, "title": title, "body": doc.Body, "tags": tagsOf(doc)}
		method, apiPath := http.MethodPost, labPath+"/notes"
		switch {
		case l.kind == kindBrief:
			method, apiPath = http.MethodPut, labPath+"/notes/brief"
		case l.kind == kindLesson && entry != nil:
			method, apiPath = http.MethodPut, "/lessons/"+url.PathEscape(entry.Slug)
		case l.kind == kindLesson:
			apiPath = "/lessons"
		case entry != nil:
			method, apiPath = http.MethodPut, labPath+"/notes/"+url.PathEscape(entry.Slug)
		}
		var base int64
		if entry != nil {
			base = entry.Version
			if entry.Remote != 0 {
				base = entry.Remote
			}
		}
		body["base_version"] = base
		what := "sent"
		switch {
		case entry == nil:
			what = "created"
		case from != l.path:
			what = "renamed " + from + " →"
		}
		if opts.DryRun {
			rep.did("would have %s %s", what, l.path)
			continue
		}
		var saved Note
		if err := api.Do(ctx, method, apiPath, nil, body, &saved); err != nil {
			var se *StatusError
			switch {
			case IsConflict(err) && entry == nil:
				rep.kept("%s: a note of that name is on the platform already; pull, then merge it into that note", l.path)
			case IsConflict(err):
				rep.kept("%s changed on the platform since your pull (now v%d): pull, merge what lands in %s, push", l.path, conflictVersion(err), conflictsDir)
			case errors.As(err, &se) && se.Code < 500:
				rep.kept("%s refused: %s", l.path, se.Msg)
			default:
				return rep, fmt.Errorf("%s: %w", l.path, err)
			}
			continue
		}
		if from != l.path {
			delete(st.Files, from)
		}
		st.Files[l.path] = &File{Kind: l.kind, Slug: saved.Slug, Version: saved.Version, SHA: sum}
		rep.did("%s %s (v%d)", what, l.path, saved.Version)
	}

	// Deleted here.
	var gone []string
	for p, f := range st.Files {
		if editable(f.Kind) && !present[p] && !claimed[p] {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		f := st.Files[p]
		switch {
		case f.Kind == kindBrief:
			rep.kept("%s was deleted here: the brief cannot be deleted; the next pull brings it back", p)
		case !opts.Delete:
			rep.kept("%s was deleted here: push with delete to delete it on the platform", p)
		case opts.DryRun:
			rep.did("would have deleted %s on the platform", p)
		default:
			apiPath := labPath + "/notes/" + url.PathEscape(f.Slug)
			if f.Kind == kindLesson {
				apiPath = "/lessons/" + url.PathEscape(f.Slug)
			}
			err := api.Do(ctx, http.MethodDelete, apiPath, url.Values{"base_version": {fmt.Sprint(f.Version)}}, nil, nil)
			switch {
			case err == nil || isNotFound(err):
				delete(st.Files, p)
				rep.did("deleted %s on the platform", p)
			case IsConflict(err):
				rep.kept("%s changed on the platform since your pull: not deleted; pull to see the change", p)
			default:
				return rep, fmt.Errorf("deleting %s: %w", p, err)
			}
		}
	}
	if opts.DryRun {
		return rep, nil
	}
	return rep, st.save(dir)
}

func conflictVersion(err error) int64 {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Version
	}
	return 0
}

// scan finds the brief, the notes and the lessons, in subfolders too, and
// says which other markdown files a push leaves out.
func scan(dir string, rep *Report) ([]local, error) {
	var out []local
	err := filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, full)
		rel = filepath.ToSlash(rel)
		if rel != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(rel, ".md") {
			return nil
		}
		top, _, _ := strings.Cut(rel, "/")
		var kind string
		switch {
		case rel == BriefFile:
			kind = kindBrief
		case top == NotesDir && rel != top:
			kind = kindNote
		case top == LessonsDir && rel != top:
			kind = kindLesson
		case rel == ReadmeFile || rel == RunsFile || top == ExperimentsDir || top == HandoffsDir:
			return nil
		default:
			rep.kept("%s is outside Notes/ and Lessons/: not sent", rel)
			return nil
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		out = append(out, local{path: rel, kind: kind, data: data})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, err
}

// titleOf is a note's title: its file's name, unless the frontmatter holds a
// title that names the same file (one a file name cannot spell).
func titleOf(l local, doc knowledge.Doc) string {
	if l.kind == kindBrief {
		return "Brief"
	}
	base := strings.TrimSuffix(path.Base(l.path), ".md")
	if t := doc.String("title"); t != "" && knowledge.FileName(t) == base {
		return t
	}
	return base
}

func tagsOf(doc knowledge.Doc) []string {
	tags := doc.Strings("tags")
	if tags == nil {
		return []string{}
	}
	return tags
}
