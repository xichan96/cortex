package file

import "os"

type File interface {
	IsDirEmpty(dir string) (bool, error)
	ReadDir(dir string) ([]string, error)
	Mkdir(dir string) error
	RemoveDir(dir string) error
	RemoveFile(file string) error
	Rename(src, dst string) error
	Copy(src, dst string) error
	Symlink(target, link string) error
	ReadLink(link string) (string, error)
	ReadFile(file string) ([]byte, error)
	WriteFile(file string, data []byte) error
	AppendFile(file string, data []byte) error
	Exists(file string) (bool, error)
	IsFile(file string) (bool, error)
	IsDir(dir string) (bool, error)
	Stat(file string) (os.FileInfo, error)
	Chmod(file string, mode os.FileMode) error
	Walk(root string) ([]string, error)
	WalkDir(root string) ([]string, error)
	WalkFile(root string) ([]string, error)
	WalkRel(root string) ([]string, error)
	Glob(pattern string) ([]string, error)
}
