package sql

import (
	"unicode/utf8"

	"github.com/xichan96/cortex/pkg/sets"
)

var (
	PercentSignUTF8, _ = utf8.DecodeRuneInString("%")
	UnderscoreUTF8, _  = utf8.DecodeRuneInString("_")
	EscapeCharUTF8, _  = utf8.DecodeRuneInString(`\`)
)

var escapeChars = sets.NewSet(PercentSignUTF8, UnderscoreUTF8, EscapeCharUTF8)

func EscapeLikeString(s string) string {
	buf := make([]rune, 0)
	for _, r := range s {
		if escapeChars.Has(r) {
			buf = append(buf, EscapeCharUTF8)
		}
		buf = append(buf, r)
	}
	return string(buf)
}

func PrefixSearch(s string) string {
	return EscapeLikeString(s) + "%"
}

func SuffixSearch(s string) string {
	return "%" + EscapeLikeString(s)
}

func FuzzySearch(s string) string {
	return "%" + EscapeLikeString(s) + "%"
}
