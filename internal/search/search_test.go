package search

import (
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/config"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/index"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
)

func setupIndex(t *testing.T) *index.Index {
	t.Helper()
	idx := index.New()
	docs := []model.Document{
		{ID: "d1", Fields: map[string]string{"title": "Quick Brown Fox", "body": "a fast animal"}},
		{ID: "d2", Fields: map[string]string{"title": "Lazy Dog Days", "body": "the quick brown fox jumps"}},
		{ID: "d3", Fields: map[string]string{"title": "Cats Rule", "body": "nothing relevant"}},
	}
	for _, d := range docs {
		idx.Apply(&d, "")
	}
	return idx
}

func cfg() config.Config {
	c := config.Default()
	c.FieldWeights = map[string]float64{"title": 3}
	c.MaxEditDistance = 1
	return c
}

func ids(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.DocID
	}
	return out
}

func TestExactTermAndWeight(t *testing.T) {
	idx := setupIndex(t)
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("quick")
	hits := e.Search(n)
	got := ids(hits)
	// d1 在 title（权重3）命中；d2 在 body（权重1）命中 => d1 优先
	if len(got) != 2 || got[0] != "d1" || got[1] != "d2" {
		t.Fatalf("hits = %v", got)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("weighted title should score higher: %+v", hits)
	}
}

func TestFuzzyRankedBelowExact(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "exact", Fields: map[string]string{"body": "hello"}}, "")
	idx.Apply(&model.Document{ID: "fuzzy1", Fields: map[string]string{"body": "hallo"}}, "")
	idx.Apply(&model.Document{ID: "fuzzy2", Fields: map[string]string{"body": "hxllo world"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("hello")
	hits := e.Search(n)
	got := ids(hits)
	if len(got) != 3 || got[0] != "exact" {
		t.Fatalf("exact must rank first, got %v", got)
	}
	if hits[1].Score <= hits[2].Score {
		// 两个距离1的近似命中得分应相等，按 docID 排序 => fuzzy1 < fuzzy2
	}
	if got[1] != "fuzzy1" || got[2] != "fuzzy2" {
		t.Fatalf("tie must break by docID, got %v", got)
	}
}

func TestFuzzyDisabledByConfig(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "hallo"}}, "")
	c := cfg()
	c.MaxEditDistance = 0
	e := NewEngine(idx.Current(), c)
	n, _ := query.Parse("hello")
	if len(e.Search(n)) != 0 {
		t.Fatal("no fuzzy match allowed when max distance is 0")
	}
	// 显式 ~ 也不能突破配置上限
	n, _ = query.Parse("hello~")
	if len(e.Search(n)) != 0 {
		t.Fatal("~ must not override config limit 0")
	}
}

func TestFieldQualified(t *testing.T) {
	idx := setupIndex(t)
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("title:quick")
	hits := e.Search(n)
	if got := ids(hits); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("only d1 has quick in title, got %v", got)
	}
}

func TestBooleanAndNot(t *testing.T) {
	idx := setupIndex(t)
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("quick AND fox")
	if got := ids(e.Search(n)); len(got) != 2 {
		t.Fatalf("both docs contain quick and fox: %v", got)
	}
	n, _ = query.Parse("quick AND NOT title:quick")
	if got := ids(e.Search(n)); len(got) != 1 || got[0] != "d2" {
		t.Fatalf("only d2 matches quick but not in title: %v", got)
	}
	n, _ = query.Parse("quick OR cats")
	if got := ids(e.Search(n)); len(got) != 3 {
		t.Fatalf("OR should return 3 docs: %v", got)
	}
}

func TestPhrase(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "inorder", Fields: map[string]string{"body": "the quick brown fox"}}, "")
	idx.Apply(&model.Document{ID: "scattered", Fields: map[string]string{"body": "brown and quick fox"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse(`"quick brown"`)
	hits := e.Search(n)
	if got := ids(hits); len(got) != 1 || got[0] != "inorder" {
		t.Fatalf("phrase only matches adjacent tokens, got %v", got)
	}
}

func TestPhraseWithFuzzy(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "quikk brown fox"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse(`"quick brown"`)
	hits := e.Search(n)
	if len(hits) != 1 {
		t.Fatalf("fuzzy phrase should match, got %v", ids(hits))
	}
}

func TestStableOrdering(t *testing.T) {
	idx := setupIndex(t)
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("quick OR fox OR dog OR cats")
	first := e.Search(n)
	for i := 0; i < 10; i++ {
		again := e.Search(n)
		if len(again) != len(first) {
			t.Fatal("unstable length")
		}
		for j := range first {
			if again[j].DocID != first[j].DocID || again[j].Score != first[j].Score {
				t.Fatalf("unstable result at run %d: %v vs %v", i, ids(again), ids(first))
			}
		}
	}
}
