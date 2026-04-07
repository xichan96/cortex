package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xichan96/cortex/dino/harness"
	dinotask "github.com/xichan96/cortex/dino/task"
)

func TestCompositeVerifierVerifyCommandExit0(t *testing.T) {
	dir := t.TempDir()
	v := NewCompositeVerifier()
	tk := &dinotask.Task{
		Config: &dinotask.TaskConfig{
			VerifyCommand: "exit 0",
			ArtifactDir:   dir,
		},
	}
	snap := &dinotask.TurnSnapshot{}
	ok, reason := v.Verify(context.Background(), tk, snap)
	if !ok || reason != "" {
		t.Fatalf("want ok, got ok=%v reason=%q", ok, reason)
	}
	if snap.VerifyExitCode == nil || *snap.VerifyExitCode != 0 {
		t.Fatalf("exit code: %+v", snap.VerifyExitCode)
	}
}

func TestCompositeVerifierVerifyCommandExit1(t *testing.T) {
	dir := t.TempDir()
	v := NewCompositeVerifier()
	tk := &dinotask.Task{
		Config: &dinotask.TaskConfig{
			VerifyCommand: "exit 1",
			ArtifactDir:   dir,
		},
	}
	snap := &dinotask.TurnSnapshot{}
	ok, reason := v.Verify(context.Background(), tk, snap)
	if ok || reason == "" {
		t.Fatalf("want fail, got ok=%v reason=%q", ok, reason)
	}
	if snap.VerifyExitCode == nil || *snap.VerifyExitCode != 1 {
		t.Fatalf("exit code: %+v", snap.VerifyExitCode)
	}
}

func TestCompositeVerifierVerifyCommandStdoutInReason(t *testing.T) {
	dir := t.TempDir()
	v := NewCompositeVerifier()
	tk := &dinotask.Task{
		Config: &dinotask.TaskConfig{
			VerifyCommand: `echo "nope"; exit 2`,
			ArtifactDir:   dir,
		},
	}
	snap := &dinotask.TurnSnapshot{}
	ok, reason := v.Verify(context.Background(), tk, snap)
	if ok {
		t.Fatal("want fail")
	}
	if reason != "nope\n" && reason != "nope" {
		t.Fatalf("reason: %q", reason)
	}
}

func TestDirTrackedFilesFingerprintFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "progress"), []byte("a"), 0o644)
	fp1, err := harness.DirTrackedFilesFingerprint(dir, []string{"progress", "feature_list.json"})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "progress"), []byte("b"), 0o644)
	fp2, err := harness.DirTrackedFilesFingerprint(dir, []string{"progress", "feature_list.json"})
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp2 {
		t.Fatal("fingerprint should change when progress changes")
	}
}
