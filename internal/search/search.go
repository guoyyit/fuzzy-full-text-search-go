package search

import (
	"sort"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/config"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/index"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
)

// Range 是匹配字段内的一个字节区间（左闭右开），可直接对原字段值切片做高亮。
// 近似命中（拼写容错）同样标注实际命中的文档词条区间，而非查询词区间。
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// FieldHit 描述一篇文档在某个字段上的命中明细。
type FieldHit struct {
	Field        string  `json:"field"`
	Score        float64 `json:"score"`
	EditDistance int     `json:"edit_distance"` // 该字段命中中最小的编辑距离；精确命中为 0
	Ranges       []Range `json:"ranges"`
}

// Hit 是一篇命中文档的结果项。
type Hit struct {
	DocID  string              `json:"doc_id"`
	Score  float64             `json:"score"`
	Fields map[string]FieldHit `json:"fields"`
}

// Engine 在一个不可变快照上执行查询；Engine 本身无状态、可并发使用。
type Engine struct {
	snap *index.Snapshot
	cfg  config.Config
}

// NewEngine 基于快照与配置创建查询引擎。
func NewEngine(snap *index.Snapshot, cfg config.Config) *Engine {
	return &Engine{snap: snap, cfg: cfg}
}

// Search 执行查询并返回稳定排序的命中结果。
//
// 稳定性保证：相同查询、相同快照下，返回的集合与顺序完全一致。
// 排序规则为：总分降序；总分相同时按文档 ID 字典序升序（无随机性、无 map 遍历依赖）。
func (e *Engine) Search(n query.Node) []Hit {
	docs := e.snap.DocumentIDs()
	results := make(map[string]*docAcc)
	for _, id := range docs {
		acc := e.eval(n, id)
		if acc != nil && acc.match {
			results[id] = acc
		}
	}
	hits := make([]Hit, 0, len(results))
	for id, acc := range results {
		hits = append(hits, acc.toHit(id))
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].DocID < hits[j].DocID
	})
	return hits
}

// Highlight 返回某命中文档各字段的精确匹配字节区间（含近似命中）。
// 区间按起始位置升序、去重重叠；不会越界。
func (e *Engine) Highlight(n query.Node, docID string) map[string][]Range {
	acc := e.eval(n, docID)
	if acc == nil || !acc.match {
		return map[string][]Range{}
	}
	out := make(map[string][]Range, len(acc.fields))
	for f, fm := range acc.fields {
		out[f] = normalizeRanges(fm.ranges)
	}
	return out
}
