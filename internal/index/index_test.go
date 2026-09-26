package index

import (
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
)

func TestAddQueryAndDelete(t *testing.T) {
	idx := New()
	old := idx.Current()
	idx.Apply(&model.Document{ID: "d1", Fields: map[string]string{"title": "Hello World"}}, "")
	s := idx.Current()
	if got := s.Postings("title", "hello"); len(got) != 1 || got[0].DocID != "d1" {
		t.Fatalf("posting wrong: %+v", got)
	}
	// 旧快照不可变
	if old.Has("d1") {
		t.Fatal("old snapshot must remain unchanged")
	}
	// 覆盖更新
	idx.Apply(&model.Document{ID: "d1", Fields: map[string]string{"title": "Goodbye"}}, "")
	s = idx.Current()
	if len(s.Postings("title", "hello")) != 0 {
		t.Fatal("old term must be removed on overwrite")
	}
	if len(s.Postings("title", "goodbye")) != 1 {
		t.Fatal("new term must be indexed")
	}
	if len(s.DocumentIDs()) != 1 {
		t.Fatal("overwrite must not duplicate doc")
	}
	// 删除
	idx.Apply(nil, "d1")
	if idx.Current().Has("d1") {
		t.Fatal("delete failed")
	}
}

func TestOrderStable(t *testing.T) {
	idx := New()
	for _, id := range []string{"b", "a", "c"} {
		idx.Apply(&model.Document{ID: id, Fields: map[string]string{"b": "x"}}, "")
	}
	got := idx.Current().DocumentIDs()
	want := []string{"b", "a", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
