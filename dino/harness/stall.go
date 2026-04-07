package harness

import (
	"hash/fnv"
	"strings"
)

type StallSample struct {
	Primary   string
	Secondary string
}

func TextFingerprint(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func StallSampleEmpty(v StallSample, requireSecondary bool) bool {
	if strings.TrimSpace(v.Primary) != "" {
		return false
	}
	if requireSecondary && strings.TrimSpace(v.Secondary) != "" {
		return false
	}
	return true
}

func StallAccumulate(ring *[][2]uint64, v StallSample, stallN int, requireSecondary bool) bool {
	if StallSampleEmpty(v, requireSecondary) {
		return false
	}
	t := TextFingerprint(v.Primary)
	a := TextFingerprint(v.Secondary)
	*ring = append(*ring, [2]uint64{t, a})
	if len(*ring) > stallN {
		*ring = (*ring)[len(*ring)-stallN:]
	}
	if len(*ring) < stallN {
		return false
	}
	first := (*ring)[0]
	for i := 1; i < len(*ring); i++ {
		if (*ring)[i] != first {
			return false
		}
	}
	return true
}
