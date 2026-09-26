package fuzzsearch

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

// walFileName is the single append-only log file inside Config.DataDir.
const walFileName = "fuzzy-search.wal"

// wal is an append-only, fsync-durable write-ahead log.
//
// On-disk format (little-endian):
//
//	uint64 length | uint32 CRC32(length || payload) | JSON payload
//
// Each record is one atomic batch. Open reads every intact record; a
// short read, a length/crc mismatch, or invalid JSON can only occur in
// the unsynced tail left by a torn write or a crash, so replay stops
// there and truncates the file to the last intact boundary. Everything
// fsync'd before the crash is therefore preserved, nothing after is
// half-applied.
type wal struct {
	path string
	f    *os.File
	w    *bufio.Writer
}

// writeOp carries unexported fields, so JSON cannot see them; walBatch
// and opDTO are the exported serialization DTOs.
type walBatch struct {
	Ops []opDTO `json:"ops"`
}

type opDTO struct {
	Kind string   `json:"kind"` // "upsert" | "delete"
	ID   string   `json:"id"`
	Doc  Document `json:"doc,omitempty"`
}

func openWAL(dir string) (*wal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: create data dir: %w", err)
	}
	path := filepath.Join(dir, walFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: open: %w", err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: seek: %w", err)
	}
	return &wal{path: path, f: f, w: bufio.NewWriterSize(f, 4096)}, nil
}

// append persists one atomic batch: full frame write, buffer flush and
// fsync happen before it returns, so a successful return means the
// batch survives power loss.
func (w *wal) append(ops []writeOp) error {
	dto := walBatch{}
	for _, op := range ops {
		switch op.kind {
		case opUpsert:
			dto.Ops = append(dto.Ops, opDTO{Kind: "upsert", ID: op.doc.ID, Doc: op.doc})
		case opDelete:
			dto.Ops = append(dto.Ops, opDTO{Kind: "delete", ID: op.id})
		}
	}
	payload, err := json.Marshal(dto)
	if err != nil {
		return fmt.Errorf("wal: encode batch: %w", err)
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint64(hdr[:], uint64(len(payload)))
	frame := make([]byte, 0, 12+len(payload))
	frame = append(frame, hdr[:]...)
	frame = append(frame, payload...)
	// CRC covers length header and payload (everything before the CRC).
	crc := crc32.ChecksumIEEE(frame)
	var crcB [4]byte
	binary.LittleEndian.PutUint32(crcB[:], crc)
	frame = append(frame, crcB[:]...)

	if _, err := w.w.Write(frame); err != nil {
		return err
	}
	if err := w.w.Flush(); err != nil {
		return fmt.Errorf("wal: flush: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("wal: fsync: %w", err)
	}
	return nil
}

// replay feeds every intact batch record to fn in commit order and then
// truncates any damaged tail. It reads the file directly (never through
// a buffering reader, whose readahead would desynchronize the on-disk
// offset used for truncation) and tracks the valid boundary by frame
// lengths.
func (w *wal) replay(fn func(ops []writeOp)) error {
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var validEnd int64
	for {
		var hdr [8]byte
		n, err := io.ReadFull(w.f, hdr[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n > 0 {
				break // torn length header
			}
			break
		}
		if err != nil {
			break
		}
		length := int64(binary.LittleEndian.Uint64(hdr[:]))
		if length <= 0 || length > 1<<28 {
			break // absurd length: corrupt header
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(w.f, payload); err != nil {
			break // torn payload
		}
		var crcB [4]byte
		if _, err := io.ReadFull(w.f, crcB[:]); err != nil {
			break // torn crc
		}
		hdrPayload := make([]byte, 0, 8+length)
		hdrPayload = append(hdrPayload, hdr[:]...)
		hdrPayload = append(hdrPayload, payload...)
		if crc32.ChecksumIEEE(hdrPayload) != binary.LittleEndian.Uint32(crcB[:]) {
			break // corrupt frame
		}
		var dto walBatch
		if err := json.Unmarshal(payload, &dto); err != nil {
			break // corrupt payload
		}
		ops := make([]writeOp, 0, len(dto.Ops))
		bad := false
		for _, d := range dto.Ops {
			switch d.Kind {
			case "upsert":
				if d.Doc.ID == "" || d.Doc.Fields == nil {
					bad = true
				}
				ops = append(ops, writeOp{kind: opUpsert, doc: d.Doc})
			case "delete":
				ops = append(ops, writeOp{kind: opDelete, id: d.ID})
			default:
				bad = true
			}
		}
		if bad {
			break
		}
		fn(ops)
		validEnd += 8 + length + 4
	}
	// Truncate torn tail and move write position back to valid end.
	if err := w.f.Truncate(validEnd); err != nil {
		return fmt.Errorf("wal: truncate torn tail: %w", err)
	}
	if _, err := w.f.Seek(validEnd, io.SeekStart); err != nil {
		return err
	}
	w.w.Reset(w.f)
	return nil
}

func (w *wal) close() error {
	if err := w.w.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}
