//go:build unix

package transcript

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes a non-blocking exclusive flock on "<path>.lock".
func acquireLock(path string) (*os.File, error) {
	name := path + ".lock"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, name)
		}
		return nil, fmt.Errorf("transcript: lock %s: %w", name, err)
	}
	return f, nil
}

func releaseLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
