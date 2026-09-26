package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
)

func doc(id, v string) model.Document {
	return model.Document{ID: id, Fields: map[string]string{"body": v}}
}

func upsert(seq int, d model.Document) Record { return Record{Seq: seq, Op: OpUpsert, Doc: d} }

func TestAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	st, recs, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatal("empty dir should replay nothing")
	}
	for _, d := range []model.Document{doc("a", "alpha"), doc("b", "beta")} {
		if _, err := st.Append(Record{Op: OpUpsert, Doc: d}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Append(Record{Op: OpDelete, Doc: model.Document{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	_, recs, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].Doc.ID != "a" || recs[2].Op != OpDelete {
		t.Fatalf("replay wrong: %+v", recs)
	}
	if recs[0].Seq == 0 || !(recs[0].Seq < recs[1].Seq && recs[1].Seq < recs[2].Seq) {
		t.Fatalf("seq must be monotonic: %+v", recs)
	}
}

func TestSnapshotRotation(t *testing.T) {
	dir := t.TempDir()
	st, _, _ := Open(dir)
	st.Append(Record{Op: OpUpsert, Doc: doc("a", "alpha")})
	st.Append(Record{Op: OpUpsert, Doc: doc("b", "beta")})
	if err := st.SaveSnapshot([]model.Document{doc("a", "alpha"), doc("b", "beta")}); err != nil {
		t.Fatal(err)
	}
	// 快照后新变更进入 WAL
	st.Append(Record{Op: OpUpsert, Doc: doc("c", "gamma")})
	st.Close()

	if fi, err := os.Stat(filepath.Join(dir, walName)); err != nil || fi.Size() == 0 {
		// WAL 应被截断后只含 c 这一帧；帧头大于 0
		t.Fatalf("wal after snapshot unexpected: %v", err)
	}
	_, recs, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 快照中的 a,b + WAL 中的 c
	ids := map[string]bool{}
	for _, r := range recs {
		ids[r.Doc.ID] = true
	}
	if !ids["a"] || !ids["b"] || !ids["c"] || len(recs) != 3 {
		t.Fatalf("snapshot+wal replay wrong: %+v", recs)
	}
}

func TestTornTailIgnored(t *testing.T) {
	dir := t.TempDir()
	st, _, _ := Open(dir)
	st.Append(Record{Op: OpUpsert, Doc: doc("a", "alpha")})
	st.Append(Record{Op: OpUpsert, Doc: doc("b", "beta")})
	st.Close()

	// 模拟崩溃：往 WAL 末尾追加半截垃圾字节。
	p := filepath.Join(dir, walName)
	fi, _ := os.Stat(p)
	good := fi.Size()
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	f.Write([]byte{0x46, 0x53, 0x4c, 0x57, 0x00, 0x00})
	f.Close()

	_, recs, err := Open(dir)
	if err != nil {
		t.Fatalf("torn tail must be recovered, got %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("committed records must survive, got %+v", recs)
	}
	// 恢复后 WAL 应被截回干净长度。
	fi2, _ := os.Stat(p)
	if fi2.Size() != good {
		t.Fatalf("wal not truncated to %d, got %d", good, fi2.Size())
	}
}

func TestMidCorruptionRejected(t *testing.T) {
	dir := t.TempDir()
	st, _, _ := Open(dir)
	var sizes []int64
	for _, d := range []model.Document{doc("a", "alpha"), doc("b", "beta")} {
		st.Append(Record{Op: OpUpsert, Doc: d})
		fi, _ := os.Stat(filepath.Join(dir, walName))
		sizes = append(sizes, fi.Size())
	}
	st.Close()

	// 破坏第一帧 payload 中的一个字节（位于第一帧内部、其后仍有完整帧）。
	p := filepath.Join(dir, walName)
	data, _ := os.ReadFile(p)
	data[hdrSize+2] ^= 0xFF
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(dir); err == nil {
		t.Fatal("mid-file CRC corruption must be rejected")
	}
}
