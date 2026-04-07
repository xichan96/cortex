package verify

import "strings"

func TextContains(text, needle string) (ok bool, reason string) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return true, ""
	}
	if !strings.Contains(text, needle) {
		return false, "text does not contain required substring"
	}
	return true, ""
}

func OptionalSubstring(text, sub string) bool {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return true
	}
	return strings.Contains(text, sub)
}
