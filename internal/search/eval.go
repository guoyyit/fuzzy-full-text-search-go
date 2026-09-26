package search

import (
	"sort"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/editdist"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/index"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/tokenize"
)

// fieldMatch 记录一个字段上的命中证据。
type fieldMatch struct {
	score   float64
	minDist int     // 该字段本次命中中最小的编辑距离
	ranges  []Range // 命中 token 的字节区间
	matched bool
}

// docAcc 是一篇文档在 AST 求值过程中的累加器。
//
// 布尔语义采用三值：
//   - match=true：正向子句判定文档命中；
//   - match=false 且 touched=true：子句在该文档上不成立（用于 AND/OR 组合）；
//   - touched=false：子句完全不适用于该文档（例如字段限定到文档不具备的字段）。
type docAcc struct {
	match   bool
	touched bool
	score   float64
	fields  map[string]*fieldMatch // 只记录正向（得分）命中的证据
}

func newDocAcc() *docAcc {
	return &docAcc{fields: map[string]*fieldMatch{}}
}

func (a *docAcc) field(f string) *fieldMatch {
	fm, ok := a.fields[f]
	if !ok {
		fm = &fieldMatch{minDist: -1}
		a.fields[f] = fm
	}
	return fm
}

func (a *docAcc) toHit(id string) Hit {
	fhs := make(map[string]FieldHit, len(a.fields))
	for f, fm := range a.fields {
		if !fm.matched {
			continue
		}
		fhs[f] = FieldHit{
			Field:        f,
			Score:        fm.score,
			EditDistance: fm.minDist,
			Ranges:       normalizeRanges(fm.ranges),
		}
	}
	return Hit{DocID: id, Score: a.score, Fields: fhs}
}

// mergeFields 把 b 的字段证据并入 a（OR 组合时使用：多次命中取更强得分与区间并集）。
func (a *docAcc) mergeFields(b *docAcc) {
	for f, bm := range b.fields {
		if !bm.matched {
			continue
		}
		am := a.field(f)
		if !am.matched || bm.score > am.score {
			am.score = bm.score
			am.minDist = bm.minDist
		}
		am.matched = true
		am.ranges = append(am.ranges, bm.ranges...)
	}
}

// eval 在当前快照上对单篇文档递归求值查询 AST。
func (e *Engine) eval(n query.Node, docID string) *docAcc {
	switch x := n.(type) {
	case *query.TermNode:
		return e.evalTerm(x, docID)
	case *query.PhraseNode:
		return e.evalPhrase(x, docID)
	case *query.AndNode:
		acc := newDocAcc()
		// AND 命中条件：每个正向子句都命中，且没有否定子句命中。
		// 子句不适用（touched=false，如字段缺失）视为正向不满足、否定不成立。
		ok := true
		hasPositive := false
		for _, c := range x.Children {
			if nn, isNot := c.(*query.NotNode); isNot {
				na := e.eval(nn.Child, docID)
				if na != nil && na.touched && na.match {
					ok = false // 被否定的子句命中 => 排除
				}
				continue
			}
			hasPositive = true
			ca := e.eval(c, docID)
			if ca == nil || !ca.touched || !ca.match {
				ok = false
				continue
			}
			acc.mergeFields(ca)
		}
		// 只有否定子句（如 AND NOT a）：只要没有否定子句命中，文档即命中。
		acc.touched = true
		acc.match = ok && (hasPositive || allChildrenNegated(x))
		acc.score = sumFieldScores(acc)
		return acc
	case *query.OrNode:
		acc := newDocAcc()
		anyTouched, anyMatch := false, false
		for _, c := range x.Children {
			if nn, isNot := c.(*query.NotNode); isNot {
				na := e.eval(nn.Child, docID)
				if na != nil && na.touched {
					anyTouched = true
					if !na.match {
						anyMatch = true // 否定子句成立也算 OR 的一种命中（不得分）
					}
				}
				continue
			}
			ca := e.eval(c, docID)
			if ca == nil || !ca.touched {
				continue
			}
			anyTouched = true
			if ca.match {
				anyMatch = true
				acc.mergeFields(ca)
			}
		}
		acc.touched = anyTouched
		acc.match = anyMatch
		acc.score = sumFieldScores(acc)
		return acc
	case *query.NotNode:
		ca := e.eval(x.Child, docID)
		acc := newDocAcc()
		// 子句适用且不命中 => 否定成立；子句不适用 => 否定不成立（不放宽匹配）。
		if ca != nil && ca.touched {
			acc.touched = true
			acc.match = !ca.match
		}
		return acc
	}
	return nil
}

func allChildrenNegated(n *query.AndNode) bool {
	if len(n.Children) == 0 {
		return false
	}
	for _, c := range n.Children {
		if _, ok := c.(*query.NotNode); !ok {
			return false
		}
	}
	return true
}

func sumFieldScores(a *docAcc) float64 {
	var total float64
	for _, fm := range a.fields {
		if fm.matched {
			total += fm.score
		}
	}
	return total
}

// fuzzyAllowed 决定一个三态 Fuzzy 标记在当前配置下是否允许容错。
func (e *Engine) fuzzyAllowed(mark *bool) bool {
	if mark != nil {
		return *mark && e.cfg.MaxEditDistance > 0
	}
	return e.cfg.MaxEditDistance > 0
}

// matchTokens 在一个字段的 token 序列中寻找与目标词的最佳命中，
// 返回最佳编辑距离与对应 token；不命中时 ok=false。
func (e *Engine) matchTokens(toks []tokenize.Token, target string, allowFuzzy bool) (int, *tokenize.Token, bool) {
	bestDist := 1<<31 - 1
	var bestTok *tokenize.Token
	for i := range toks {
		t := &toks[i]
		d := -1
		if t.Text == target {
			d = 0
		} else if allowFuzzy {
			d = editdist.Distance(t.Text, target, e.cfg.MaxEditDistance)
		}
		if d < 0 || d > e.cfg.MaxEditDistance {
			continue
		}
		if d < bestDist || (d == bestDist && (bestTok == nil || t.Start < bestTok.Start)) {
			bestDist = d
			bestTok = t
		}
	}
	if bestTok == nil {
		return 0, nil, false
	}
	return bestDist, bestTok, true
}

// fieldNamesToSearch 返回一个词/短语应搜索的字段集合（显式限定或文档全部字段）。
func (e *Engine) fieldNamesToSearch(docID, explicit string) []string {
	if explicit != "" {
		if _, ok := e.snap.DocFieldValue(docID, explicit); ok {
			return []string{explicit}
		}
		return nil
	}
	return e.snap.Fields(docID)
}

// normalizeRanges 对区间按起点排序、合并重叠/相邻区间并去重。
func normalizeRanges(rs []Range) []Range {
	if len(rs) == 0 {
		return nil
	}
	sorted := make([]Range, len(rs))
	copy(sorted, rs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})
	out := sorted[:1]
	for _, r := range sorted[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
		} else {
			out = append(out, r)
		}
	}
	return out
}

var _ = index.Posting{}
