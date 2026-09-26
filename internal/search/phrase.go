package search

import (
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/tokenize"
)

// evalPhrase 在单篇文档上求值一个短语。
//
// 匹配规则：短语中的词条必须在同一字段内按顺序、位置相邻（token 位置连续）。
// 容错可与短语组合：允许短语内每个词各自发生不超过 MaxEditDistance 的偏差，
// 短语的总偏差为各词编辑距离之和。
//
// 得分：先按普通词条命中逐词计分（权重 × (1 - penalty×距离)），
// 再对整条短语命中乘以 (1 + PhraseProximityBonus)。
// 总偏差越大，折扣越多、得分越低，与单词容错排序方向一致。
func (e *Engine) evalPhrase(pn *query.PhraseNode, docID string) *docAcc {
	fields := e.fieldNamesToSearch(docID, pn.Field)
	allowFuzzy := e.fuzzyAllowed(pn.Fuzzy)

	acc := newDocAcc()
	bestScore := -1.0
	var bestField string
	var bestTotalDist int
	var bestRanges []Range

	for _, f := range fields {
		toks := e.snap.FieldTokens(docID, f)
		score, totalDist, ranges, ok := e.bestPhraseInTokens(toks, pn.Terms, f, allowFuzzy)
		if !ok {
			continue
		}
		if score > bestScore {
			bestScore = score
			bestField = f
			bestTotalDist = totalDist
			bestRanges = ranges
		}
	}

	if bestScore < 0 {
		acc.touched = len(fields) > 0
		acc.match = false
		return acc
	}
	acc.touched = true
	acc.match = true
	fm := acc.field(bestField)
	fm.matched = true
	fm.score = bestScore
	fm.minDist = bestTotalDist
	fm.ranges = bestRanges
	acc.score = bestScore
	return acc
}

// bestPhraseInTokens 在一个字段的 token 序列中寻找短语的最佳出现。
// 返回加成后的得分、总编辑距离与命中 token 的字节区间。
func (e *Engine) bestPhraseInTokens(
	toks []tokenize.Token, terms []string, field string, allowFuzzy bool,
) (float64, int, []Range, bool) {
	if len(terms) == 0 || len(toks) < len(terms) {
		return 0, 0, nil, false
	}
	type anchor struct {
		idx  int
		dist int
	}
	// 为每个短语词收集可匹配的 token 位置及距离。
	anchors := make([][]anchor, len(terms))
	for wi, term := range terms {
		for ti := range toks {
			var d int
			if toks[ti].Text == term {
				d = 0
			} else if allowFuzzy {
				d = editDistanceBounded(toks[ti].Text, term, e.cfg.MaxEditDistance)
			} else {
				continue
			}
			if d <= e.cfg.MaxEditDistance {
				anchors[wi] = append(anchors[wi], anchor{ti, d})
			}
		}
		if len(anchors[wi]) == 0 {
			return 0, 0, nil, false // 短语中任一无候选即不可能命中
		}
	}

	// 寻找严格位置递增且相邻（t[j+1] == t[j]+1）的序列，取总编辑距离最小者。
	type seq struct {
		pos  []int
		dist int
	}
	var seqs []seq
	for _, a := range anchors[0] {
		seqs = append(seqs, seq{pos: []int{a.idx}, dist: a.dist})
	}
	for wi := 1; wi < len(terms); wi++ {
		var next []seq
		for _, s := range seqs {
			prev := s.pos[len(s.pos)-1]
			for _, a := range anchors[wi] {
				if a.idx == prev+1 {
					ns := seq{pos: make([]int, len(s.pos)+1), dist: s.dist + a.dist}
					copy(ns.pos, s.pos)
					ns.pos[len(s.pos)] = a.idx
					next = append(next, ns)
				}
			}
		}
		if len(next) == 0 {
			return 0, 0, nil, false
		}
		seqs = next
	}

	// 选择总距离最小、并列时起始位置更早的序列（确定性）。
	best := seqs[0]
	for _, s := range seqs[1:] {
		if s.dist < best.dist || (s.dist == best.dist && s.pos[0] < best.pos[0]) {
			best = s
		}
	}

	weight := e.cfg.Weight(field)
	var rawScore float64
	ranges := make([]Range, 0, len(best.pos))
	for k, ti := range best.pos {
		d := 0
		if toks[ti].Text != terms[k] {
			d = editDistanceBounded(toks[ti].Text, terms[k], e.cfg.MaxEditDistance)
		}
		// 每个词按其自身偏差折扣；字段权重只乘一次。
		rawScore += (1 - e.cfg.FuzzyPenalty*float64(d))
		ranges = append(ranges, Range{Start: toks[ti].Start, End: toks[ti].End})
	}
	score := weight * rawScore / float64(len(terms)) * (1 + e.cfg.PhraseProximityBonus)
	return score, best.dist, ranges, true
}
