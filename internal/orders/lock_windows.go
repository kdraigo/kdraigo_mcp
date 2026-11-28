//go:build windows

package orders

import (
	"errors"
	"os"
	"syscall"
)

// errorSharingViolation is Windows' ERROR_SHARING_VIOLATION (the syscall
// package does not name it).
const errorSharingViolation syscall.Errno = 32

// tryLockFile opens path shared with no one: another process's open fails
// with a sharing violation until this handle closes, which Windows does when
// the process ends.
func tryLockFile(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, errLocked
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func unlockFile(f *os.File) { _ = f.Close() }
