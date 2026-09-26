package search

import (
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
)

// evalTerm 在单篇文档上求值一个词条。
//
// 字段限定：
//   - TermNode.Field 非空时只搜索该字段（文档无该字段 => touched=false）；
//   - 为空时搜索文档的全部字段，取得分最高（编辑距离最小）的字段命中。
//
// 得分（每个字段至多计一次，避免词频刷分；区间仍标注全部近似命中位置）：
//
//	score(field) = weight(field) * (1 - FuzzyPenalty * editDistance)
//
// 精确命中 editDistance=0 不打折；偏差越大，(1-penalty*d) 越小，得分越低。
func (e *Engine) evalTerm(tn *query.TermNode, docID string) *docAcc {
	fields := e.fieldNamesToSearch(docID, tn.Field)
	allowFuzzy := e.fuzzyAllowed(tn.Fuzzy)

	acc := newDocAcc()
	bestScore := -1.0
	var bestField string
	var bestDist int
	var bestRanges []Range

	for _, f := range fields {
		toks := e.snap.FieldTokens(docID, f)
		d, tok, ok := e.matchTokens(toks, tn.Term, allowFuzzy)
		if !ok {
			continue
		}
		// 收集该字段内所有达到上限的命中 token（供高亮标注全部出现位置）。
		ranges := []Range{{Start: tok.Start, End: tok.End}}
		for i := range toks {
			t := &toks[i]
			if t.Start == tok.Start {
				continue
			}
			var dd int
			if t.Text == tn.Term {
				dd = 0
			} else if allowFuzzy {
				dd = boundedDist(e, t.Text, tn.Term)
			} else {
				continue
			}
			if dd <= e.cfg.MaxEditDistance {
				ranges = append(ranges, Range{Start: t.Start, End: t.End})
			}
		}
		score := e.cfg.Weight(f) * (1 - e.cfg.FuzzyPenalty*float64(d))
		if score > bestScore {
			bestScore = score
			bestField = f
			bestDist = d
			bestRanges = ranges
		}
	}

	if bestScore < 0 {
		// 文档没有任何字段可命中。
		acc.touched = len(fields) > 0
		acc.match = false
		return acc
	}
	acc.touched = true
	acc.match = true
	fm := acc.field(bestField)
	fm.matched = true
	fm.score = bestScore
	fm.minDist = bestDist
	fm.ranges = bestRanges
	acc.score = bestScore
	return acc
}

func boundedDist(e *Engine, a, b string) int {
	return editDistanceBounded(a, b, e.cfg.MaxEditDistance)
}
