package index

import (
	"sort"
	"sync"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/tokenize"
)

// docData 保存一篇文档的原始字段值与分词结果。
type docData struct {
	fields map[string]string           // 原始文本
	tokens map[string][]tokenize.Token // 字段 -> 分词
}

// Posting 描述某文档内某词条的一次出现。
type Posting struct {
	DocID string
	Token tokenize.Token
}

// Snapshot 是某一时刻索引的不可变只读快照。
// 快照一经发布即不再修改；任何变更都通过 Add/Delete 复制出一份新快照，
// 从而保证进行中的查询始终读到一致的历史版本。
type Snapshot struct {
	docs  map[string]*docData             // docID -> 文档数据
	posts map[string]map[string][]Posting // field -> term -> postings
	order []string                        // 文档 ID，按首次插入顺序（排序稳定用）
}

func newSnapshot() *Snapshot {
	return &Snapshot{
		docs:  map[string]*docData{},
		posts: map[string]map[string][]Posting{},
	}
}

// Index 是支持快照隔离的并发安全索引。
type Index struct {
	mu sync.RWMutex
	s  *Snapshot
}

// New 创建空索引。
func New() *Index {
	return &Index{s: newSnapshot()}
}

// Current 返回当前快照。返回的快照不可变，可被任意 goroutine 并发长期持有。
func (i *Index) Current() *Snapshot {
	i.mu.RLock()
	s := i.s
	i.mu.RUnlock()
	return s
}

// Commit 原子发布一个新快照；发布后所有新查询看到该快照。
func (i *Index) Commit(s *Snapshot) {
	i.mu.Lock()
	i.s = s
	i.mu.Unlock()
}

// Apply 在一次原子操作内应用变更：
// doc 非空表示新增/覆盖；deleteID 非空表示删除。提交后立即可见，
// 进行中的查询仍持有旧快照，不会看到部分结果。
func (i *Index) Apply(doc *model.Document, deleteID string) {
	i.mu.Lock()
	if doc != nil {
		i.s = i.s.Add(*doc)
	} else if deleteID != "" {
		i.s = i.s.Delete(deleteID)
	}
	i.mu.Unlock()
}

// clone 复制一份快照结构（map/shallow copy），供 copy-on-write 修改。
func (s *Snapshot) clone() *Snapshot {
	ns := &Snapshot{
		docs:  make(map[string]*docData, len(s.docs)),
		posts: make(map[string]map[string][]Posting, len(s.posts)),
		order: make([]string, len(s.order)),
	}
	copy(ns.order, s.order)
	for k, v := range s.docs {
		ns.docs[k] = v // 旧 docData 不可变，共享即可
	}
	for f, m := range s.posts {
		nm := make(map[string][]Posting, len(m))
		for t, p := range m {
			nm[t] = p // 旧 posting 切片不可变，共享即可
		}
		ns.posts[f] = nm
	}
	return ns
}

// Add 基于当前快照构建一个新增/覆盖文档后的新快照。
func (s *Snapshot) Add(doc model.Document) *Snapshot {
	ns := s.clone()

	// 覆盖更新：先移除旧文档在倒排表中的全部痕迹。
	if _, existed := ns.docs[doc.ID]; existed {
		ns.removeFromPostings(doc.ID)
	} else {
		ns.order = append(ns.order, doc.ID)
	}

	d := &docData{
		fields: make(map[string]string, len(doc.Fields)),
		tokens: make(map[string][]tokenize.Token, len(doc.Fields)),
	}
	for f, v := range doc.Fields {
		d.fields[f] = v
		toks := tokenize.Tokenize(v)
		d.tokens[f] = toks
		fieldPosts, ok := ns.posts[f]
		if !ok {
			fieldPosts = map[string][]Posting{}
			ns.posts[f] = fieldPosts
		}
		for _, tk := range toks {
			fieldPosts[tk.Text] = append(fieldPosts[tk.Text], Posting{DocID: doc.ID, Token: tk})
		}
	}
	ns.docs[doc.ID] = d
	return ns
}

// removeFromPostings 删除指定文档在所有字段倒排表中的 postings（原地收缩）。
// 仅在 clone 出的新快照上调用。
func (s *Snapshot) removeFromPostings(id string) {
	for _, m := range s.posts {
		for term, ps := range m {
			// 必须分配新数组：复用 ps 的底层数组会污染不可变的旧快照。
			kept := make([]Posting, 0, len(ps))
			for _, p := range ps {
				if p.DocID != id {
					kept = append(kept, p)
				}
			}
			if len(kept) == 0 {
				delete(m, term)
			} else {
				m[term] = kept
			}
		}
	}
}

// Delete 基于当前快照构建一个删除文档后的新快照；文档不存在时等价于克隆。
func (s *Snapshot) Delete(id string) *Snapshot {
	ns := s.clone()
	if _, ok := ns.docs[id]; !ok {
		return ns
	}
	ns.removeFromPostings(id)
	delete(ns.docs, id)
	out := ns.order[:0]
	for _, x := range ns.order {
		if x != id {
			out = append(out, x)
		}
	}
	ns.order = out
	return ns
}

// --- 只读访问器 ---

// DocumentIDs 返回按首次插入顺序排列的全部文档 ID。
func (s *Snapshot) DocumentIDs() []string { return s.order }

// Has 报告文档是否存在。
func (s *Snapshot) Has(id string) bool { _, ok := s.docs[id]; return ok }

// AllDocuments 按插入顺序导出全部文档（持久化快照用）。
func (s *Snapshot) AllDocuments() []model.Document {
	out := make([]model.Document, 0, len(s.order))
	for _, id := range s.order {
		d := s.docs[id]
		fields := make(map[string]string, len(d.fields))
		for f, v := range d.fields {
			fields[f] = v
		}
		out = append(out, model.Document{ID: id, Fields: fields})
	}
	return out
}

// FieldTokens 返回某文档某字段的分词结果（只读，不可修改）。
func (s *Snapshot) FieldTokens(docID, field string) []tokenize.Token {
	d, ok := s.docs[docID]
	if !ok {
		return nil
	}
	return d.tokens[field]
}

// DocFieldValue 返回某文档某字段的原始文本。
func (s *Snapshot) DocFieldValue(docID, field string) (string, bool) {
	d, ok := s.docs[docID]
	if !ok {
		return "", false
	}
	v, ok := d.fields[field]
	return v, ok
}

// Fields 返回某文档包含的全部字段名（排序后，保证遍历稳定）。
func (s *Snapshot) Fields(docID string) []string {
	d, ok := s.docs[docID]
	if !ok {
		return nil
	}
	fs := make([]string, 0, len(d.fields))
	for f := range d.fields {
		fs = append(fs, f)
	}
	sort.Strings(fs)
	return fs
}

// Postings 返回指定字段指定词条的 postings；无精确词条时返回 nil。
func (s *Snapshot) Postings(field, term string) []Posting {
	return s.posts[field][term]
}

// Vocabulary 返回指定字段内全部不同词条（排序后，供容错枚举）。
func (s *Snapshot) Vocabulary(field string) []string {
	m := s.posts[field]
	terms := make([]string, 0, len(m))
	for t := range m {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	return terms
}
