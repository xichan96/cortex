package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkTextFingerprint(b *testing.B) {
	s := strings.Repeat("hello ", 256)
	b.ResetTimer()
	for b.Loop() {
		TextFingerprint(s)
	}
}

func BenchmarkStallAccumulate(b *testing.B) {
	v := StallSample{Primary: "same"}
	var ring [][2]uint64
	b.ResetTimer()
	for b.Loop() {
		StallAccumulate(&ring, v, 3, false)
		ring = ring[:0]
	}
}

func BenchmarkDirTrackedFilesFingerprint(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "progress"), []byte("ok"), 0644); err != nil {
		b.Fatal(err)
	}
	names := []string{"progress"}
	b.ResetTimer()
	for b.Loop() {
		_, _ = DirTrackedFilesFingerprint(dir, names)
	}
}

func BenchmarkRunOuterLoopCompletedFirstTurn(b *testing.B) {
	ctx := context.Background()
	outer, retry := 0, 0
	prog := &OuterProgress{OuterIteration: &outer, VerifyRetryCount: &retry}
	p := OuterParams{MaxOuterIterations: 8, StallWindow: 3}
	b.ResetTimer()
	for b.Loop() {
		outer = 0
		retry = 0
		_, _, _ = RunOuterLoop(ctx, p, prog,
			func(context.Context) (*int, *OuterOutcome, bool, error) {
				v := 1
				return &v, nil, true, nil
			},
			func(*int) StallSample { return StallSample{Primary: "x"} },
			func(context.Context, *int) (bool, string) { return true, "" },
			func(*int) bool { return true },
			func(context.Context, string) error { return nil },
		)
	}
}
