package verify

import "strings"

func ParseYesNoAnswer(reply string) (ok bool, reason string) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return false, "empty model reply"
	}
	firstLine := strings.TrimSpace(strings.Split(reply, "\n")[0])
	lower := strings.ToLower(firstLine)
	if strings.HasPrefix(lower, "yes") && (len(lower) == 3 || !isASCIILetter(lower[3])) {
		return true, ""
	}
	if strings.HasPrefix(lower, "no") && noWordBoundary(lower) {
		rest := strings.TrimSpace(firstLine[2:])
		rest = strings.TrimLeft(rest, ":,.-) ")
		if rest != "" {
			return false, rest
		}
		if idx := strings.Index(reply, "\n"); idx >= 0 {
			return false, strings.TrimSpace(reply[idx+1:])
		}
		return false, ""
	}
	return false, reply
}

func noWordBoundary(lower string) bool {
	if len(lower) == 2 {
		return true
	}
	if len(lower) < 3 {
		return false
	}
	c := lower[2]
	return c == ' ' || c == ':' || c == ',' || c == '.' || c == '-' || c == ')'
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
