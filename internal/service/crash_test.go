package service

import (
	"os"
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
)

// TestCrashWithoutClose 模拟进程被杀死：不调用 Close、不做快照，
// 但每条 WAL 记录已经 fsync。重新打开后已确认的提交必须全部保留。
func TestCrashWithoutClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		id := string(rune('a'+i%5)) + "-doc"
		s.Upsert(model.Document{ID: id, Fields: map[string]string{"body": "persisted content"}})
	}
	s.Delete("a-doc")
	// 故意不 Close：直接丢弃句柄，模拟崩溃（数据已逐帧 fsync）。
	s = nil

	s2, err := Open(dir, testCfg())
	if err != nil {
		t.Fatalf("reopen after crash failed: %v", err)
	}
	defer s2.Close()
	r, err := s2.Query("persisted")
	if err != nil {
		t.Fatal(err)
	}
	// 5 个不同 ID 中删除了 a-doc，应剩 4 个。
	if len(r.Hits) != 4 {
		t.Fatalf("after crash expected 4 docs, got %d: %+v", len(r.Hits), r.Hits)
	}
	for _, h := range r.Hits {
		if h.DocID == "a-doc" {
			t.Fatal("deleted doc must stay deleted after crash")
		}
	}
}

// TestSnapshotFileAtomicShape 验证快照以原子文件形式存在，且数据目录自包含。
func TestSnapshotFileAtomicShape(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, testCfg())
	s.Upsert(model.Document{ID: "x", Fields: map[string]string{"body": "v"}})
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	// checkpoint 后目录中不应残留 tmp 文件。
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if name := e.Name(); len(name) > 4 && name[len(name)-4:] == ".tmp" {
			t.Fatalf("temporary file left behind: %s", name)
		}
	}
	s.Close()
}

// TestRepeatedOpenCloseIdempotent 多次开关结果一致。
func TestRepeatedOpenCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	for round := 0; round < 3; round++ {
		s, err := Open(dir, testCfg())
		if err != nil {
			t.Fatal(err)
		}
		s.Upsert(model.Document{ID: "same", Fields: map[string]string{"body": "round value"}})
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := Open(dir, testCfg())
	defer s.Close()
	r, _ := s.Query("round")
	if len(r.Hits) != 1 || r.Hits[0].DocID != "same" {
		t.Fatalf("repeated restart lost data: %+v", r.Hits)
	}
}
