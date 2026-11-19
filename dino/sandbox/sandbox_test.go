package sandbox_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/xichan96/cortex/dino/sandbox"
)

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test requires a Unix shell")
	}
}

func newHost(t *testing.T, opts sandbox.HostOptions) *sandbox.HostSandbox {
	t.Helper()
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	sb, err := sandbox.NewHostSandbox(opts)
	if err != nil {
		t.Fatalf("NewHostSandbox: %v", err)
	}
	return sb
}

func TestHostSandboxNameAndRoot(t *testing.T) {
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	if got := sb.Name(); got != "host" {
		t.Fatalf("Name() = %q, want host", got)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := sb.Root(); got != filepath.Clean(abs) {
		t.Fatalf("Root() = %q, want %q", got, filepath.Clean(abs))
	}
}

func TestNewHostSandboxRejectsMissingRoot(t *testing.T) {
	_, err := sandbox.NewHostSandbox(sandbox.HostOptions{
		Root: filepath.Join(t.TempDir(), "missing"),
	})
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestNewHostSandboxRejectsFileRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := sandbox.NewHostSandbox(sandbox.HostOptions{Root: root})
	if !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("expected ErrInvalidSpec, got %v", err)
	}
}

func TestRunEcho(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := string(res.Stdout); got != "hello\n" {
		t.Fatalf("Stdout = %q, want %q", got, "hello\n")
	}
	if len(res.Stderr) != 0 {
		t.Fatalf("Stderr = %q, want empty", res.Stderr)
	}
	if res.Truncated {
		t.Fatal("Truncated = true, want false")
	}
	if res.Duration <= 0 {
		t.Fatalf("Duration = %v, want > 0", res.Duration)
	}
}

func TestRunExitCodeAndStderr(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", "printf out; printf err >&2; exit 7"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}
	if got := string(res.Stdout); got != "out" {
		t.Fatalf("Stdout = %q, want %q", got, "out")
	}
	if got := string(res.Stderr); got != "err" {
		t.Fatalf("Stderr = %q, want %q", got, "err")
	}
}

func TestRunTimeoutKillsProcessTree(t *testing.T) {
	requireShell(t)
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	marker := filepath.Join(root, "marker")

	started := time.Now()
	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", "sleep 0.5; touch marker"},
		Timeout: 100 * time.Millisecond,
	})
	elapsed := time.Since(started)

	if !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run took %v, timeout did not take effect", elapsed)
	}
	if res.Duration <= 0 || res.Duration > 3*time.Second {
		t.Fatalf("Duration = %v", res.Duration)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("marker exists or stat failed unexpectedly: %v", statErr)
	}
}

func TestRunContextCancellationKillsProcess(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := sb.Run(ctx, sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", "sleep 5"},
	})
	if !errors.Is(err, sandbox.ErrCanceled) {
		t.Fatalf("err = %v, want ErrCanceled", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Run took %v, cancellation did not take effect", elapsed)
	}
}

func TestRunPreCanceledContext(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := sb.Run(ctx, sandbox.ExecSpec{Command: "echo", Args: []string{"nope"}})
	if !errors.Is(err, sandbox.ErrCanceled) {
		t.Fatalf("err = %v, want ErrCanceled", err)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command:        "sh",
		Args:           []string{"-c", "printf 0123456789; printf abcdef >&2"},
		MaxOutputBytes: 4,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "0123" {
		t.Fatalf("Stdout = %q, want %q", got, "0123")
	}
	if got := string(res.Stderr); got != "abcd" {
		t.Fatalf("Stderr = %q, want %q", got, "abcd")
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}

func TestRunStdin(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "cat",
		Stdin:   []byte("piped input"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "piped input" {
		t.Fatalf("Stdout = %q, want %q", got, "piped input")
	}
}

func TestRunWorkingDir(t *testing.T) {
	requireShell(t)
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	if err := os.Mkdir(filepath.Join(root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s' "${PWD##*/}"`},
		Dir:     "work",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "work" {
		t.Fatalf("Stdout = %q, want %q", got, "work")
	}
}

func TestRunDoesNotUseShell(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	_, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "echo hello",
	})
	if err == nil {
		t.Fatal("expected an error: Command must not be shell-split")
	}
}

func TestRunRejectsEscapingDir(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})

	_, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "echo",
		Dir:     "..",
	})
	if !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("err = %v, want ErrPathEscape", err)
	}
}

func TestRunDefaultTimeout(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{DefaultTimeout: 100 * time.Millisecond})

	_, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", "sleep 5"},
	})
	if !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestEnvAllowlistFiltersInheritedAndExplicit(t *testing.T) {
	requireShell(t)
	t.Setenv("SBX_INHERITED", "inherited")
	t.Setenv("SBX_HIDDEN", "hidden")
	sb := newHost(t, sandbox.HostOptions{
		EnvAllowlist: []string{"PATH", "SBX_ALLOWED", "SBX_INHERITED"},
	})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s|%s|%s|%s' "$SBX_ALLOWED" "$SBX_INHERITED" "$SBX_HIDDEN" "$SBX_EXTRA"`},
		Env: map[string]string{
			"SBX_ALLOWED": "override",
			"SBX_EXTRA":   "extra",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "override|inherited||" {
		t.Fatalf("Stdout = %q, want %q", got, "override|inherited||")
	}
}

func TestEnvDenyWins(t *testing.T) {
	requireShell(t)
	t.Setenv("SBX_DENIED", "inherited")
	sb := newHost(t, sandbox.HostOptions{
		NoInheritEnv: true,
		EnvAllowlist: []string{"SBX_DENIED", "SBX_KEPT"},
		EnvDeny:      []string{"SBX_DENIED"},
	})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s|%s' "$SBX_DENIED" "$SBX_KEPT"`},
		Env: map[string]string{
			"SBX_DENIED": "explicit",
			"SBX_KEPT":   "kept",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "|kept" {
		t.Fatalf("Stdout = %q, want %q", got, "|kept")
	}
}

func TestEnvAllowlistPrefixWildcard(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{
		NoInheritEnv: true,
		EnvAllowlist: []string{"SBX_*"},
	})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s|%s' "$SBX_ONE" "$OTHER"`},
		Env: map[string]string{
			"SBX_ONE": "one",
			"OTHER":   "other",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "one|" {
		t.Fatalf("Stdout = %q, want %q", got, "one|")
	}
}

func TestNoInheritEnv(t *testing.T) {
	requireShell(t)
	t.Setenv("SBX_INHERITED", "inherited")
	sb := newHost(t, sandbox.HostOptions{NoInheritEnv: true})

	res, err := sb.Run(context.Background(), sandbox.ExecSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s|%s' "$SBX_EXPLICIT" "$SBX_INHERITED"`},
		Env:     map[string]string{"SBX_EXPLICIT": "explicit"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "explicit|" {
		t.Fatalf("Stdout = %q, want %q", got, "explicit|")
	}
}

func TestFileRoundTrip(t *testing.T) {
	sb := newHost(t, sandbox.HostOptions{})
	ctx := context.Background()

	if err := sb.WriteFile(ctx, "sub/dir/a.txt", []byte("content"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := sb.ReadFile(ctx, "sub/dir/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("ReadFile = %q, want content", data)
	}
	info, err := sb.Stat(ctx, "sub/dir/a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "a.txt" || info.Size != int64(len("content")) || info.IsDir {
		t.Fatalf("Stat = %+v", info)
	}
	if info.Mode.Perm() != 0o600 {
		t.Fatalf("Mode = %v, want 0600", info.Mode.Perm())
	}
	if info.ModTime.IsZero() {
		t.Fatal("ModTime is zero")
	}

	dirInfo, err := sb.Stat(ctx, "sub")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !dirInfo.IsDir {
		t.Fatal("Stat(sub).IsDir = false, want true")
	}

	absInside := filepath.Join(sb.Root(), "abs.txt")
	if err := sb.WriteFile(ctx, absInside, []byte("abs"), 0o644); err != nil {
		t.Fatalf("WriteFile absolute inside root: %v", err)
	}
	if data, err := sb.ReadFile(ctx, absInside); err != nil || string(data) != "abs" {
		t.Fatalf("ReadFile absolute inside root = %q, %v", data, err)
	}
}

func TestFileMissing(t *testing.T) {
	sb := newHost(t, sandbox.HostOptions{})
	ctx := context.Background()

	if _, err := sb.ReadFile(ctx, "nope.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile err = %v, want fs.ErrNotExist", err)
	}
	if _, err := sb.Stat(ctx, "nope.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat err = %v, want fs.ErrNotExist", err)
	}
}

func TestRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	ctx := context.Background()

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	escapePaths := []string{
		"../secret.txt",
		"../../etc/passwd",
		"sub/../../escape.txt",
		outsideFile,
	}
	for _, p := range escapePaths {
		if _, err := sb.ReadFile(ctx, p); !errors.Is(err, sandbox.ErrPathEscape) {
			t.Fatalf("ReadFile(%q) err = %v, want ErrPathEscape", p, err)
		}
	}
	if err := sb.WriteFile(ctx, "../evil.txt", []byte("evil"), 0o644); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("WriteFile escape err = %v, want ErrPathEscape", err)
	}
	if _, err := sb.Stat(ctx, "../"); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("Stat escape err = %v, want ErrPathEscape", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("evil.txt was created outside the root: %v", err)
	}
}

func TestRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	ctx := context.Background()

	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := sb.ReadFile(ctx, "link/secret.txt"); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("ReadFile through symlink err = %v, want ErrPathEscape", err)
	}
	if err := sb.WriteFile(ctx, "link/new.txt", []byte("new"), 0o644); !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("WriteFile through symlink err = %v, want ErrPathEscape", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "new.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file was created outside the root: %v", err)
	}
}

func TestRejectsDanglingSymlinkWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	ctx := context.Background()

	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "created.txt")
	if err := os.Symlink(target, filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}

	err := sb.WriteFile(ctx, "dangling", []byte("escape"), 0o644)
	if !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("WriteFile err = %v, want ErrPathEscape", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file was created outside the root: %v", err)
	}
}

func TestSymlinkInsideRootAllowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	sb := newHost(t, sandbox.HostOptions{Root: root})
	ctx := context.Background()
	if err := sb.WriteFile(ctx, "real/a.txt", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}

	data, err := sb.ReadFile(ctx, "alias/a.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("ReadFile = %q, want ok", data)
	}
}

func TestRunInvalidSpec(t *testing.T) {
	requireShell(t)
	sb := newHost(t, sandbox.HostOptions{})
	ctx := context.Background()

	if _, err := sb.Run(ctx, sandbox.ExecSpec{}); !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("empty command err = %v, want ErrInvalidSpec", err)
	}
	if _, err := sb.Run(ctx, sandbox.ExecSpec{Command: "echo", Timeout: -time.Second}); !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("negative timeout err = %v, want ErrInvalidSpec", err)
	}
	_, err := sb.Run(ctx, sandbox.ExecSpec{
		Command: "echo",
		Env:     map[string]string{"A=B": "x"},
	})
	if !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("invalid env err = %v, want ErrInvalidSpec", err)
	}
}
