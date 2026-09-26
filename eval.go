package fuzzsearch

import (
	"math"
	"sort"
)

// docAccum accumulates one document's score, per-field contributions
// and highlights while a query is evaluated.
type docAccum struct {
	score   float64
	byField map[string]float64
	hits    []Highlight
}

type evalResult map[string]*docAccum

func (r evalResult) acc(docID string) *docAccum {
	a := r[docID]
	if a == nil {
		a = &docAccum{byField: map[string]float64{}}
		r[docID] = a
	}
	return a
}

func (a *docAccum) merge(src *docAccum) {
	a.score += src.score
	for f, v := range src.byField {
		a.byField[f] += v
	}
	a.hits = append(a.hits, src.hits...)
}

// evalQuery evaluates q against an immutable snapshot and returns hits
// sorted deterministically by (score desc, docID asc).
func evalQuery(q Query, snap *snapshot) []Hit {
	res := evalNode(q, snap)
	hits := make([]Hit, 0, len(res))
	for docID, a := range res {
		hits = append(hits, Hit{
			DocID:      docID,
			Score:      a.score,
			Highlights: normalizeHighlights(a.hits),
			Explain:    a.byField,
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].DocID < hits[j].DocID
	})
	return hits
}

// normalizeHighlights sorts by (Field, Start, End) and drops duplicate
// intervals, keeping the smallest edit distance.
func normalizeHighlights(hs []Highlight) []Highlight {
	sort.Slice(hs, func(i, j int) bool {
		a, b := hs[i], hs[j]
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		return a.EditDistance < b.EditDistance
	})
	out := hs[:0]
	for i, h := range hs {
		if i > 0 && h.Field == hs[i-1].Field && h.Start == hs[i-1].Start && h.End == hs[i-1].End {
			continue
		}
		out = append(out, h)
	}
	return out
}

func evalNode(q Query, snap *snapshot) evalResult {
	switch n := q.(type) {
	case *TermQuery:
		return evalTerm(n, snap)
	case *PhraseQuery:
		return evalPhrase(n, snap)
	case *AndQuery:
		return evalAnd(n, snap)
	case *OrQuery:
		return evalOr(n, snap)
	case *NotQuery:
		child := evalNode(n.Child, snap)
		res := evalResult{}
		for docID := range snap.docs {
			if _, excluded := child[docID]; !excluded {
				res[docID] = &docAccum{byField: map[string]float64{}}
			}
		}
		return res
	}
	return evalResult{}
}

func evalAnd(q *AndQuery, snap *snapshot) evalResult {
	var positives, negatives []Query
	for _, c := range q.Children {
		if _, isNot := c.(*NotQuery); isNot {
			negatives = append(negatives, c)
		} else {
			positives = append(positives, c)
		}
	}
	var base evalResult
	if len(positives) == 0 {
		base = evalResult{}
		for docID := range snap.docs {
			base[docID] = &docAccum{byField: map[string]float64{}}
		}
	} else {
		base = evalNode(positives[0], snap)
		for _, p := range positives[1:] {
			other := evalNode(p, snap)
			for docID, a := range base {
				if o, ok := other[docID]; ok {
					a.merge(o)
				} else {
					delete(base, docID)
				}
			}
		}
	}
	for _, neg := range negatives {
		excluded := evalNode(neg.(*NotQuery).Child, snap)
		for docID := range excluded {
			delete(base, docID)
		}
	}
	return base
}

func evalOr(q *OrQuery, snap *snapshot) evalResult {
	res := evalResult{}
	for _, c := range q.Children {
		part := evalNode(c, snap)
		for docID, a := range part {
			if existing, ok := res[docID]; ok {
				existing.merge(a)
			} else {
				res[docID] = a
			}
		}
	}
	return res
}

// resolveMaxEd maps a parsed MaxEd to the effective edit-distance cap:
// -1 (no '~') -> 0 (exact); -2 (bare '~') -> the configured default;
// otherwise the explicit value clamped to the configured default.
func resolveMaxEd(maxEd int, cfg Config) int {
	switch {
	case maxEd == -1:
		return 0
	case maxEd == -2:
		return cfg.DefaultMaxEditDistance
	case maxEd > cfg.DefaultMaxEditDistance:
		return cfg.DefaultMaxEditDistance
	default:
		return maxEd
	}
}

// hasPrefix reports whether candidate starts with the first
// prefixLen runes of query (or all of query when it is shorter).
func hasPrefix(query, candidate string, prefixLen int) bool {
	if prefixLen <= 0 {
		return true
	}
	qr := []rune(query)
	if len(qr) < prefixLen {
		prefixLen = len(qr)
	}
	cr := []rune(candidate)
	if len(cr) < prefixLen {
		return false
	}
	for i := 0; i < prefixLen; i++ {
		if qr[i] != cr[i] {
			return false
		}
	}
	return true
}

// idf is the deterministic inverse-document-frequency component.
func idf(totalDocs, docFreq int) float64 {
	return math.Log(1 + (float64(totalDocs-docFreq)+0.5)/(float64(docFreq)+0.5))
}

// fuzzyFactor decays a match's score as its edit distance grows.
func fuzzyFactor(d int, cfg Config) float64 {
	return 1 / (1 + cfg.FuzzyPenalty*float64(d))
}

func evalTerm(tq *TermQuery, snap *snapshot) evalResult {
	res := evalResult{}
	fields, ok := snap.fieldsFor(tq.Field)
	if !ok {
		return res
	}
	maxEd := resolveMaxEd(tq.MaxEd, snap.cfg)
	totalDocs := len(snap.docs)

	for _, field := range fields {
		termInv := snap.inv[field]
		if len(termInv) == 0 {
			continue
		}
		weight := snap.cfg.FieldWeights[field]

		// Collect candidate terms in sorted order so floating-point
		// accumulation order is deterministic.
		var candidates []string
		if maxEd == 0 {
			if _, ok := termInv[tq.Term]; ok {
				candidates = []string{tq.Term}
			}
		} else {
			for term := range termInv {
				if !hasPrefix(tq.Term, term, snap.cfg.FuzzyPrefixLength) {
					continue
				}
				if _, ok := withinEditDistance(tq.Term, term, maxEd); ok {
					candidates = append(candidates, term)
				}
			}
			sort.Strings(candidates)
		}

		for _, cand := range candidates {
			list := termInv[cand]
			d := 0
			if cand != tq.Term {
				d, _ = withinEditDistance(tq.Term, cand, maxEd)
			}
			base := weight * (1 + snap.cfg.IDFWeight*idf(totalDocs, len(list))) * fuzzyFactor(d, snap.cfg)
			for _, p := range list {
				tf := float64(len(p.Positions))
				contrib := base * (1 + math.Log(tf))
				acc := res.acc(p.DocID)
				acc.score += contrib
				acc.byField[field] += contrib
				for _, pos := range p.Positions {
					tok := snap.tokens[p.DocID][field][pos]
					acc.hits = append(acc.hits, Highlight{
						Field:        field,
						Start:        tok.Start,
						End:          tok.End,
						Term:         cand,
						EditDistance: d,
					})
				}
			}
		}
	}
	return res
}

func evalPhrase(pq *PhraseQuery, snap *snapshot) evalResult {
	res := evalResult{}
	fields, ok := snap.fieldsFor(pq.Field)
	if !ok {
		return res
	}
	maxEd := 0
	if pq.Fuzzy {
		maxEd = resolveMaxEd(pq.MaxEd, snap.cfg)
	}
	totalDocs := len(snap.docs)
	n := len(pq.Terms)

	// Deterministic document iteration order.
	docIDs := make([]string, 0, len(snap.docs))
	for id := range snap.docs {
		docIDs = append(docIDs, id)
	}
	sort.Strings(docIDs)

	for _, field := range fields {
		weight := snap.cfg.FieldWeights[field]
		for _, docID := range docIDs {
			toks := snap.tokens[docID][field]
			if len(toks) < n {
				continue
			}
			for i := 0; i+n <= len(toks); i++ {
				totalEd := 0
				sumIDF := 0.0
				matched := true
				var occ []Highlight
				for j := 0; j < n; j++ {
					tok := toks[i+j]
					d := 0
					if tok.Term != pq.Terms[j] {
						if maxEd == 0 || !hasPrefix(pq.Terms[j], tok.Term, snap.cfg.FuzzyPrefixLength) {
							matched = false
							break
						}
						dist, ok := withinEditDistance(pq.Terms[j], tok.Term, maxEd)
						if !ok {
							matched = false
							break
						}
						d = dist
					}
					totalEd += d
					sumIDF += idf(totalDocs, len(snap.inv[field][tok.Term]))
					occ = append(occ, Highlight{
						Field:        field,
						Start:        tok.Start,
						End:          tok.End,
						Term:         tok.Term,
						EditDistance: d,
					})
				}
				if !matched {
					continue
				}
				contrib := weight * (1 + snap.cfg.IDFWeight*sumIDF) * fuzzyFactor(totalEd, snap.cfg)
				acc := res.acc(docID)
				acc.score += contrib
				acc.byField[field] += contrib
				acc.hits = append(acc.hits, occ...)
			}
		}
	}
	return res
}
