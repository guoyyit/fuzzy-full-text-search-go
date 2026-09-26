package fuzzsearch

import (
	"errors"
	"fmt"
	"sync"
)

// Service is a persistent, concurrency-safe fuzzy full-text search
// service. A Service is safe for concurrent use by many goroutines.
//
// Consistency model: writers are serialized; each committed batch is
// appended to the WAL (fsync) and then published as one immutable
// snapshot. A Search call captures the snapshot at its start and sees a
// consistent point-in-time view — never a partial commit.
type Service struct {
	mu     sync.Mutex // serializes writers (WAL append + snapshot swap)
	cfg    Config
	idx    *index
	wal    *wal
	closed bool
}

// Open opens (or creates) a service backed by cfg.DataDir, replaying
// any previously committed batches. Invalid configuration is rejected
// before any file is touched.
func Open(cfg Config) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	w, err := openWAL(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, idx: newIndex(cfg), wal: w}
	if err := w.replay(func(ops []writeOp) { s.idx.applyOps(ops) }); err != nil {
		w.close()
		return nil, fmt.Errorf("open: replay wal: %w", err)
	}
	return s, nil
}

// Close flushes and releases all resources. The service must not be
// used afterwards.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.wal.close()
}

// Mutation is one element of an atomic batch.
type Mutation struct {
	Delete bool // true: delete Doc.ID; false: upsert Doc
	Doc    Document
}

// Upsert adds a document, replacing any existing document with the same
// ID. The change is durable and visible before the method returns.
func (s *Service) Upsert(doc Document) error {
	return s.Batch([]Mutation{{Doc: doc}})
}

// Delete removes a document by ID. Deleting a missing ID is not an
// error. The change is durable and visible before the method returns.
func (s *Service) Delete(id string) error {
	return s.Batch([]Mutation{{Delete: true, Doc: Document{ID: id}}})
}

// Batch commits several upserts/deletes atomically: the whole batch is
// written to the WAL and fsync'd, then published as one snapshot. If
// the WAL write fails, nothing becomes visible.
func (s *Service) Batch(muts []Mutation) error {
	if len(muts) == 0 {
		return nil
	}
	ops := make([]writeOp, 0, len(muts))
	for _, m := range muts {
		if m.Doc.ID == "" {
			return errors.New("batch: document ID must be non-empty")
		}
		if m.Delete {
			ops = append(ops, writeOp{kind: opDelete, id: m.Doc.ID})
		} else {
			if m.Doc.Fields == nil {
				m.Doc.Fields = map[string]string{}
			}
			ops = append(ops, writeOp{kind: opUpsert, doc: m.Doc})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("service: closed")
	}
	if err := s.wal.append(ops); err != nil {
		return fmt.Errorf("batch: commit: %w", err)
	}
	s.idx.applyOps(ops)
	return nil
}

// Get returns the current document with the given ID, if any.
func (s *Service) Get(id string) (Document, bool) {
	doc, ok := s.idx.current().docs[id]
	return doc, ok
}

// Search runs a query against the snapshot visible at call time and
// returns a deterministic, ranked, highlighted result set.
func (s *Service) Search(query string) (*SearchResult, error) {
	q, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	snap := s.idx.current()
	return &SearchResult{Hits: evalQuery(q, snap)}, nil
}

// Explain documents the fuzzy-match boundary used by the service.
func (s *Service) Explain() string {
	return fmt.Sprintf(
		"fuzzy matching: a query term 't~N' matches any indexed term within Levenshtein edit distance N "+
			"(insert/delete/substitute = 1 each); 't~' uses the configured default of %d; "+
			"explicit N is clamped to that default; the first %d character(s) must match exactly; "+
			"score decays as 1/(1+%g*distance)",
		s.cfg.DefaultMaxEditDistance, s.cfg.FuzzyPrefixLength, s.cfg.FuzzyPenalty)
}
