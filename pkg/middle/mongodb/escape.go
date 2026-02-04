// Package mongodb
package mongodb

import (
	"unicode/utf8"
)

// 一些转义符
var (
	PercentSignUTF8, _      = utf8.DecodeRuneInString("%")
	UnderscoreUTF8, _       = utf8.DecodeRuneInString("_")
	EscapeCharUTF8, _       = utf8.DecodeRuneInString(`\`)
	LeftSquareCharUTF8, _   = utf8.DecodeRuneInString(`[`)
	RightSquareCharUTF8, _  = utf8.DecodeRuneInString(`]`)
	LeftBracketCharUTF8, _  = utf8.DecodeRuneInString(`(`)
	RightBracketCharUTF8, _ = utf8.DecodeRuneInString(`)`)
	AsteriskCharUTF8, _     = utf8.DecodeRuneInString(`*`)
	DotCharUTF8, _          = utf8.DecodeRuneInString(`.`)
	DollarCharUTF8, _       = utf8.DecodeRuneInString(`$`)
	TipCharUTF8, _          = utf8.DecodeRuneInString(`^`)
	QMCharUTF8, _           = utf8.DecodeRuneInString(`?`)
)

// EscapeLikeString replace掉正则查询的字符
func EscapeLikeString(s string) string {
	/*
		'%'： 37
		'\'： 92
		'_': 95
	*/

	buf := make([]rune, 0)

	for _, r := range s {
		if r == PercentSignUTF8 || r == UnderscoreUTF8 || r == EscapeCharUTF8 || r == LeftSquareCharUTF8 ||
			r == RightSquareCharUTF8 || r == LeftBracketCharUTF8 || r == RightBracketCharUTF8 ||
			r == AsteriskCharUTF8 || r == DotCharUTF8 || r == DollarCharUTF8 || r == TipCharUTF8 || r == QMCharUTF8 {
			continue
		}
		buf = append(buf, r)

	}
	return string(buf)
}
