package search

import (
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/index"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
)

func TestAndScoresAcrossFields(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{
		"title": "quick",
		"body":  "fox runs",
	}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("title:quick AND body:fox")
	hits := e.Search(n)
	if len(hits) != 1 {
		t.Fatalf("expected hit, got %+v", hits)
	}
	// title 权重 3 + body 权重 1 = 4
	if got := hits[0].Score; got != 4.0 {
		t.Fatalf("AND score should sum fields: got %v", got)
	}
	if len(hits[0].Fields) != 2 {
		t.Fatalf("both fields should contribute: %+v", hits[0].Fields)
	}
}

func TestOrSameFieldRangesAndScore(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{
		"body": "cat and dog",
	}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("cat OR dog")
	hits := e.Search(n)
	if len(hits) != 1 {
		t.Fatalf("expected one doc, got %+v", hits)
	}
	fh := hits[0].Fields["body"]
	// 同字段两个词都应被高亮（区间并集，2 段）
	if len(fh.Ranges) != 2 {
		t.Fatalf("both OR terms in same field must be highlighted: %+v", fh.Ranges)
	}
}

func TestFuzzyPenaltyOrderingAcrossDistances(t *testing.T) {
	idx := index.New()
	// max edit distance 允许 2
	idx.Apply(&model.Document{ID: "d0", Fields: map[string]string{"body": "abc"}}, "")
	idx.Apply(&model.Document{ID: "d1", Fields: map[string]string{"body": "abd"}}, "") // dist 1
	c := cfg()
	c.MaxEditDistance = 2
	e := NewEngine(idx.Current(), c)
	n, _ := query.Parse("abc")
	hits := e.Search(n)
	if len(hits) != 2 || hits[0].DocID != "d0" {
		t.Fatalf("exact first: %+v", hits)
	}
	if !(hits[0].Score > hits[1].Score) {
		t.Fatalf("distance must lower score: %v vs %v", hits[0].Score, hits[1].Score)
	}
}

func TestForcedExactMarker(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "hallo"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("hello$")
	if len(e.Search(n)) != 0 {
		t.Fatal("$ must force exact even when fuzzy enabled")
	}
}

func TestFieldWeightAffectsOrder(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "inBody", Fields: map[string]string{"body": "term"}}, "")
	idx.Apply(&model.Document{ID: "inTitle", Fields: map[string]string{"title": "term"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("term")
	hits := e.Search(n)
	if len(hits) != 2 || hits[0].DocID != "inTitle" {
		t.Fatalf("high-weight field must rank first: %+v", hits)
	}
	if hits[0].Score != 3.0 || hits[1].Score != 1.0 {
		t.Fatalf("weights wrong: %v %v", hits[0].Score, hits[1].Score)
	}
}
