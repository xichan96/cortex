package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIsDirEmpty(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	empty, err := f.IsDirEmpty(ctx, dir)
	if err != nil {
		t.Fatalf("IsDirEmpty failed: %v", err)
	}
	if !empty {
		t.Error("Expected empty directory")
	}

	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	empty, err = f.IsDirEmpty(ctx, dir)
	if err != nil {
		t.Fatalf("IsDirEmpty failed: %v", err)
	}
	if empty {
		t.Error("Expected non-empty directory")
	}
}

func TestReadDir(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	names, err := f.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("Expected empty slice, got %v", names)
	}

	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	names, err = f.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("Expected 2 files, got %d", len(names))
	}
}

func TestMkdir(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	newDir := filepath.Join(dir, "newdir")

	err := f.Mkdir(ctx, newDir)
	if err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	exists, _ := f.Exists(ctx, newDir)
	if !exists {
		t.Error("Directory was not created")
	}
}

func TestRemoveDir(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testDir := filepath.Join(dir, "testdir")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}

	err := f.RemoveDir(ctx, testDir)
	if err != nil {
		t.Fatalf("RemoveDir failed: %v", err)
	}

	exists, _ := f.Exists(ctx, testDir)
	if exists {
		t.Error("Directory was not removed")
	}
}

func TestRemoveFile(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	err := f.RemoveFile(ctx, testFile)
	if err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}

	exists, _ := f.Exists(ctx, testFile)
	if exists {
		t.Error("File was not removed")
	}
}

func TestRename(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	err := f.Rename(ctx, src, dst)
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	srcExists, _ := f.Exists(ctx, src)
	if srcExists {
		t.Error("Source file still exists")
	}

	dstExists, _ := f.Exists(ctx, dst)
	if !dstExists {
		t.Error("Destination file was not created")
	}
}

func TestCopy(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	content := []byte("test content")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	err := f.Copy(ctx, src, dst)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	dstContent, err := f.ReadFile(ctx, dst)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(dstContent) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", string(content), string(dstContent))
	}
}

func TestSymlink(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	err := f.Symlink(ctx, target, link)
	if err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	linkTarget, err := f.ReadLink(ctx, link)
	if err != nil {
		t.Fatalf("ReadLink failed: %v", err)
	}

	if linkTarget != target {
		t.Errorf("Link target mismatch: expected %s, got %s", target, linkTarget)
	}
}

func TestReadLink(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("os.Symlink failed: %v", err)
	}

	linkTarget, err := f.ReadLink(ctx, link)
	if err != nil {
		t.Fatalf("ReadLink failed: %v", err)
	}

	if linkTarget != target {
		t.Errorf("Link target mismatch: expected %s, got %s", target, linkTarget)
	}
}

func TestReadFile(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content := []byte("test content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	data, err := f.ReadFile(ctx, testFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", string(content), string(data))
	}
}

func TestWriteFile(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content := []byte("test content")

	err := f.WriteFile(ctx, testFile, content)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("os.ReadFile failed: %v", err)
	}

	if string(data) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", string(content), string(data))
	}
}

func TestAppendFile(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content1 := []byte("first ")
	content2 := []byte("second")

	err := f.WriteFile(ctx, testFile, content1)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = f.AppendFile(ctx, testFile, content2)
	if err != nil {
		t.Fatalf("AppendFile failed: %v", err)
	}

	data, err := f.ReadFile(ctx, testFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	expected := string(content1) + string(content2)
	if string(data) != expected {
		t.Errorf("Content mismatch: expected %s, got %s", expected, string(data))
	}
}

func TestExists(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")

	exists, err := f.Exists(ctx, testFile)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("File should not exist")
	}

	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	exists, err = f.Exists(ctx, testFile)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("File should exist")
	}
}

func TestIsFile(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	testDir := filepath.Join(dir, "testdir")

	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}

	isFile, err := f.IsFile(ctx, testFile)
	if err != nil {
		t.Fatalf("IsFile failed: %v", err)
	}
	if !isFile {
		t.Error("Expected file")
	}

	isFile, err = f.IsFile(ctx, testDir)
	if err != nil {
		t.Fatalf("IsFile failed: %v", err)
	}
	if isFile {
		t.Error("Expected directory, not file")
	}
}

func TestIsDir(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	testDir := filepath.Join(dir, "testdir")

	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}

	isDir, err := f.IsDir(ctx, testDir)
	if err != nil {
		t.Fatalf("IsDir failed: %v", err)
	}
	if !isDir {
		t.Error("Expected directory")
	}

	isDir, err = f.IsDir(ctx, testFile)
	if err != nil {
		t.Fatalf("IsDir failed: %v", err)
	}
	if isDir {
		t.Error("Expected file, not directory")
	}
}

func TestStat(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	info, err := f.Stat(ctx, testFile)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if info.IsDir() {
		t.Error("Expected file, not directory")
	}

	if info.Name() != "test.txt" {
		t.Errorf("Name mismatch: expected test.txt, got %s", info.Name())
	}
}

func TestChmod(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	err := f.Chmod(ctx, testFile, 0755)
	if err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("os.Stat failed: %v", err)
	}

	if info.Mode().Perm() != 0755 {
		t.Errorf("Permission mismatch: expected 0755, got %o", info.Mode().Perm())
	}
}

func TestWalk(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "file2.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	paths, err := f.Walk(ctx, dir)
	if err != nil {
		t.Fatalf("Walk failed: %v", err)
	}

	if len(paths) < 4 {
		t.Errorf("Expected at least 4 paths, got %d", len(paths))
	}
}

func TestWalkDir(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	paths, err := f.WalkDir(ctx, dir)
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}

	if len(paths) < 2 {
		t.Errorf("Expected at least 2 directories, got %d", len(paths))
	}

	for _, path := range paths {
		isDir, _ := f.IsDir(ctx, path)
		if !isDir {
			t.Errorf("Expected directory, got file: %s", path)
		}
	}
}

func TestWalkFile(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "file2.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	paths, err := f.WalkFile(ctx, dir)
	if err != nil {
		t.Fatalf("WalkFile failed: %v", err)
	}

	if len(paths) < 2 {
		t.Errorf("Expected at least 2 files, got %d", len(paths))
	}

	for _, path := range paths {
		isFile, _ := f.IsFile(ctx, path)
		if !isFile {
			t.Errorf("Expected file, got directory: %s", path)
		}
	}
}

func TestWalkRel(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatalf("os.MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "file2.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	paths, err := f.WalkRel(ctx, dir)
	if err != nil {
		t.Fatalf("WalkRel failed: %v", err)
	}

	if len(paths) < 4 {
		t.Errorf("Expected at least 4 paths, got %d", len(paths))
	}

	for _, path := range paths {
		if filepath.IsAbs(path) {
			t.Errorf("Expected relative path, got absolute: %s", path)
		}
	}
}

func TestGlob(t *testing.T) {
	f := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file3.go"), []byte("test"), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}

	pattern := filepath.Join(dir, "*.txt")
	matches, err := f.Glob(ctx, pattern)
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}

	if len(matches) != 2 {
		t.Errorf("Expected 2 matches, got %d", len(matches))
	}
}
