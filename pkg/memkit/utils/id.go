package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d_fallback", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func RandomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		s := NewID()
		if len(s) < n {
			return s + strings.Repeat("0", n-len(s))
		}
		return s[:n]
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func EstimateTokens(text string) int {
	asciiCount := 0
	nonAsciiCount := 0
	for _, r := range text {
		if r < 128 {
			asciiCount++
		} else {
			nonAsciiCount++
		}
	}
	return (asciiCount / 4) + (nonAsciiCount * 2)
}
