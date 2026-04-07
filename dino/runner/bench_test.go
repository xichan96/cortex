package runner

import (
	"strings"
	"testing"

	"github.com/xichan96/cortex/dino/harness"
)

func BenchmarkTextFingerprint(b *testing.B) {
	s := strings.Repeat("hello ", 256)
	for b.Loop() {
		harness.TextFingerprint(s)
	}
}

func BenchmarkStallAccumulate(b *testing.B) {
	v := harness.StallSample{Primary: "same"}
	var ring [][2]uint64
	for b.Loop() {
		harness.StallAccumulate(&ring, v, 3, false)
		ring = ring[:0]
	}
}
