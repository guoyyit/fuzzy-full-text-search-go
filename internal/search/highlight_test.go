package search

import (
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/index"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
)

func TestHighlightExact(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "hello world hello"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("hello")
	hl := e.Highlight(n, "d")
	rs := hl["body"]
	want := []Range{{Start: 0, End: 5}, {Start: 12, End: 17}}
	if len(rs) != len(want) {
		t.Fatalf("ranges = %+v", rs)
	}
	for i := range want {
		if rs[i] != want[i] {
			t.Fatalf("range %d = %+v want %+v", i, rs[i], want[i])
		}
	}
	// 区间必须可安全切片
	text := "hello world hello"
	for _, r := range rs {
		if r.Start < 0 || r.End > len(text) || r.Start >= r.End {
			t.Fatalf("range out of bounds: %+v", r)
		}
		_ = text[r.Start:r.End]
	}
}

func TestHighlightFuzzyPointsAtDocToken(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "a hallo there"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("hello")
	hl := e.Highlight(n, "d")
	rs := hl["body"]
	if len(rs) != 1 {
		t.Fatalf("want single fuzzy range, got %+v", rs)
	}
	// 必须标注文档中的实际词条 "hallo"（偏移 2..7），而不是查询词。
	text := "a hallo there"
	if got := text[rs[0].Start:rs[0].End]; got != "hallo" {
		t.Fatalf("highlighted slice = %q want hallo; range %+v", got, rs[0])
	}
}

func TestHighlightPhraseCoversEachToken(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "the quikk brown fox"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse(`"quick brown"`)
	hl := e.Highlight(n, "d")
	rs := hl["body"]
	text := "the quikk brown fox"
	wantWords := []string{"quikk", "brown"}
	if len(rs) != 2 {
		t.Fatalf("want 2 ranges, got %+v", rs)
	}
	for i, w := range wantWords {
		if text[rs[i].Start:rs[i].End] != w {
			t.Fatalf("range %d points to %q want %q", i, text[rs[i].Start:rs[i].End], w)
		}
	}
}

func TestHighlightUnicodeOffsets(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"body": "你好世界 golang"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("golang")
	rs := e.Highlight(n, "d")["body"]
	text := "你好世界 golang"
	if len(rs) != 1 || text[rs[0].Start:rs[0].End] != "golang" {
		t.Fatalf("unicode-safe offsets wrong: %+v", rs)
	}
}

func TestHighlightBooleanOnlyPositiveClauses(t *testing.T) {
	idx := index.New()
	idx.Apply(&model.Document{ID: "d", Fields: map[string]string{"title": "fox", "body": "dog"}}, "")
	e := NewEngine(idx.Current(), cfg())
	n, _ := query.Parse("title:fox AND NOT body:cat")
	hl := e.Highlight(n, "d")
	if len(hl["title"]) != 1 {
		t.Fatalf("positive clause must be highlighted: %+v", hl)
	}
	if _, ok := hl["body"]; ok {
		t.Fatalf("negative clause must not produce highlights: %+v", hl)
	}
}
