package file

import (
	"context"
	"os"
)

type File interface {
	IsDirEmpty(ctx context.Context, dir string) (bool, error)
	ReadDir(ctx context.Context, dir string) ([]string, error)
	Mkdir(ctx context.Context, dir string) error
	RemoveDir(ctx context.Context, dir string) error
	RemoveFile(ctx context.Context, file string) error
	Rename(ctx context.Context, src, dst string) error
	Copy(ctx context.Context, src, dst string) error
	Symlink(ctx context.Context, target, link string) error
	ReadLink(ctx context.Context, link string) (string, error)
	ReadFile(ctx context.Context, file string) ([]byte, error)
	WriteFile(ctx context.Context, file string, data []byte) error
	AppendFile(ctx context.Context, file string, data []byte) error
	Exists(ctx context.Context, file string) (bool, error)
	IsFile(ctx context.Context, file string) (bool, error)
	IsDir(ctx context.Context, dir string) (bool, error)
	Stat(ctx context.Context, file string) (os.FileInfo, error)
	Chmod(ctx context.Context, file string, mode os.FileMode) error
	Walk(ctx context.Context, root string) ([]string, error)
	WalkDir(ctx context.Context, root string) ([]string, error)
	WalkFile(ctx context.Context, root string) ([]string, error)
	WalkRel(ctx context.Context, root string) ([]string, error)
	Glob(ctx context.Context, pattern string) ([]string, error)
}
