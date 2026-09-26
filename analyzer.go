package fuzzsearch

import (
	"strings"
	"unicode"
)

// Token is an occurrence of a normalized term inside a field value.
// Start/End are byte offsets of the token in the ORIGINAL field string;
// Term is the lowercased text of [Start, End). Highlighting always uses
// the original substring [Start, End), never the normalized term, so
// multi-byte case folding can never shift a highlight boundary.
type Token struct {
	Term  string
	Start int
	End   int
}

// analyze lowercases and splits text into Unicode word tokens
// (maximal runs of letters/digits), recording each token's byte offsets.
func analyze(text string) []Token {
	toks := make([]Token, 0)
	start := -1
	for i, r := range text {
		isWord := unicode.IsLetter(r) || unicode.IsDigit(r)
		if isWord && start == -1 {
			start = i
		} else if !isWord && start != -1 {
			toks = append(toks, Token{
				Term:  strings.ToLower(text[start:i]),
				Start: start,
				End:   i,
			})
			start = -1
		}
	}
	if start != -1 {
		toks = append(toks, Token{
			Term:  strings.ToLower(text[start:]),
			Start: start,
			End:   len(text),
		})
	}
	return toks
}
