//go:build !unix && !windows

package orders

import "os"

// tryLockFile: no file lock on this system, so every process runs an
// executor, as before the lock (wiki bug #76).
func tryLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
}

func unlockFile(f *os.File) { _ = f.Close() }
