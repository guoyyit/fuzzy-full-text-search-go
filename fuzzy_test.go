package fuzzsearch

import "testing"

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"gumbo", "gambol", 2},
		{"abc", "", 3},
		{"", "abc", 3},
		{"你好", "你坏", 1},
		{"hallo", "hello", 1},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestWithinEditDistance(t *testing.T) {
	if d, ok := withinEditDistance("kitten", "sitting", 2); ok {
		t.Errorf("expected reject at maxEd=2, got d=%d", d)
	}
	d, ok := withinEditDistance("kitten", "sitting", 3)
	if !ok || d != 3 {
		t.Errorf("expected accept d=3, got d=%d ok=%v", d, ok)
	}
	if d, ok := withinEditDistance("abc", "xyz", 1); ok {
		t.Errorf("expected reject, got d=%d", d)
	}
	if d, ok := withinEditDistance("abc", "abc", 0); !ok || d != 0 {
		t.Errorf("exact at 0: got d=%d ok=%v", d, ok)
	}
	if _, ok := withinEditDistance("abc", "abcd", 0); ok {
		t.Errorf("length difference should reject at maxEd=0")
	}
	if d, ok := withinEditDistance("abcdefgh", "abc", 3); ok {
		t.Errorf("length diff 5 should reject at 3, got %d", d)
	}
	// Rune-safe multibyte distance.
	if d, ok := withinEditDistance("你好世界", "你好", 2); !ok || d != 2 {
		t.Errorf("multibyte: got d=%d ok=%v", d, ok)
	}
}
