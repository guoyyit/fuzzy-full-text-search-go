package tokenize

import (
	"strings"
	"unicode"
)

// Token 是分词结果，Start/End 为该词条在原文字符串中的字节区间（左闭右开）。
// 偏移按字节计数，可直接用于对原始字符串做切片高亮。
type Token struct {
	Text  string
	Start int
	End   int
}

// Tokenize 对文本做小写化分词，并记录每个词条的精确字节偏移。
// 词条由 Unicode 字母或数字构成；其余字符（空白、标点等）均为分隔符。
func Tokenize(text string) []Token {
	var toks []Token
	start := -1
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			toks = append(toks, Token{Text: strings.ToLower(text[start:i]), Start: start, End: i})
			start = -1
		}
	}
	if start != -1 {
		toks = append(toks, Token{Text: strings.ToLower(text[start:]), Start: start, End: len(text)})
	}
	return toks
}
