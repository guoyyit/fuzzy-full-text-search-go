package fuzzsearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func reopen(t *testing.T, cfg Config) *Service {
	t.Helper()
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPersistReplay(t *testing.T) {
	cfg := Config{
		FieldWeights:           map[string]float64{"title": 3, "body": 1},
		DefaultMaxEditDistance: 1,
		FuzzyPenalty:           0.5,
		DataDir:                t.TempDir(),
	}
	s := newTestService(t, cfg)
	mustUpsert(t, s, "a", "Apples", "red fruit")
	mustUpsert(t, s, "b", "Banana", "yellow fruit")
	mustUpsert(t, s, "c", "Cherry", "red small fruit")
	mustUpsert(t, s, "a", "Green Apple", "green fruit") // overwrite
	if err := s.Delete("b"); err != nil {               // delete
		t.Fatal(err)
	}
	if err := s.Batch([]Mutation{
		{Doc: Document{ID: "d", Fields: map[string]string{"title": "Date", "body": "brown fruit"}}},
		{Doc: Document{ID: "e", Fields: map[string]string{"title": "Elderberry", "body": "dark fruit"}}},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Search("fruit~")
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := reopen(t, cfg)
	if _, ok := s2.Get("b"); ok {
		t.Errorf("deleted doc b reappeared after restart")
	}
	if doc, ok := s2.Get("a"); !ok || doc.Fields["title"] != "Green Apple" {
		t.Errorf("updated doc a wrong after restart: %+v", doc)
	}
	after, err := s2.Search("fruit~")
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("results changed across restart:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
	}
	if ids := hitIDs(after); !eqStrings(ids, []string{"a", "c", "d", "e"}) {
		t.Errorf("replayed hits = %v", ids)
	}
	// New writes after replay must persist as well.
	if err := s2.Upsert(Document{ID: "f", Fields: map[string]string{"body": "fig"}}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	s3 := reopen(t, cfg)
	if _, ok := s3.Get("f"); !ok {
		t.Errorf("post-replay write lost on second restart")
	}
}

// TestWALTornTail simulates a crash in the middle of a write: a
// garbage tail is appended after several committed batches. Reopen
// must recover every intact batch, truncate the garbage, and remain
// healthy for new commits.
func TestWALTornTail(t *testing.T) {
	cfg := Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	}
	s := newTestService(t, cfg)
	for _, id := range []string{"a", "b", "c"} {
		mustUpsert(t, s, id, "x", "committed "+id)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Append a torn frame: valid length header claiming a huge payload,
	// only a few bytes present.
	path := filepath.Join(cfg.DataDir, walFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0xff, 0xff, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 'X', 'Y'}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := reopen(t, cfg)
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := s2.Get(id); !ok {
			t.Errorf("committed doc %s lost after torn-tail recovery", id)
		}
	}
	// The torn frame must not have created a phantom document.
	r, err := s2.Search("committed")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 3 {
		t.Errorf("want 3 hits after recovery, got %d", len(r.Hits))
	}
	// New commits work and survive another restart.
	mustUpsert(t, s2, "d", "x", "committed d")
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	s3 := reopen(t, cfg)
	if _, ok := s3.Get("d"); !ok {
		t.Errorf("post-recovery commit lost")
	}
	info, err := os.Stat(filepath.Join(cfg.DataDir, walFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Errorf("wal empty after recovery")
	}
}

// TestWALCRCRejectsCorruptFrame checks that a flipped byte inside an
// otherwise well-sized frame is detected and treated as tail damage.
func TestWALCRCRejectsCorruptFrame(t *testing.T) {
	cfg := Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	}
	s := newTestService(t, cfg)
	mustUpsert(t, s, "a", "x", "alpha")
	mustUpsert(t, s, "b", "x", "beta")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.DataDir, walFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the payload of the FIRST frame, then truncate the
	// rest: replay must reject the corrupt first frame cleanly rather
	// than crash, leaving an empty index.
	raw[10] ^= 0xff
	// Keep only the corrupted first frame so it is unambiguously the tail.
	var firstLen int64 = 8
	firstLen += int64(uint64(raw[0])) | int64(uint64(raw[1]))<<8 | int64(uint64(raw[2]))<<16 | int64(uint64(raw[3]))<<24
	firstLen += 4
	if err := os.WriteFile(path, raw[:firstLen], 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := reopen(t, cfg)
	if _, ok := s2.Get("a"); ok {
		t.Errorf("corrupt frame must not be replayed")
	}
	mustUpsert(t, s2, "c", "x", "gamma")
}
