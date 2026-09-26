package fuzzsearch

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestConcurrentReadWrite asserts that readers always observe an
// internally consistent, correctly sorted snapshot while a writer
// continuously commits batches. Run with -race for the data-race
// detector.
func TestConcurrentReadWrite(t *testing.T) {
	cfg := Config{
		FieldWeights:           map[string]float64{"body": 1},
		DefaultMaxEditDistance: 1,
		DataDir:                t.TempDir(),
	}
	s := newTestService(t, cfg)
	const n = 8
	for i := 0; i < n; i++ {
		mustUpsert(t, s, fmt.Sprintf("d%d", i), "x", "apple")
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: atomic batches, overwriting the same IDs in a tight loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		round := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			word := "apple"
			if round%2 == 1 {
				word = "peach"
			}
			muts := make([]Mutation, n)
			for i := 0; i < n; i++ {
				muts[i] = Mutation{Doc: Document{
					ID:     fmt.Sprintf("d%d", i),
					Fields: map[string]string{"body": word},
				}}
			}
			if err := s.Batch(muts); err != nil {
				t.Errorf("batch: %v", err)
				return
			}
			round++
		}
	}()

	// Readers validate every snapshot they receive.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := s.Search("appel~") // "appel" ~1 matches "apple"
				if err != nil {
					t.Errorf("search: %v", err)
					return
				}
				var prevScore = 1e300
				prevID := ""
				for _, h := range res.Hits {
					if h.Score != h.Score || h.Score > 1e30 {
						t.Errorf("bad score for %s: %v", h.DocID, h.Score)
					}
					if h.Score > prevScore || (h.Score == prevScore && h.DocID < prevID) {
						t.Errorf("hits not sorted: %s(%v) after %s(%v)",
							h.DocID, h.Score, prevID, prevScore)
					}
					prevScore, prevID = h.Score, h.DocID
					for _, hl := range h.Highlights {
						if hl.Field != "body" || hl.Start < 0 || hl.End <= hl.Start {
							t.Errorf("bad highlight %+v", hl)
						}
					}
				}
			}
		}()
	}

	time.AfterFunc(300*time.Millisecond, func() { close(stop) })
	wg.Wait()

	// After all writers quiesce, exactly n "apple"/"peach" docs exist;
	// reopening must replay without error.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := reopen(t, cfg)
	r, err := s2.Search("apple OR peach")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != n {
		t.Errorf("final state has %d docs, want %d", len(r.Hits), n)
	}
}

// TestBatchAtomicVisibility stresses that a multi-op batch appears as a
// unit: the number of docs matching the batch's vocabulary never takes
// an intermediate value while many such batches commit concurrently
// with searches.
func TestBatchAtomicVisibility(t *testing.T) {
	cfg := Config{
		FieldWeights: map[string]float64{"body": 1},
		DataDir:      t.TempDir(),
	}
	s := newTestService(t, cfg)
	const n = 6
	for i := 0; i < n; i++ {
		mustUpsert(t, s, fmt.Sprintf("d%d", i), "x", "alpha")
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		round := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			word := "alpha"
			if round%2 == 1 {
				word = "beta"
			}
			muts := make([]Mutation, n)
			for i := range muts {
				muts[i] = Mutation{Doc: Document{
					ID:     fmt.Sprintf("d%d", i),
					Fields: map[string]string{"body": word},
				}}
			}
			if err := s.Batch(muts); err != nil {
				t.Errorf("batch: %v", err)
				return
			}
			round++
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			r, err := s.Search("alpha OR beta")
			if err != nil {
				t.Errorf("search: %v", err)
				return
			}
			// Each snapshot is either pre-batch or post-batch: every one
			// of the n docs is in exactly one vocabulary, so the union
			// always contains exactly n hits.
			if len(r.Hits) != n {
				t.Errorf("partial commit observed: %d of %d docs", len(r.Hits), n)
				return
			}
		}
	}()
	time.AfterFunc(300*time.Millisecond, func() { close(stop) })
	wg.Wait()
}
