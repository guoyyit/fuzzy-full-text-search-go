// Package store 提供全文索引的本地持久化：
// 一份周期性/关闭时写出的 JSON 快照（snapshot.json，原子 rename 发布）
// 加上一份只追加的预写日志（wal.log）。
//
// 崩溃安全语义：
//   - 每条 WAL 记录在 append 后执行 fsync，fsync 成功才视为“已确认提交”；
//   - WAL 记录带长度前缀与 CRC32 校验，崩溃留下的半截/损坏尾部在打开时被拒绝
//     或截断（见 Open 的 strict 参数语义），不会破坏此前已确认的数据；
//   - 快照通过 temp 文件 + fsync + rename 原子发布，任何时刻读到的快照要么是
//     上一个完整版本，要么是新版本，不会是中间状态。
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/model"
)

const (
	snapshotName = "snapshot.json"
	walName      = "wal.log"
	tmpSuffix    = ".tmp"
)

// Op 表示一次写操作的类型。
type Op uint8

const (
	OpUpsert Op = 1
	OpDelete Op = 2
)

func (o Op) valid() bool { return o == OpUpsert || o == OpDelete }

// Record 是 WAL 中的一条提交记录。
type Record struct {
	Seq int            `json:"seq"`
	Op  Op             `json:"op"`
	Doc model.Document `json:"doc"`
}

// frame 是 WAL 的磁盘布局（带校验）。
type frameHeader struct {
	Magic uint32 // 固定魔数，防止误判
	Seq   uint64
	Op    uint8
	CRC   uint32 // 对 Payload 的 CRC32
	Len   uint32 // Payload 字节数
}

const (
	magic   uint32 = 0x46534c57 // "FSLW"
	hdrSize        = 4 + 8 + 1 + 4 + 4
)

// payload 是 frame 中 JSON 编码的部分。
type payload struct {
	Seq int            `json:"seq"`
	Op  Op             `json:"op"`
	Doc model.Document `json:"doc"`
}

// Store 管理一个数据目录下的快照与 WAL。
type Store struct {
	dir    string
	wal    *os.File
	seq    int
	closed bool
}

// Open 打开（或创建）数据目录，返回存储句柄与按序重放的已确认记录。
//
// 恢复顺序：先加载 snapshot.json（若存在），再重放 wal.log 中快照之后的全部
// 完整记录。WAL 末尾若存在半截或校验失败的记录（崩溃的典型痕迹），
// 会被安全忽略；一旦中间记录损坏（其后仍有完整记录），返回错误而非静默丢弃，
// 以避免跳过可能已确认的提交。
func Open(dir string) (*Store, []Record, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	snapDocs, snapSeq, snapOK, err := readSnapshot(filepath.Join(dir, snapshotName))
	if err != nil {
		return nil, nil, err
	}

	walPath := filepath.Join(dir, walName)
	recs, lastSeq, tailTruncated, err := readWAL(walPath)
	if err != nil {
		return nil, nil, err
	}
	if tailTruncated {
		// 截断崩溃残留的损坏尾部，使目录回到最近一致状态。
		if err := truncateWAL(walPath, recs); err != nil {
			return nil, nil, err
		}
	}

	replayed := make([]Record, 0, len(snapDocs)+len(recs))
	if snapOK {
		for _, d := range snapDocs {
			replayed = append(replayed, Record{Op: OpUpsert, Doc: d})
		}
	}
	for _, r := range recs {
		replayed = append(replayed, r)
	}

	maxSeq := snapSeq
	if lastSeq > maxSeq {
		maxSeq = lastSeq
	}
	f, err := openWALAppend(walPath)
	if err != nil {
		return nil, nil, err
	}
	st := &Store{dir: dir, wal: f, seq: maxSeq}
	return st, replayed, nil
}

// Append 追加一条记录；分配单调序号并在 fsync 成功后返回（即已确认提交）。
func (st *Store) Append(rec Record) (Record, error) {
	if st.closed {
		return Record{}, errors.New("store 已关闭")
	}
	if !rec.Op.valid() {
		return Record{}, fmt.Errorf("非法 WAL 操作类型: %d", rec.Op)
	}
	st.seq++
	rec.Seq = st.seq

	body, err := json.Marshal(payload{Seq: rec.Seq, Op: rec.Op, Doc: rec.Doc})
	if err != nil {
		return Record{}, fmt.Errorf("编码 WAL 记录失败: %w", err)
	}
	hdr := make([]byte, hdrSize)
	binary.BigEndian.PutUint32(hdr[0:4], magic)
	binary.BigEndian.PutUint64(hdr[4:12], uint64(rec.Seq))
	hdr[12] = byte(rec.Op)
	binary.BigEndian.PutUint32(hdr[13:17], crc32.ChecksumIEEE(body))
	binary.BigEndian.PutUint32(hdr[17:21], uint32(len(body)))

	if err := st.writeFrame(hdr, body); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// writeFrame 执行“追加全部字节 + fsync”，失败时向上返回错误。
// 调用方在错误后不应再假设记录已提交（崩溃恢复会忽略不完整尾部）。
func (st *Store) writeFrame(hdr, body []byte) error {
	if _, err := st.wal.Write(hdr); err != nil {
		return fmt.Errorf("写 WAL 头失败: %w", err)
	}
	if _, err := st.wal.Write(body); err != nil {
		return fmt.Errorf("写 WAL 体失败: %w", err)
	}
	if err := st.wal.Sync(); err != nil {
		return fmt.Errorf("fsync WAL 失败: %w", err)
	}
	return nil
}

// snapshotFile 是快照的磁盘格式。Seq 为生成快照时已并入的最大 WAL 序号。
type snapshotFile struct {
	Seq  int              `json:"seq"`
	Docs []model.Document `json:"docs"`
}

// SaveSnapshot 把当前全量文档原子写为快照并截断 WAL。
// 流程：写 tmp → fsync(tmp) → rename → fsync(目录) → 重开空 WAL → fsync。
// 任一步失败都不会破坏上一个可用快照。
func (st *Store) SaveSnapshot(docs []model.Document) error {
	if st.closed {
		return errors.New("store 已关闭")
	}
	st.seq++
	snapSeq := st.seq

	data, err := json.MarshalIndent(snapshotFile{Seq: snapSeq, Docs: docs}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码快照失败: %w", err)
	}
	final := filepath.Join(st.dir, snapshotName)
	tmp := final + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建快照临时文件失败: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("写快照临时文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync 快照临时文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭快照临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("原子发布快照失败: %w", err)
	}
	if err := syncDir(st.dir); err != nil {
		return err
	}

	// 快照已包含 snapSeq 之前的全部状态，安全截断 WAL。
	if err := st.wal.Close(); err != nil {
		return fmt.Errorf("关闭旧 WAL 失败: %w", err)
	}
	nf, err := os.OpenFile(filepath.Join(st.dir, walName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("重建 WAL 失败: %w", err)
	}
	if err := nf.Sync(); err != nil {
		nf.Close()
		return fmt.Errorf("fsync 新 WAL 失败: %w", err)
	}
	st.wal = nf
	return nil
}

// Close 关闭底层文件句柄（不自动做快照，由上层决定）。
func (st *Store) Close() error {
	if st.closed {
		return nil
	}
	st.closed = true
	if err := st.wal.Sync(); err != nil {
		st.wal.Close()
		return err
	}
	return st.wal.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("打开目录用于 fsync 失败: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync 目录失败: %w", err)
	}
	return nil
}

func openWALAppend(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开 WAL 失败: %w", err)
	}
	return f, nil
}

func readSnapshot(path string) ([]model.Document, int, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("读快照失败: %w", err)
	}
	var sf snapshotFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, 0, false, fmt.Errorf("快照文件已损坏: %w", err)
	}
	return sf.Docs, sf.Seq, true, nil
}

// readWAL 顺序读取全部完整记录。返回记录、最大序号、以及“尾部是否发现
// 不完整/校验失败帧”。中间损坏返回错误。
func readWAL(path string) ([]Record, int, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("打开 WAL 失败: %w", err)
	}
	defer f.Close()

	var recs []Record
	lastSeq := 0
	pos := int64(0)
	tailTruncated := false
	buf := make([]byte, hdrSize)
	for {
		n, err := io.ReadFull(f, buf)
		if errors.Is(err, io.EOF) {
			break // 干净结束
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// 头部不完整：崩溃残留尾部。
			if n > 0 {
				tailTruncated = true
			}
			break
		}
		if err != nil {
			return nil, 0, false, fmt.Errorf("读 WAL 头失败: %w", err)
		}
		hdr := frameHeader{
			Magic: binary.BigEndian.Uint32(buf[0:4]),
			Seq:   binary.BigEndian.Uint64(buf[4:12]),
			Op:    buf[12],
			CRC:   binary.BigEndian.Uint32(buf[13:17]),
			Len:   binary.BigEndian.Uint32(buf[17:21]),
		}
		frameStart := pos
		pos += hdrSize
		if hdr.Magic != magic {
			return nil, 0, false, fmt.Errorf("WAL 魔数不匹配（offset %d）：文件已损坏或不是本服务的日志", frameStart)
		}
		if hdr.Len > 64*1024*1024 {
			return nil, 0, false, fmt.Errorf("WAL 帧长度异常（offset %d, len=%d）", frameStart, hdr.Len)
		}
		body := make([]byte, hdr.Len)
		_, err = io.ReadFull(f, body)
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			tailTruncated = true
			break
		}
		if err != nil {
			return nil, 0, false, fmt.Errorf("读 WAL 体失败: %w", err)
		}
		pos += int64(hdr.Len)
		if crc32.ChecksumIEEE(body) != hdr.CRC {
			// 若后续已无更多数据，按崩溃尾部处理；否则属于中间损坏，报错。
			if isEOF(f) {
				tailTruncated = true
				break
			}
			return nil, 0, false, fmt.Errorf("WAL CRC 校验失败（offset %d）：日志已损坏", frameStart)
		}
		var pl payload
		if err := json.Unmarshal(body, &pl); err != nil {
			if isEOF(f) {
				tailTruncated = true
				break
			}
			return nil, 0, false, fmt.Errorf("WAL 记录解析失败（offset %d）: %w", frameStart, err)
		}
		if !pl.Op.valid() {
			return nil, 0, false, fmt.Errorf("WAL 含非法操作类型（offset %d）", frameStart)
		}
		recs = append(recs, Record{Seq: pl.Seq, Op: pl.Op, Doc: pl.Doc})
		if int(hdr.Seq) > lastSeq {
			lastSeq = int(hdr.Seq)
		}
	}
	return recs, lastSeq, tailTruncated, nil
}

func isEOF(f *os.File) bool {
	pos, _ := f.Seek(0, io.SeekCurrent)
	end, _ := f.Seek(0, io.SeekEnd)
	_, _ = f.Seek(pos, io.SeekStart)
	return pos >= end
}

// truncateWAL 把 WAL 截到仅保留前 len(recs) 个完整帧的长度。
func truncateWAL(path string, recs []Record) error {
	// 通过重新读取确定字节长度：简单起见重放计算。
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开 WAL 以截断失败: %w", err)
	}
	length := int64(0)
	hdr := make([]byte, hdrSize)
	for i := 0; i < len(recs); i++ {
		if _, err := io.ReadFull(f, hdr); err != nil {
			f.Close()
			return fmt.Errorf("重算 WAL 长度失败: %w", err)
		}
		length += hdrSize + int64(binary.BigEndian.Uint32(hdr[17:21]))
		if _, err := f.Seek(int64(binary.BigEndian.Uint32(hdr[17:21])), io.SeekCurrent); err != nil {
			f.Close()
			return err
		}
	}
	f.Close()
	tmp := path + tmpSuffix
	nf, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		nf.Close()
		return err
	}
	if _, err := io.CopyN(nf, src, length); err != nil {
		nf.Close()
		src.Close()
		return err
	}
	src.Close()
	if err := nf.Sync(); err != nil {
		nf.Close()
		return err
	}
	if err := nf.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
