package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/config"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/search"
)

func testCfg() config.Config {
	c := config.Default()
	c.FieldWeights = map[string]float64{"title": 3}
	return c
}

func idsFromRes(r *QueryResult) []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, h.DocID)
	}
	return out
}

func TestUpsertDeleteVisible(t *testing.T) {
	s, err := Open(t.TempDir(), testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Upsert(model.Document{ID: "d1", Fields: map[string]string{"title": "hello world"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Query("hello")
	if got := idsFromRes(r); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("upsert not visible: %v", got)
	}
	// 覆盖：旧词条消失，新词条可见
	s.Upsert(model.Document{ID: "d1", Fields: map[string]string{"title": "goodbye"}})
	if r, _ := s.Query("hello"); len(r.Hits) != 0 {
		t.Fatal("old term must disappear after overwrite")
	}
	if r, _ := s.Query("goodbye"); len(r.Hits) != 1 {
		t.Fatal("new term must be searchable")
	}
	// 删除
	if err := s.Delete("d1"); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Query("goodbye"); len(r.Hits) != 0 {
		t.Fatal("deleted doc must not match")
	}
}

func TestRestartConsistency(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	s.Upsert(model.Document{ID: "a", Fields: map[string]string{"title": "alpha"}})
	s.Upsert(model.Document{ID: "b", Fields: map[string]string{"body": "beta"}})
	s.Upsert(model.Document{ID: "a", Fields: map[string]string{"title": "alphabet"}}) // 覆盖
	s.Delete("b")
	before, _ := s.Query("alphabet")
	if got := idsFromRes(before); len(got) != 1 || got[0] != "a" {
		t.Fatalf("before restart: %v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	after, err := s2.Query("alphabet")
	if err != nil {
		t.Fatal(err)
	}
	if got := idsFromRes(after); len(got) != 1 || got[0] != "a" {
		t.Fatalf("after restart mismatch: %v", got)
	}
	if r, _ := s2.Query("beta"); len(r.Hits) != 0 {
		t.Fatal("delete must survive restart")
	}
	if r, _ := s2.Query("alpha"); len(r.Hits) != 0 {
		t.Fatal("overwritten old content must not survive")
	}
}

func TestRestartKeepsResultsStable(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, testCfg())
	for i := 0; i < 20; i++ {
		s.Upsert(model.Document{
			ID:     fmt.Sprintf("d%02d", i),
			Fields: map[string]string{"title": fmt.Sprintf("title %d word", i), "body": "shared word body"},
		})
	}
	before, _ := s.Query("word")
	s.Close()
	s2, _ := Open(dir, testCfg())
	defer s2.Close()
	after, _ := s2.Query("word")
	if len(before.Hits) != len(after.Hits) {
		t.Fatalf("hit count differs: %d vs %d", len(before.Hits), len(after.Hits))
	}
	for i := range before.Hits {
		if before.Hits[i].DocID != after.Hits[i].DocID || before.Hits[i].Score != after.Hits[i].Score {
			t.Fatalf("result %d differs across restart", i)
		}
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	c := testCfg()
	c.MaxEditDistance = -1
	if _, err := Open(t.TempDir(), c); err == nil {
		t.Fatal("invalid config must be rejected at startup")
	}
}

func TestInvalidDocRejected(t *testing.T) {
	s, _ := Open(t.TempDir(), testCfg())
	defer s.Close()
	if err := s.Upsert(model.Document{ID: "", Fields: map[string]string{"a": "b"}}); err == nil {
		t.Fatal("empty doc id must be rejected")
	}
}

func TestConcurrentReadersSnapshotIsolation(t *testing.T) {
	s, _ := Open(t.TempDir(), testCfg())
	defer s.Close()
	for i := 0; i < 50; i++ {
		s.Upsert(model.Document{ID: fmt.Sprintf("doc%d", i), Fields: map[string]string{"body": fmt.Sprintf("content %d", i)}})
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	// 读者：在一次查询期间，结果必须自洽（数量与内部结构一致）。
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := s.Query("content")
				if err != nil {
					t.Errorf("query error: %v", err)
					return
				}
				for _, h := range res.Hits {
					if h.DocID == "" {
						t.Errorf("empty docid in hit")
					}
				}
			}
		}()
	}
	// 写者：持续覆盖与删除/重插。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 50; i < 200; i++ {
			id := fmt.Sprintf("doc%d", i%60)
			s.Upsert(model.Document{ID: id, Fields: map[string]string{"body": fmt.Sprintf("content %d", i)}})
			if i%7 == 0 {
				s.Delete(id)
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestSnapshotFunctionSeesImmutableView(t *testing.T) {
	s, _ := Open(t.TempDir(), testCfg())
	defer s.Close()
	s.Upsert(model.Document{ID: "d1", Fields: map[string]string{"body": "stable"}})
	n, _ := query.Parse("stable")
	err := s.Snapshot(func(e *search.Engine) error {
		h1 := len(e.Search(n))
		// 即使发生写操作，同一引擎绑定的是旧快照，结果不变。
		s.Upsert(model.Document{ID: "d2", Fields: map[string]string{"body": "stable also"}})
		h2 := len(e.Search(n))
		if h1 != h2 {
			return fmt.Errorf("snapshot changed under reader: %d -> %d", h1, h2)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 快照外新查询可以看到新数据
	r, _ := s.Query("stable")
	if got := idsFromRes(r); len(got) != 2 {
		t.Fatalf("new query must see committed write: %v", got)
	}
}

func TestHighlightsFromService(t *testing.T) {
	s, _ := Open(t.TempDir(), testCfg())
	defer s.Close()
	s.Upsert(model.Document{ID: "d", Fields: map[string]string{"body": "a hallo word"}})
	res, _ := s.Query("hello")
	if len(res.Hits) != 1 {
		t.Fatalf("fuzzy should hit: %+v", res.Hits)
	}
	fh := res.Hits[0].Fields["body"]
	if len(fh.Ranges) == 0 {
		t.Fatal("ranges must be included in results")
	}
	// 排序确定性：文档 ID 顺序
	ids := []string{}
	for _, h := range res.Hits {
		ids = append(ids, h.DocID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("results must be deterministically ordered")
	}
}

func TestDataDirPathUsed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	s, err := Open(dir, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
