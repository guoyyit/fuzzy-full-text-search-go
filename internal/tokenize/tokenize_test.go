package tokenize

import "testing"

func TestTokenize(t *testing.T) {
	toks := Tokenize("Hello, World! 你好 world123")
	want := []Token{
		{Text: "hello", Start: 0, End: 5},
		{Text: "world", Start: 7, End: 12},
		{Text: "你好", Start: 14, End: 20},
		{Text: "world123", Start: 21, End: 29},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(toks), len(want), toks)
	}
	for i := range want {
		if toks[i] != want[i] {
			t.Fatalf("token %d = %+v, want %+v", i, toks[i], want[i])
		}
	}
}
