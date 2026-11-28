package orders

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// One executor per account on a computer (wiki bug #76). Every Claude Code
// session starts a kdraigo-mcp of its own, and each used to run the executor
// under the computer's one name: they took kdraigo's order stream from each
// other, and they shared one "AI orders" live-console session, so closing any
// session told the user AI orders had stopped while the others ran on.
//
// Now the process that holds the account's executor lock (an OS lock on a
// file in ~/.kdraigo, let go by the OS when the process ends, even on a
// crash) runs the order stream and the console and places what the user
// confirms. The others stand by: they propose (kdraigo sends a confirmed
// order to this computer's executor, the one holding the lock), their checks
// read the console file the holder keeps, and one of them takes over when the
// holder ends. The lock is per kdraigo account, as the console file is: two
// accounts' MCPs on one computer each have an executor (bug #65).

// errLocked: another process holds the lock.
var errLocked = errors.New("held by another process")

// ExecutorLock is the account's executor lock, held by this process.
type ExecutorLock struct {
	f      *os.File
	holder string // the holder file, naming the process that holds the lock
	token  string
}

// Holder is the process holding an executor lock, as it wrote itself down.
type Holder struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	// Token tells one taking of the lock from another (two in one process,
	// in tests, have one PID).
	Token string `json:"token"`
}

// lockFiles are the lock file and the holder file of the account a key
// belongs to, next to its console file.
func lockFiles(dir, keyID string) (lock, holder string) {
	base := filepath.Join(dir, "ai-orders-executor-"+keyTag(keyID))
	return base + ".lock", base + ".json"
}

// TryLockExecutor takes the account's executor lock in dir when no other
// process holds it. Held by another: nil, and that process as it wrote
// itself down (zero when it cannot be read).
func TryLockExecutor(dir, keyID string) (*ExecutorLock, Holder, error) {
	lockPath, holderPath := lockFiles(dir, keyID)
	f, err := tryLockFile(lockPath)
	if errors.Is(err, errLocked) {
		h, _ := ReadHolder(dir, keyID)
		return nil, h, nil
	}
	if err != nil {
		return nil, Holder{}, err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		unlockFile(f)
		return nil, Holder{}, err
	}
	l := &ExecutorLock{f: f, holder: holderPath, token: hex.EncodeToString(b)}
	h := Holder{PID: os.Getpid(), Started: time.Now().UTC(), Token: l.token}
	// The holder file only tells the standbys who places: not writing it
	// takes nothing from the lock.
	if raw, err := json.Marshal(h); err == nil {
		tmp := holderPath + ".tmp" + l.token
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, holderPath)
		}
	}
	return l, h, nil
}

// ReadHolder is the process that wrote itself down as holding the account's
// executor lock (the last one, if none holds it now).
func ReadHolder(dir, keyID string) (Holder, error) {
	_, holderPath := lockFiles(dir, keyID)
	raw, err := os.ReadFile(holderPath)
	if err != nil {
		return Holder{}, err
	}
	var h Holder
	err = json.Unmarshal(raw, &h)
	return h, err
}

// Release lets another process take the lock.
func (l *ExecutorLock) Release() {
	if l != nil && l.f != nil {
		unlockFile(l.f)
		l.f = nil
	}
}

// TakenOver waits up to d, after Release, for another process to take the
// lock: true once the holder file names another taking of it.
func (l *ExecutorLock) TakenOver(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		raw, err := os.ReadFile(l.holder)
		var h Holder
		if err == nil && json.Unmarshal(raw, &h) == nil && h.Token != "" && h.Token != l.token {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}
