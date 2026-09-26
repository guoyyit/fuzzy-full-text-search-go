package fuzzsearch

import (
	"strings"
	"testing"
)

func TestAnalyze(t *testing.T) {
	toks := analyze("Hello, World! hello")
	want := []Token{
		{"hello", 0, 5},
		{"world", 7, 12},
		{"hello", 14, 19},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(toks), len(want), toks)
	}
	for i := range want {
		if toks[i] != want[i] {
			t.Errorf("token %d = %+v, want %+v", i, toks[i], want[i])
		}
	}
}

func TestAnalyzeOffsets(t *testing.T) {
	text := "Quick Brown"
	for _, tok := range analyze(text) {
		got := strings.ToLower(text[tok.Start:tok.End])
		if got != tok.Term {
			t.Errorf("offset mismatch for %q: substring %q", tok.Term, got)
		}
	}
}

func TestAnalyzeMultibyte(t *testing.T) {
	// "你好 world" — multibyte token then ASCII token; byte offsets must
	// still slice the original string exactly.
	text := "你好 world"
	toks := analyze(text)
	if len(toks) != 2 {
		t.Fatalf("got %d tokens: %+v", len(toks), toks)
	}
	if toks[0].Term != "你好" || text[toks[0].Start:toks[0].End] != "你好" {
		t.Errorf("first token %+v", toks[0])
	}
	if toks[1].Term != "world" || text[toks[1].Start:toks[1].End] != "world" {
		t.Errorf("second token %+v", toks[1])
	}
}
