//go:build !unix

package transcript

import (
	"fmt"
	"os"
)

// acquireLock emulates flock with an exclusive marker file. The marker is
// removed by releaseLock; a crashed writer leaves it behind and a human must
// remove it.
func acquireLock(path string) (*os.File, error) {
	name := path + ".lock"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, name)
		}
		return nil, err
	}
	return f, nil
}

func releaseLock(f *os.File) error {
	name := f.Name()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}
