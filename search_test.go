package fuzzsearch

import (
	"encoding/json"
	"testing"
)

func newTestService(t *testing.T, cfg Config) *Service {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	if cfg.FieldWeights == nil {
		cfg.FieldWeights = map[string]float64{"title": 3, "body": 1}
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustUpsert(t *testing.T, s *Service, id, title, body string) {
	t.Helper()
	if err := s.Upsert(Document{ID: id, Fields: map[string]string{"title": title, "body": body}}); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
}

func hitIDs(r *SearchResult) []string {
	ids := make([]string, len(r.Hits))
	for i, h := range r.Hits {
		ids[i] = h.DocID
	}
	return ids
}

func findHit(r *SearchResult, id string) *Hit {
	for i := range r.Hits {
		if r.Hits[i].DocID == id {
			return &r.Hits[i]
		}
	}
	return nil
}

func TestFuzzySearch(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights:           map[string]float64{"body": 1},
		DefaultMaxEditDistance: 2,
		FuzzyPenalty:           0.5,
		DataDir:                t.TempDir(),
	})
	mustUpsert(t, s, "d0", "x", "help please") // exact "help"
	mustUpsert(t, s, "d1", "x", "hello world")
	mustUpsert(t, s, "d3", "x", "halp galaxy") // "halp" is edit distance 1 from "help"
	mustUpsert(t, s, "d4", "x", "xxxx galaxy") // distance > 2 from "help"

	// Bare ~ uses default cap 2; matches at edit distance 0,1,2 in that
	// rank order: "help"(d0), "halp"(d3, d=1), "hello"(d1, d=2).
	r, err := s.Search("help~")
	if err != nil {
		t.Fatal(err)
	}
	ids := hitIDs(r)
	want := []string{"d0", "d3", "d1"}
	if !eqStrings(ids, want) {
		t.Fatalf("fuzzy hits = %v, want %v", ids, want)
	}
	if !(r.Hits[0].Score > r.Hits[1].Score && r.Hits[1].Score > r.Hits[2].Score) {
		t.Errorf("scores must decrease with edit distance: %v %v %v",
			r.Hits[0].Score, r.Hits[1].Score, r.Hits[2].Score)
	}
	// Approximate match must carry its edit distance in the highlight.
	if h := findHit(r, "d3"); h != nil {
		found := false
		for _, hl := range h.Highlights {
			if hl.Term == "halp" && hl.EditDistance == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("fuzzy highlight missing, got %+v", h.Highlights)
		}
	}

	// Per-query cap 1: "xelpo" is distance 2 from "help", must not match at ~1.
	r, err = s.Search("xelpo~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 0 {
		t.Errorf("distance 2 matched under cap 1: %v", hitIDs(r))
	}
	// Same term matches at cap 2.
	r, err = s.Search("xelpo~2")
	if err != nil {
		t.Fatal(err)
	}
	if findHit(r, "d0") == nil {
		t.Errorf("distance 2 should match under cap 2: %v", hitIDs(r))
	}

	// No ~ means exact only: misspelled term matches nothing.
	r, _ = s.Search("halp")
	if len(r.Hits) != 1 || hitIDs(r)[0] != "d3" {
		t.Errorf("exact-only search wrong: %v", hitIDs(r))
	}
}

func TestFuzzyCapClamped(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights:           map[string]float64{"body": 1},
		DefaultMaxEditDistance: 1,
		DataDir:                t.TempDir(),
	})
	mustUpsert(t, s, "d1", "x", "help")
	// ~5 must be clamped to the configured default 1.
	r, err := s.Search("xelpo~5") // distance 2
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 0 {
		t.Errorf("~5 must be clamped to default 1, but matched %v", hitIDs(r))
	}
}

func TestFieldWeights(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights: map[string]float64{"title": 5, "body": 1},
		DataDir:      t.TempDir(),
	})
	mustUpsert(t, s, "a", "go", "unrelated")
	mustUpsert(t, s, "b", "unrelated", "go go")
	r, err := s.Search("go")
	if err != nil {
		t.Fatal(err)
	}
	if ids := hitIDs(r); len(ids) != 2 || ids[0] != "a" {
		t.Fatalf("high-weight title must rank first, got %v", ids)
	}
	// Explain exposes per-field contribution.
	h := findHit(r, "a")
	if h.Explain["title"] <= 0 || h.Explain["body"] != 0 {
		t.Errorf("explain wrong: %+v", h.Explain)
	}
}

func TestPhraseSearch(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights:           map[string]float64{"body": 1},
		DefaultMaxEditDistance: 2,
		DataDir:                t.TempDir(),
	})
	mustUpsert(t, s, "a", "x", "the quick brown fox")
	mustUpsert(t, s, "b", "x", "quick the brown")

	r, err := s.Search(`"quick brown"`)
	if err != nil {
		t.Fatal(err)
	}
	if ids := hitIDs(r); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("exact phrase = %v, want [a]", ids)
	}
	// Exact-mode typo does not match.
	r, err = s.Search(`"quikc brown"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 0 {
		t.Errorf("non-fuzzy typo phrase matched: %v", hitIDs(r))
	}
	// Fuzzy phrase matches at edit distance 2 ("quikc" -> "quick").
	r, err = s.Search(`"quikc brown"~2`)
	if err != nil {
		t.Fatal(err)
	}
	if ids := hitIDs(r); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("fuzzy phrase = %v, want [a]", ids)
	}
}

func TestBooleanSearch(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	})
	mustUpsert(t, s, "a", "x", "alpha")
	mustUpsert(t, s, "b", "x", "beta gamma")
	mustUpsert(t, s, "c", "x", "alpha gamma")
	mustUpsert(t, s, "d", "x", "delta")

	cases := []struct {
		q    string
		want []string
	}{
		{"alpha AND gamma", []string{"c"}},
		{"alpha OR beta", []string{"a", "b", "c"}},
		{"alpha beta", []string{"a", "b", "c"}}, // implicit OR
		{"alpha AND NOT gamma", []string{"a"}},
		{"(alpha OR beta) AND gamma", []string{"b", "c"}},
		{"alpha && gamma", []string{"c"}},
		{"alpha || beta", []string{"a", "b", "c"}},
		{"NOT gamma", []string{"a", "d"}},
		{"body:alpha", []string{"a", "c"}},
		{"body:(alpha OR delta)", []string{"a", "c", "d"}},
	}
	for _, tc := range cases {
		r, err := s.Search(tc.q)
		if err != nil {
			t.Errorf("%q: %v", tc.q, err)
			continue
		}
		if ids := hitIDs(r); !eqStrings(ids, tc.want) {
			t.Errorf("%q = %v, want %v", tc.q, ids, tc.want)
		}
	}
}

func TestFieldQualifiedUnknown(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	})
	mustUpsert(t, s, "a", "x", "hello")
	r, err := s.Search("nope:hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 0 {
		t.Errorf("unknown field qualifier must match nothing, got %v", hitIDs(r))
	}
}

func TestDeterministicOrder(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights:           map[string]float64{"body": 1},
		DefaultMaxEditDistance: 1,
		DataDir:                t.TempDir(),
	})
	for _, id := range []string{"d1", "d2", "d3", "d4", "d5"} {
		mustUpsert(t, s, id, "x", "same term here")
	}
	var first string
	for i := 0; i < 10; i++ {
		r, err := s.Search("term~")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		if i == 0 {
			first = string(b)
		} else if string(b) != first {
			t.Fatalf("non-deterministic result on iteration %d:\n%s\n%s", i, first, b)
		}
	}
	// Ties are broken by document ID ascending.
	r, _ := s.Search("term~")
	if ids := hitIDs(r); !eqStrings(ids, []string{"d1", "d2", "d3", "d4", "d5"}) {
		t.Errorf("tie-breaking wrong: %v", ids)
	}
}

func TestIncrementalUpdate(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	})
	mustUpsert(t, s, "a", "x", "red apple")
	r, _ := s.Search("red")
	if len(r.Hits) != 1 {
		t.Fatal("new doc not visible")
	}
	// Overwrite update.
	mustUpsert(t, s, "a", "x", "green pear")
	r, _ = s.Search("red")
	if len(r.Hits) != 0 {
		t.Errorf("old term still found after update: %v", hitIDs(r))
	}
	r, _ = s.Search("green")
	if len(r.Hits) != 1 || hitIDs(r)[0] != "a" {
		t.Errorf("new term not found after update: %v", hitIDs(r))
	}
	// Delete.
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Search("green")
	if len(r.Hits) != 0 {
		t.Errorf("deleted doc still found: %v", hitIDs(r))
	}
	if _, ok := s.Get("a"); ok {
		t.Errorf("Get after delete must miss")
	}
	// Delete of missing ID is not an error.
	if err := s.Delete("ghost"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
	// Batch is atomic: one upsert + one delete together.
	mustUpsert(t, s, "b", "x", "banana")
	mustUpsert(t, s, "c", "x", "cherry")
	if err := s.Batch([]Mutation{
		{Doc: Document{ID: "b", Fields: map[string]string{"body": "blueberry"}}},
		{Delete: true, Doc: Document{ID: "c"}},
	}); err != nil {
		t.Fatal(err)
	}
	if r, _ = s.Search("banana"); len(r.Hits) != 0 {
		t.Errorf("batch: old b term survived")
	}
	if r, _ = s.Search("blueberry"); len(r.Hits) != 1 {
		t.Errorf("batch: new b term missing")
	}
	if r, _ = s.Search("cherry"); len(r.Hits) != 0 {
		t.Errorf("batch: deleted c survived")
	}
}

func TestHighlights(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights:           map[string]float64{"body": 1},
		DefaultMaxEditDistance: 1,
		DataDir:                t.TempDir(),
	})
	body := "Quack red fox" // "quack" is edit distance 1 from "quick"
	mustUpsert(t, s, "a", "x", body)
	r, err := s.Search("quick~ red")
	if err != nil {
		t.Fatal(err)
	}
	h := findHit(r, "a")
	if h == nil {
		t.Fatal("doc not matched")
	}
	if len(h.Highlights) != 2 {
		t.Fatalf("want 2 highlights, got %+v", h.Highlights)
	}
	for _, hl := range h.Highlights {
		if hl.Field != "body" || hl.Start < 0 || hl.End > len(body) || hl.Start >= hl.End {
			t.Fatalf("illegal interval %+v", hl)
		}
		// The interval must slice exactly the matched original text.
		got := body[hl.Start:hl.End]
		if hl.Term == "quack" {
			if got != "Quack" || hl.EditDistance != 1 {
				t.Errorf("fuzzy highlight wrong: %+v substring %q", hl, got)
			}
		} else if hl.Term == "red" {
			if got != "red" || hl.EditDistance != 0 {
				t.Errorf("exact highlight wrong: %+v substring %q", hl, got)
			}
		} else {
			t.Errorf("unexpected highlight %+v", hl)
		}
	}
}

func TestQueryParseErrors(t *testing.T) {
	s := newTestService(t, Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	})
	bad := []string{
		"",
		"   ",
		"(alpha",
		`"unterminated`,
		`""`,
		"term~x",
		":value",
		"alpha)",
	}
	for _, q := range bad {
		if _, err := s.Search(q); err == nil {
			t.Errorf("query %q should be rejected", q)
		}
	}
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
