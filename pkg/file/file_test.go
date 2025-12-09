package file

import (
	"os"
	"path/filepath"
	"testing"
)

var tmpDir = "/tmp"

func TestIsDirEmpty(t *testing.T) {
	f := New()

	empty, err := f.IsDirEmpty(tmpDir)
	if err != nil {
		t.Fatalf("IsDirEmpty failed: %v", err)
	}
	if !empty {
		t.Error("Expected empty directory")
	}

	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	empty, err = f.IsDirEmpty(tmpDir)
	if err != nil {
		t.Fatalf("IsDirEmpty failed: %v", err)
	}
	if empty {
		t.Error("Expected non-empty directory")
	}
}

func TestReadDir(t *testing.T) {
	f := New()

	names, err := f.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("Expected empty slice, got %v", names)
	}

	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("test"), 0644)

	names, err = f.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("Expected 2 files, got %d", len(names))
	}
}

func TestMkdir(t *testing.T) {
	f := New()

	newDir := filepath.Join(tmpDir, "newdir")

	err := f.Mkdir(newDir)
	if err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	exists, _ := f.Exists(newDir)
	if !exists {
		t.Error("Directory was not created")
	}
}

func TestRemoveDir(t *testing.T) {
	f := New()

	testDir := filepath.Join(tmpDir, "testdir")
	os.MkdirAll(testDir, 0755)

	err := f.RemoveDir(testDir)
	if err != nil {
		t.Fatalf("RemoveDir failed: %v", err)
	}

	exists, _ := f.Exists(testDir)
	if exists {
		t.Error("Directory was not removed")
	}
}

func TestRemoveFile(t *testing.T) {
	f := New()

	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	err := f.RemoveFile(testFile)
	if err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}

	exists, _ := f.Exists(testFile)
	if exists {
		t.Error("File was not removed")
	}
}

func TestRename(t *testing.T) {
	f := New()

	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	os.WriteFile(src, []byte("test"), 0644)

	err := f.Rename(src, dst)
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	srcExists, _ := f.Exists(src)
	if srcExists {
		t.Error("Source file still exists")
	}

	dstExists, _ := f.Exists(dst)
	if !dstExists {
		t.Error("Destination file was not created")
	}
}

func TestCopy(t *testing.T) {
	f := New()

	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	content := []byte("test content")
	os.WriteFile(src, content, 0644)

	err := f.Copy(src, dst)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	dstContent, err := f.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(dstContent) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", string(content), string(dstContent))
	}
}

func TestSymlink(t *testing.T) {
	f := New()

	target := filepath.Join(tmpDir, "target.txt")
	link := filepath.Join(tmpDir, "link.txt")
	os.WriteFile(target, []byte("test"), 0644)

	err := f.Symlink(target, link)
	if err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	linkTarget, err := f.ReadLink(link)
	if err != nil {
		t.Fatalf("ReadLink failed: %v", err)
	}

	if linkTarget != target {
		t.Errorf("Link target mismatch: expected %s, got %s", target, linkTarget)
	}
}

func TestReadLink(t *testing.T) {
	f := New()

	target := filepath.Join(tmpDir, "target.txt")
	link := filepath.Join(tmpDir, "link.txt")
	os.WriteFile(target, []byte("test"), 0644)
	os.Symlink(target, link)

	linkTarget, err := f.ReadLink(link)
	if err != nil {
		t.Fatalf("ReadLink failed: %v", err)
	}

	if linkTarget != target {
		t.Errorf("Link target mismatch: expected %s, got %s", target, linkTarget)
	}
}

func TestReadFile(t *testing.T) {
	f := New()

	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content")
	os.WriteFile(testFile, content, 0644)

	data, err := f.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if string(data) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", string(content), string(data))
	}
}

func TestWriteFile(t *testing.T) {
	f := New()

	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content")

	err := f.WriteFile(testFile, content)
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

	testFile := filepath.Join(tmpDir, "test.txt")
	content1 := []byte("first ")
	content2 := []byte("second")

	err := f.WriteFile(testFile, content1)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err = f.AppendFile(testFile, content2)
	if err != nil {
		t.Fatalf("AppendFile failed: %v", err)
	}

	data, err := f.ReadFile(testFile)
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

	testFile := filepath.Join(tmpDir, "test.txt")

	exists, err := f.Exists(testFile)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("File should not exist")
	}

	os.WriteFile(testFile, []byte("test"), 0644)

	exists, err = f.Exists(testFile)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("File should exist")
	}
}

func TestIsFile(t *testing.T) {
	f := New()

	testFile := filepath.Join(tmpDir, "test.txt")
	testDir := filepath.Join(tmpDir, "testdir")

	os.WriteFile(testFile, []byte("test"), 0644)
	os.MkdirAll(testDir, 0755)

	isFile, err := f.IsFile(testFile)
	if err != nil {
		t.Fatalf("IsFile failed: %v", err)
	}
	if !isFile {
		t.Error("Expected file")
	}

	isFile, err = f.IsFile(testDir)
	if err != nil {
		t.Fatalf("IsFile failed: %v", err)
	}
	if isFile {
		t.Error("Expected directory, not file")
	}
}

func TestIsDir(t *testing.T) {
	f := New()

	testFile := filepath.Join(tmpDir, "test.txt")
	testDir := filepath.Join(tmpDir, "testdir")

	os.WriteFile(testFile, []byte("test"), 0644)
	os.MkdirAll(testDir, 0755)

	isDir, err := f.IsDir(testDir)
	if err != nil {
		t.Fatalf("IsDir failed: %v", err)
	}
	if !isDir {
		t.Error("Expected directory")
	}

	isDir, err = f.IsDir(testFile)
	if err != nil {
		t.Fatalf("IsDir failed: %v", err)
	}
	if isDir {
		t.Error("Expected file, not directory")
	}
}

func TestStat(t *testing.T) {
	f := New()

	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	info, err := f.Stat(testFile)
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

	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	err := f.Chmod(testFile, 0755)
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

	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "file2.txt"), []byte("test"), 0644)

	paths, err := f.Walk(tmpDir)
	if err != nil {
		t.Fatalf("Walk failed: %v", err)
	}

	if len(paths) < 4 {
		t.Errorf("Expected at least 4 paths, got %d", len(paths))
	}
}

func TestWalkDir(t *testing.T) {
	f := New()

	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)

	paths, err := f.WalkDir(tmpDir)
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}

	if len(paths) < 2 {
		t.Errorf("Expected at least 2 directories, got %d", len(paths))
	}

	for _, path := range paths {
		isDir, _ := f.IsDir(path)
		if !isDir {
			t.Errorf("Expected directory, got file: %s", path)
		}
	}
}

func TestWalkFile(t *testing.T) {
	f := New()

	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "file2.txt"), []byte("test"), 0644)

	paths, err := f.WalkFile(tmpDir)
	if err != nil {
		t.Fatalf("WalkFile failed: %v", err)
	}

	if len(paths) < 2 {
		t.Errorf("Expected at least 2 files, got %d", len(paths))
	}

	for _, path := range paths {
		isFile, _ := f.IsFile(path)
		if !isFile {
			t.Errorf("Expected file, got directory: %s", path)
		}
	}
}

func TestWalkRel(t *testing.T) {
	f := New()

	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "file2.txt"), []byte("test"), 0644)

	paths, err := f.WalkRel(tmpDir)
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

	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file3.go"), []byte("test"), 0644)

	pattern := filepath.Join(tmpDir, "*.txt")
	matches, err := f.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}

	if len(matches) != 2 {
		t.Errorf("Expected 2 matches, got %d", len(matches))
	}
}
