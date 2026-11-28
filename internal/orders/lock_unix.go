//go:build unix

package orders

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive flock on path without waiting. A flock
// belongs to the open file: the OS lets it go when the process ends, and
// another process opening and closing the file does not touch it. Go opens
// files close-on-exec, so a program the MCP starts does not keep it.
func tryLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
