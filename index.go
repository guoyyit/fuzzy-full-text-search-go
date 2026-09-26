package fuzzsearch

import (
	"sort"
	"sync"
)

type opKind int

const (
	opUpsert opKind = iota
	opDelete
)

// writeOp is one mutation inside an atomic batch.
type writeOp struct {
	kind opKind
	doc  Document // kind == opUpsert
	id   string   // kind == opDelete
}

// posting records one document's occurrences of a term in one field.
// Positions are token positions within the field, in ascending order.
type posting struct {
	DocID     string
	Positions []int
}

// snapshot is an immutable point-in-time view of the whole index.
// Query goroutines hold a *snapshot captured at call time; it is never
// mutated afterwards. Writers create a shallow-copied snapshot and
// mutate only the copies (copy-on-write), then atomically swap the
// pointer, so a running query can never observe a partial commit.
type snapshot struct {
	cfg    Config
	docs   map[string]Document             // docID -> document
	tokens map[string]map[string][]Token   // docID -> field -> analyzed tokens
	inv    map[string]map[string][]posting // field -> term -> postings sorted by DocID
}

// index is the concurrency-safe mutable handle over the latest snapshot.
type index struct {
	mu   sync.RWMutex
	snap *snapshot
}

func newSnapshot(cfg Config) *snapshot {
	return &snapshot{
		cfg:    cfg,
		docs:   make(map[string]Document),
		tokens: make(map[string]map[string][]Token),
		inv:    make(map[string]map[string][]posting),
	}
}

func newIndex(cfg Config) *index {
	return &index{snap: newSnapshot(cfg)}
}

// current returns the snapshot visible at this instant. The returned
// snapshot stays valid forever; callers must not mutate it.
func (idx *index) current() *snapshot {
	idx.mu.RLock()
	s := idx.snap
	idx.mu.RUnlock()
	return s
}

// fieldsFor returns the configured field names to search for a query
// qualifier. An unqualified query searches every weighted field in
// sorted name order; a qualifier naming an unconfigured field matches
// nothing (returns ok=false).
func (s *snapshot) fieldsFor(qualified string) ([]string, bool) {
	if qualified == "" {
		names := make([]string, 0, len(s.cfg.FieldWeights))
		for f := range s.cfg.FieldWeights {
			names = append(names, f)
		}
		sort.Strings(names)
		return names, true
	}
	if _, ok := s.cfg.FieldWeights[qualified]; !ok {
		return nil, false
	}
	return []string{qualified}, true
}

// applyOps applies a batch atomically. It copies every map it will
// touch and never mutates a slice in place, so the previously published
// snapshot remains intact for in-flight readers.
func (idx *index) applyOps(ops []writeOp) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	old := idx.snap
	next := &snapshot{
		cfg:    old.cfg,
		docs:   make(map[string]Document, len(old.docs)),
		tokens: make(map[string]map[string][]Token, len(old.tokens)),
		inv:    make(map[string]map[string][]posting, len(old.inv)),
	}
	for k, v := range old.docs {
		next.docs[k] = v
	}
	for k, v := range old.tokens {
		next.tokens[k] = v // inner maps are immutable after publication
	}
	for k, v := range old.inv {
		next.inv[k] = v // inner maps are replaced, not mutated, when touched
	}

	touchedFields := map[string]bool{}
	for _, op := range ops {
		switch op.kind {
		case opUpsert:
			if _, exists := next.docs[op.doc.ID]; exists {
				next.removeDocLocked(next.docs[op.doc.ID], touchedFields)
			}
			next.addDocLocked(op.doc, touchedFields)
		case opDelete:
			if old, exists := next.docs[op.id]; exists {
				next.removeDocLocked(old, touchedFields)
			}
		}
	}

	idx.snap = next
}

func (s *snapshot) addDocLocked(doc Document, touched map[string]bool) {
	s.docs[doc.ID] = doc
	docTokens := make(map[string][]Token)
	// Deterministic field order is unnecessary for map storage, but the
	// posting lists it produces stay sorted by DocID.
	for field, value := range doc.Fields {
		if _, weighted := s.cfg.FieldWeights[field]; !weighted {
			continue // unknown fields are stored but not indexed/scored
		}
		toks := analyze(value)
		docTokens[field] = toks
		terms := map[string][]int{} // term -> token positions
		for pos, t := range toks {
			terms[t.Term] = append(terms[t.Term], pos)
		}
		fieldInv := s.inv[field]
		if fieldInv == nil {
			fieldInv = make(map[string][]posting)
			s.inv[field] = fieldInv
		} else if !touched[field] {
			cp := make(map[string][]posting, len(fieldInv))
			for k, v := range fieldInv {
				cp[k] = v
			}
			fieldInv = cp
			s.inv[field] = cp
		}
		touched[field] = true
		for term, positions := range terms {
			list := append([]posting(nil), fieldInv[term]...)
			ins := sort.Search(len(list), func(i int) bool { return list[i].DocID >= doc.ID })
			list = append(list, posting{})
			copy(list[ins+1:], list[ins:])
			list[ins] = posting{DocID: doc.ID, Positions: positions}
			fieldInv[term] = list
		}
	}
	s.tokens[doc.ID] = docTokens
}

func (s *snapshot) removeDocLocked(doc Document, touched map[string]bool) {
	delete(s.docs, doc.ID)
	delete(s.tokens, doc.ID)
	for field, value := range doc.Fields {
		if _, weighted := s.cfg.FieldWeights[field]; !weighted {
			continue
		}
		fieldInv := s.inv[field]
		if fieldInv == nil {
			continue
		}
		if !touched[field] {
			cp := make(map[string][]posting, len(fieldInv))
			for k, v := range fieldInv {
				cp[k] = v
			}
			fieldInv = cp
			s.inv[field] = cp
		}
		touched[field] = true
		for _, t := range analyze(value) {
			list := fieldInv[t.Term]
			pos := sort.Search(len(list), func(i int) bool { return list[i].DocID >= doc.ID })
			if pos < len(list) && list[pos].DocID == doc.ID {
				list = append(append([]posting(nil), list[:pos]...), list[pos+1:]...)
				if len(list) == 0 {
					delete(fieldInv, t.Term)
				} else {
					fieldInv[t.Term] = list
				}
			}
		}
	}
}
