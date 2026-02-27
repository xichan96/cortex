//go:build !cgo

package sqlite

func IsDuplicateKeyError(err error) bool {
	return false
}
