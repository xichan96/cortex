package utils

import "strings"

func PageKeywordScore(query, title, text string) float64 {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return 1
	}
	hay := strings.ToLower(title + " " + text)
	toks := strings.Fields(q)
	if len(toks) == 0 {
		return 0
	}
	var hit int
	for _, t := range toks {
		if strings.Contains(hay, t) {
			hit++
		}
	}
	return float64(hit) / float64(len(toks))
}

func KindAllowed(kind string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if f == kind {
			return true
		}
	}
	return false
}
