// Package service 把倒排索引、查询引擎与本地持久化组合为一个完整服务。
//
// 提交与一致性：
//   - 写操作（Upsert/Delete）先在内存基于当前快照构造新快照（copy-on-write），
//     再向 WAL 追加记录并等待 fsync；持久化成功后才原子发布新快照。
//     因此“已确认提交”的变更必然已落盘，且发布对查询是原子的。
//   - 查询通过 Current() 获取某一时刻的不可变快照并在其上执行；进行中的查询
//     永远不会看到部分提交，也不受并发更新影响（快照隔离 / MVCC 语义）。
//   - 重启时先加载快照再重放 WAL，重建出与崩溃前已确认状态一致的索引。
package service

import (
	"fmt"
	"sync"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/config"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/index"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/query"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/search"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/store"
)

// Service 是全文检索服务的对外门面。
type Service struct {
	mu     sync.Mutex // 串行化写操作，保证 WAL 序号与快照发布顺序
	cfg    config.Config
	idx    *index.Index
	st     *store.Store
	closed bool
}

// Open 打开（或创建）数据目录下的持久化服务，校验配置并重放历史数据。
func Open(dir string, cfg config.Config) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	st, recs, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	idx := index.New()
	// 重放：按记录顺序应用到索引（内存重建，无需 fsync）。
	for _, r := range recs {
		switch r.Op {
		case store.OpUpsert:
			idx.Apply(&r.Doc, "")
		case store.OpDelete:
			idx.Apply(nil, r.Doc.ID)
		default:
			return nil, fmt.Errorf("恢复时遇到未知操作类型: %d", r.Op)
		}
	}
	return &Service{cfg: cfg, idx: idx, st: st}, nil
}

// validateDoc 校验写入文档的基本约束。
func validateDoc(d model.Document) error {
	if d.ID == "" {
		return fmt.Errorf("文档 ID 不能为空")
	}
	if d.Fields == nil {
		return fmt.Errorf("文档 %q 的 Fields 不能为 nil", d.ID)
	}
	return nil
}

// Upsert 新增或覆盖更新文档；返回后变更已持久化并立即可见。
func (s *Service) Upsert(doc model.Document) error {
	if err := validateDoc(doc); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("服务已关闭")
	}
	next := s.idx.Current().Add(doc)
	// 先持久化（fsync），成功后再发布；失败则不发布，状态停留在提交前。
	if _, err := s.st.Append(store.Record{Op: store.OpUpsert, Doc: doc}); err != nil {
		return err
	}
	s.idx.Commit(next)
	return nil
}

// Delete 删除文档；文档不存在也会记录一条删除（幂等）。
func (s *Service) Delete(id string) error {
	if id == "" {
		return fmt.Errorf("删除时文档 ID 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("服务已关闭")
	}
	next := s.idx.Current().Delete(id)
	if _, err := s.st.Append(store.Record{Op: store.OpDelete, Doc: model.Document{ID: id}}); err != nil {
		return err
	}
	s.idx.Commit(next)
	return nil
}

// QueryResult 是一次查询返回的完整结果（含每个字段的高亮区间）。
type QueryResult struct {
	Hits []search.Hit `json:"hits"`
}

// Query 解析并执行查询字符串。查询运行在某一时刻的一致快照上。
func (s *Service) Query(text string) (*QueryResult, error) {
	n, err := query.Parse(text)
	if err != nil {
		return nil, err
	}
	snap := s.idx.Current()
	eng := search.NewEngine(snap, s.cfg)
	hits := eng.Search(n)
	if hits == nil {
		hits = []search.Hit{}
	}
	return &QueryResult{Hits: hits}, nil
}

// Snapshot 在当前一致快照上执行一个只读函数；fn 执行期间看到的索引不会变化。
func (s *Service) Snapshot(fn func(e *search.Engine) error) error {
	eng := search.NewEngine(s.idx.Current(), s.cfg)
	return fn(eng)
}

// Checkpoint 主动把当前全量状态落为快照并截断 WAL（可用于周期维护）。
func (s *Service) Checkpoint() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("服务已关闭")
	}
	return s.st.SaveSnapshot(s.idx.Current().AllDocuments())
}

// Close 写出快照、持久化并关闭服务。关闭后不再接受读写。
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	// 关闭前做一次快照，保证重启快速且状态完整。
	if err := s.st.SaveSnapshot(s.idx.Current().AllDocuments()); err != nil {
		return err
	}
	s.closed = true
	return s.st.Close()
}
