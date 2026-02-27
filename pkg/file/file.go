package file

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

type fileImpl struct{}

func New() File {
	return &fileImpl{}
}

func (f *fileImpl) IsDirEmpty(ctx context.Context, dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func (f *fileImpl) ReadDir(ctx context.Context, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (f *fileImpl) Mkdir(ctx context.Context, dir string) error {
	return os.MkdirAll(dir, 0755)
}

func (f *fileImpl) RemoveDir(ctx context.Context, dir string) error {
	return os.RemoveAll(dir)
}

func (f *fileImpl) RemoveFile(ctx context.Context, file string) error {
	return os.Remove(file)
}

func (f *fileImpl) Rename(ctx context.Context, src, dst string) error {
	return os.Rename(src, dst)
}

func (f *fileImpl) Copy(ctx context.Context, src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func (f *fileImpl) Symlink(ctx context.Context, target, link string) error {
	return os.Symlink(target, link)
}

func (f *fileImpl) ReadLink(ctx context.Context, link string) (string, error) {
	return os.Readlink(link)
}

func (f *fileImpl) ReadFile(ctx context.Context, file string) ([]byte, error) {
	return os.ReadFile(file)
}

func (f *fileImpl) WriteFile(ctx context.Context, file string, data []byte) error {
	return os.WriteFile(file, data, 0644)
}

func (f *fileImpl) AppendFile(ctx context.Context, file string, data []byte) error {
	fd, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer fd.Close()
	_, err = fd.Write(data)
	return err
}

func (f *fileImpl) Exists(ctx context.Context, file string) (bool, error) {
	_, err := os.Stat(file)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (f *fileImpl) IsFile(ctx context.Context, file string) (bool, error) {
	info, err := os.Stat(file)
	if err != nil {
		return false, err
	}
	return !info.IsDir(), nil
}

func (f *fileImpl) IsDir(ctx context.Context, dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (f *fileImpl) Stat(ctx context.Context, file string) (os.FileInfo, error) {
	return os.Stat(file)
}

func (f *fileImpl) Chmod(ctx context.Context, file string, mode os.FileMode) error {
	return os.Chmod(file, mode)
}

func (f *fileImpl) Walk(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}

func (f *fileImpl) WalkDir(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

func (f *fileImpl) WalkFile(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

func (f *fileImpl) WalkRel(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relPath)
		} else {
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relPath)
		}
		return nil
	})
	return paths, err
}

func (f *fileImpl) Glob(ctx context.Context, pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}
