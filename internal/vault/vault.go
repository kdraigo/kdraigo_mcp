// Package vault keeps a research lab as a folder of markdown files, the shape
// Obsidian opens with its graph: pull writes the lab, push sends the edits
// back. The platform holds the truth; the folder is a working copy.
//
//	Brief.md                  the lab's brief                    read and write
//	Notes/<title>.md          notes                              read and write
//	Lessons/<title>.md        the user's lessons (every lab)     read and write
//	Experiments/<title>.md    hypothesis, protocol, verdict      read only
//	Handoffs/<when>.md        where each session stopped         read only
//	Runs.md                   the lab's recorded runs            read only
//	README.md                 what is where                      read only
//	.kdraigo/vault.json       what was pulled, at which version
//	.kdraigo/conflicts/       the platform's side of a conflict
//
// Files are named by title (knowledge.FileName), so [[Funding drag]] opens
// "Notes/Funding drag.md" in Obsidian, as the platform resolves it.
package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// The folders, and the files the platform owns.
const (
	BriefFile       = "Brief.md"
	NotesDir        = "Notes"
	LessonsDir      = "Lessons"
	ExperimentsDir  = "Experiments"
	HandoffsDir     = "Handoffs"
	RunsFile        = "Runs.md"
	ReadmeFile      = "README.md"
	stateDir        = ".kdraigo"
	stateFile       = ".kdraigo/vault.json"
	conflictsDir    = ".kdraigo/conflicts"
	kindBrief       = "brief"
	kindNote        = "note"
	kindLesson      = "lesson"
	kindHandoff     = "handoff"
	kindExperiment  = "experiment"
	kindGenerated   = "generated"
	researchAPIPath = "/api/v1/dev"
)

// What the platform answers, as far as a vault needs it.
type (
	Lab struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		Name   string `json:"name"`
		Market string `json:"market"`
	}
	Note struct {
		ID        string    `json:"id"`
		Kind      string    `json:"kind"`
		Slug      string    `json:"slug"`
		Title     string    `json:"title"`
		Body      string    `json:"body"`
		Tags      []string  `json:"tags"`
		Version   int64     `json:"version"`
		UpdatedBy string    `json:"updated_by"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	Experiment struct {
		ID           string         `json:"id"`
		Slug         string         `json:"slug"`
		Title        string         `json:"title"`
		Hypothesis   string         `json:"hypothesis"`
		Mechanism    string         `json:"mechanism"`
		Protocol     map[string]any `json:"protocol"`
		Scope        map[string]any `json:"scope"`
		Tags         []string       `json:"tags"`
		Status       string         `json:"status"`
		RuleHash     string         `json:"rule_hash"`
		RegisteredAt *time.Time     `json:"registered_at"`
		Verdict      string         `json:"verdict"`
		DecidedAt    *time.Time     `json:"decided_at"`
		DerivedFrom  string         `json:"derived_from"`
		Version      int64          `json:"version"`
		CreatedAt    time.Time      `json:"created_at"`
	}
	Run struct {
		ID           string            `json:"id"`
		ExperimentID string            `json:"experiment_id"`
		SessionIDs   []string          `json:"session_ids"`
		Params       map[string]string `json:"params"`
		Pairs        []string          `json:"pairs"`
		Timeframe    string            `json:"timeframe"`
		WindowStart  *time.Time        `json:"window_start"`
		WindowEnd    *time.Time        `json:"window_end"`
		Status       string            `json:"status"`
		Metrics      struct {
			ReturnPct      *float64 `json:"return_pct"`
			MaxDrawdownPct *float64 `json:"max_drawdown_pct"`
			Trades         *int     `json:"trades"`
			ProfitFactor   *float64 `json:"profit_factor"`
		} `json:"metrics"`
		CreatedAt time.Time `json:"created_at"`
	}
)

// API is what a vault asks of research_service.
type API interface {
	// Do sends a request to research_service's dev API (path below
	// /api/v1/dev), decoding a 2xx answer into out when out is not nil.
	Do(ctx context.Context, method, path string, query url.Values, body, out any) error
}

// StatusError is an answer other than 2xx.
type StatusError struct {
	Code int
	Msg  string
	// Version is a 409's current version of the note.
	Version int64
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Msg) }

// IsConflict reports a write refused because the note moved on.
func IsConflict(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusConflict
}

func isNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// NewAPI is the platform's research_service, signed with the user's key.
func NewAPI(h *client.HTTP) API { return httpAPI{h} }

type httpAPI struct{ h *client.HTTP }

func (a httpAPI) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, status, err := a.h.Do(ctx, true, client.HeaderStyleStandard, method, client.Research, researchAPIPath+path, query, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		var e struct {
			Error   string `json:"error"`
			Current int64  `json:"current_version"`
		}
		_ = json.Unmarshal(resp, &e)
		if e.Error == "" {
			e.Error = string(resp)
		}
		return &StatusError{Code: status, Msg: e.Error, Version: e.Current}
	}
	if out == nil || len(resp) == 0 {
		return nil
	}
	return json.Unmarshal(resp, out)
}

// State is what the last pull and push left: the lab, and each file of the
// platform's with the version it holds and the hash of what is on disk.
type State struct {
	Lab      Lab              `json:"lab"`
	PulledAt time.Time        `json:"pulled_at"`
	Files    map[string]*File `json:"files"`
}

// File is one file the platform knows of, by its path in the vault.
type File struct {
	Kind    string `json:"kind"`
	Slug    string `json:"slug,omitempty"`
	Version int64  `json:"version,omitempty"`
	SHA     string `json:"sha256"`
	// Remote is the platform's version when a pull found both sides
	// changed; its text is in .kdraigo/conflicts. Push sends this file
	// once that copy is deleted (resolved), over version Remote.
	Remote int64 `json:"remote,omitempty"`
}

// editable kinds are pushed back; the rest are the platform's copies.
func editable(kind string) bool { return kind == kindBrief || kind == kindNote || kind == kindLesson }

func loadState(dir string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", stateFile, err)
	}
	if s.Files == nil {
		s.Files = map[string]*File{}
	}
	return &s, nil
}

func (s *State) save(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, stateDir), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, stateFile))
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fileHash is the hash of a file on disk; "" when it does not exist.
func fileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash(b), nil
}

// write puts a file in place whole: a reader never sees half of it.
func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".kdraigo-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Report says what a pull or push did, file by file.
type Report struct {
	Lab Lab
	// Done lists what changed: "updated Notes/Funding drag.md".
	Done []string
	// Kept lists local edits left alone, conflicts and refusals: what the
	// user has to look at.
	Kept []string
}

func (r *Report) did(format string, a ...any)  { r.Done = append(r.Done, fmt.Sprintf(format, a...)) }
func (r *Report) kept(format string, a ...any) { r.Kept = append(r.Kept, fmt.Sprintf(format, a...)) }

// String is the report for a person or an AI: what was done, then what to look at.
func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Lab %s (%s)\n", r.Lab.Name, r.Lab.Slug)
	if len(r.Done) == 0 && len(r.Kept) == 0 {
		b.WriteString("Nothing to do: the vault and the platform agree.\n")
	}
	for _, d := range r.Done {
		fmt.Fprintf(&b, "  %s\n", d)
	}
	if len(r.Kept) > 0 {
		b.WriteString("To look at:\n")
		for _, k := range r.Kept {
			fmt.Fprintf(&b, "  ! %s\n", k)
		}
	}
	return b.String()
}
