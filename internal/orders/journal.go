package orders

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Journal is the executor's local record of what it started placing and
// what it reported, one JSON line each (~/.kdraigo/orders-journal.jsonl). It
// is what keeps a proposal from being placed twice: after a crash between
// placing and reporting, the next run finds "started" without "reported" and
// looks at the exchange instead of placing again.
type Journal struct {
	path string
	mu   sync.Mutex
	// state: proposal id → "started" or "reported".
	state map[string]string
}

type journalLine struct {
	ID     string    `json:"id"`
	Event  string    `json:"event"` // started, reported
	Hash   string    `json:"hash,omitempty"`
	Status string    `json:"status,omitempty"`
	At     time.Time `json:"at"`
}

// OpenJournal reads the journal at path (none yet is an empty one).
func OpenJournal(path string) (*Journal, error) {
	j := &Journal{path: path, state: map[string]string{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l journalLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.ID == "" {
			continue
		}
		if j.state[l.ID] != "reported" {
			j.state[l.ID] = l.Event
		}
	}
	return j, sc.Err()
}

// State is "", "started" or "reported".
func (j *Journal) State(id string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state[id]
}

// Started records, before anything reaches the exchange, that placing began.
func (j *Journal) Started(id, hash string) error {
	return j.write(journalLine{ID: id, Event: "started", Hash: hash, At: time.Now().UTC()})
}

// Reported records that kdraigo heard the outcome.
func (j *Journal) Reported(id, status string) error {
	return j.write(journalLine{ID: id, Event: "reported", Status: status, At: time.Now().UTC()})
}

func (j *Journal) write(l journalLine) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	j.state[l.ID] = l.Event
	return nil
}
