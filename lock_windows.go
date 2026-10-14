package client

import (
	"errors"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION, which package syscall does
// not name.
const errSharingViolation syscall.Errno = 32

// tryLock opens path with no sharing: a second opener is refused until the
// first closes it, which is an exclusive lock that a crash releases.
func tryLock(path string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, errLockBusy
		}
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
