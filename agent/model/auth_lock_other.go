//go:build !unix

package model

import "os"

// lockFile is a no-op on platforms without flock. Writes remain atomic and
// in-process calls remain serialized by the store's mutex.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
