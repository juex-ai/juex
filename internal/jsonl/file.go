// Package jsonl provides domain-neutral durable append and bounded reads for
// newline-delimited JSON files.
package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	defaultReadBlockSize = 64 << 10
	defaultWriteBuffer   = 64 << 10
)

var (
	ErrCorrupt       = errors.New("jsonl: corrupt file")
	ErrEmptyBatch    = errors.New("jsonl: append batch is empty")
	ErrInvalidLimit  = errors.New("jsonl: read limit must be positive")
	ErrInvalidOffset = errors.New("jsonl: offset is not a record boundary")
)

// CorruptionError identifies the byte at which a complete invalid record
// begins. Torn final records are repaired separately when the file is opened.
type CorruptionError struct {
	Offset int64
	Err    error
}

func (e *CorruptionError) Error() string {
	return fmt.Sprintf("%v at offset %d: %v", ErrCorrupt, e.Offset, e.Err)
}

func (e *CorruptionError) Unwrap() error { return e.Err }

func (e *CorruptionError) Is(target error) bool { return target == ErrCorrupt }

// Batch is the durable byte range published by one Append call. End is the
// offset immediately after the final newline.
type Batch struct {
	Start int64
	End   int64
	Count int
}

// Record is one validated JSON value. Data excludes the terminating newline;
// End is the byte offset immediately after that newline.
type Record struct {
	Start int64
	End   int64
	Data  json.RawMessage
}

// Page is one reverse-selected page returned in chronological order.
// PreviousEnd can be passed as the next ReadReverse end when HasPrevious is
// true.
type Page struct {
	Records     []Record
	PreviousEnd int64
	HasPrevious bool
}

type fileOps struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	truncate func(*os.File, int64) error
	readAt   func(*os.File, []byte, int64) (int, error)
}

func (ops fileOps) writeBytes(file *os.File, data []byte) (int, error) {
	if ops.write != nil {
		return ops.write(file, data)
	}
	return file.Write(data)
}

func (ops fileOps) syncFile(file *os.File) error {
	if ops.sync != nil {
		return ops.sync(file)
	}
	return file.Sync()
}

func (ops fileOps) truncateFile(file *os.File, size int64) error {
	if ops.truncate != nil {
		return ops.truncate(file, size)
	}
	return file.Truncate(size)
}

func (ops fileOps) readBytesAt(file *os.File, data []byte, offset int64) (int, error) {
	if ops.readAt != nil {
		return ops.readAt(file, data, offset)
	}
	return file.ReadAt(data, offset)
}

// File owns one append-only JSONL file. Its methods serialize access so reads
// observe a committed prefix and appends cannot interleave.
type File struct {
	mu            sync.Mutex
	path          string
	file          *os.File
	size          int64
	readBlockSize int
	ops           fileOps
	closed        bool
}

// Open opens or creates path and durably removes an incomplete final line.
// Complete lines are preserved and validated when read.
func Open(path string) (*File, error) {
	return open(path, fileOps{})
}

func open(path string, ops fileOps) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("jsonl: path is required")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("jsonl: create parent: %w", err)
	}
	target, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("jsonl: open %s: %w", path, err)
	}
	file := &File{
		path:          path,
		file:          target,
		readBlockSize: defaultReadBlockSize,
		ops:           ops,
	}
	if err := file.repairTornTailLocked(); err != nil {
		_ = target.Close()
		return nil, err
	}
	return file, nil
}

// Size returns the current committed byte boundary.
func (f *File) Size() int64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.size
}

// Append validates and compacts records before writing them as one durable
// batch. A failed write, flush, or fsync is rolled back to the prior boundary.
func (f *File) Append(records ...json.RawMessage) (Batch, error) {
	payload, err := encodeBatch(records)
	if err != nil {
		return Batch{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureOpenLocked(); err != nil {
		return Batch{}, err
	}
	if err := f.checkSizeLocked(); err != nil {
		return Batch{}, err
	}
	start := f.size
	if _, err := f.file.Seek(start, io.SeekStart); err != nil {
		return Batch{}, fmt.Errorf("jsonl: seek append boundary %d: %w", start, err)
	}
	writer := bufio.NewWriterSize(opWriter{file: f.file, ops: f.ops}, defaultWriteBuffer)
	written, err := writer.Write(payload)
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = writer.Flush()
	}
	if err != nil {
		return Batch{}, f.rollbackLocked(start, err)
	}
	if err := f.ops.syncFile(f.file); err != nil {
		return Batch{}, f.rollbackLocked(start, err)
	}
	f.size += int64(len(payload))
	return Batch{Start: start, End: f.size, Count: len(records)}, nil
}

// ReadForward visits validated records from start through the committed file
// boundary. It returns the byte boundary after the last record accepted by the
// visitor. The visitor must not call methods on f.
func (f *File) ReadForward(start int64, visit func(Record) error) (int64, error) {
	if visit == nil {
		return start, errors.New("jsonl: visitor is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureOpenLocked(); err != nil {
		return start, err
	}
	if err := f.checkSizeLocked(); err != nil {
		return start, err
	}
	if err := f.validateBoundaryLocked(start); err != nil {
		return start, err
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(f.file, start, f.size-start), defaultReadBlockSize)
	committed := start
	offset := start
	for offset < f.size {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return committed, &CorruptionError{Offset: offset, Err: fmt.Errorf("incomplete record: %w", err)}
		}
		end := offset + int64(len(line))
		data := line[:len(line)-1]
		if err := validateRecord(data, offset); err != nil {
			return committed, err
		}
		record := Record{Start: offset, End: end, Data: append(json.RawMessage(nil), data...)}
		if err := visit(record); err != nil {
			return committed, err
		}
		committed = end
		offset = end
	}
	return committed, nil
}

// ReadReverse selects at most limit records ending at a record boundary. The
// returned records are chronological even though selection reads backward.
// Each underlying read is capped at the configured block size.
func (f *File) ReadReverse(end int64, limit int) (Page, error) {
	if limit <= 0 {
		return Page{}, ErrInvalidLimit
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureOpenLocked(); err != nil {
		return Page{}, err
	}
	if err := f.checkSizeLocked(); err != nil {
		return Page{}, err
	}
	if err := f.validateBoundaryLocked(end); err != nil {
		return Page{}, err
	}
	if end == 0 {
		return Page{}, nil
	}

	position := end
	newlineCount := 0
	var chunks [][]byte
	total := 0
	for position > 0 && newlineCount < limit+1 {
		start := position - int64(f.readBlockSize)
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, int(position-start))
		if err := f.readFullAtLocked(chunk, start); err != nil {
			return Page{}, fmt.Errorf("jsonl: read reverse block at %d: %w", start, err)
		}
		chunks = append(chunks, chunk)
		total += len(chunk)
		newlineCount += bytes.Count(chunk, []byte{'\n'})
		position = start
	}
	suffix := make([]byte, total)
	next := 0
	for i := len(chunks) - 1; i >= 0; i-- {
		next += copy(suffix[next:], chunks[i])
	}

	cut := 0
	if newlineCount > limit {
		remaining := newlineCount - limit
		for index, value := range suffix {
			if value != '\n' {
				continue
			}
			remaining--
			if remaining == 0 {
				cut = index + 1
				break
			}
		}
	}
	base := position + int64(cut)
	records, err := decodePage(suffix[cut:], base)
	if err != nil {
		return Page{}, err
	}
	page := Page{Records: records, HasPrevious: base > 0}
	if page.HasPrevious {
		page.PreviousEnd = base
	}
	return page, nil
}

func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func encodeBatch(records []json.RawMessage) ([]byte, error) {
	if len(records) == 0 {
		return nil, ErrEmptyBatch
	}
	var payload bytes.Buffer
	for index, record := range records {
		if !json.Valid(record) {
			return nil, fmt.Errorf("jsonl: record %d is invalid JSON", index)
		}
		if err := json.Compact(&payload, record); err != nil {
			return nil, fmt.Errorf("jsonl: compact record %d: %w", index, err)
		}
		_ = payload.WriteByte('\n')
	}
	return payload.Bytes(), nil
}

func decodePage(data []byte, base int64) ([]Record, error) {
	if len(data) == 0 {
		return nil, nil
	}
	records := make([]Record, 0, bytes.Count(data, []byte{'\n'}))
	offset := base
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			return nil, &CorruptionError{Offset: offset, Err: errors.New("incomplete record")}
		}
		line := data[:newline]
		if err := validateRecord(line, offset); err != nil {
			return nil, err
		}
		end := offset + int64(newline) + 1
		records = append(records, Record{
			Start: offset,
			End:   end,
			Data:  append(json.RawMessage(nil), line...),
		})
		data = data[newline+1:]
		offset = end
	}
	return records, nil
}

func validateRecord(data []byte, offset int64) error {
	if json.Valid(data) {
		return nil
	}
	return &CorruptionError{Offset: offset, Err: errors.New("invalid JSON record")}
}

func (f *File) ensureOpenLocked() error {
	if f == nil || f.closed || f.file == nil {
		return errors.New("jsonl: file is closed")
	}
	if f.readBlockSize <= 0 {
		return errors.New("jsonl: read block size must be positive")
	}
	return nil
}

func (f *File) checkSizeLocked() error {
	info, err := f.file.Stat()
	if err != nil {
		return fmt.Errorf("jsonl: stat %s: %w", f.path, err)
	}
	if info.Size() != f.size {
		return fmt.Errorf("%w: size changed from %d to %d", ErrCorrupt, f.size, info.Size())
	}
	return nil
}

func (f *File) validateBoundaryLocked(offset int64) error {
	if offset < 0 || offset > f.size {
		return fmt.Errorf("%w: %d outside 0..%d", ErrInvalidOffset, offset, f.size)
	}
	if offset == 0 {
		return nil
	}
	last := []byte{0}
	if err := f.readFullAtLocked(last, offset-1); err != nil {
		return fmt.Errorf("jsonl: read boundary %d: %w", offset, err)
	}
	if last[0] != '\n' {
		return fmt.Errorf("%w: %d", ErrInvalidOffset, offset)
	}
	return nil
}

func (f *File) repairTornTailLocked() error {
	info, err := f.file.Stat()
	if err != nil {
		return fmt.Errorf("jsonl: stat %s: %w", f.path, err)
	}
	f.size = info.Size()
	if f.size == 0 {
		return nil
	}
	last := []byte{0}
	if err := f.readFullAtLocked(last, f.size-1); err != nil {
		return fmt.Errorf("jsonl: inspect final byte: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}
	position := f.size
	truncateAt := int64(0)
	for position > 0 {
		start := position - int64(f.readBlockSize)
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, int(position-start))
		if err := f.readFullAtLocked(chunk, start); err != nil {
			return fmt.Errorf("jsonl: scan torn tail at %d: %w", start, err)
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			truncateAt = start + int64(index) + 1
			break
		}
		position = start
	}
	if err := f.ops.truncateFile(f.file, truncateAt); err != nil {
		return fmt.Errorf("jsonl: truncate torn tail at %d: %w", truncateAt, err)
	}
	if err := f.ops.syncFile(f.file); err != nil {
		return fmt.Errorf("jsonl: sync torn-tail repair at %d: %w", truncateAt, err)
	}
	f.size = truncateAt
	return nil
}

func (f *File) rollbackLocked(offset int64, cause error) error {
	truncateErr := f.ops.truncateFile(f.file, offset)
	_, seekErr := f.file.Seek(offset, io.SeekStart)
	if truncateErr == nil && seekErr == nil {
		truncateErr = f.ops.syncFile(f.file)
	}
	return errors.Join(cause, truncateErr, seekErr)
}

func (f *File) readFullAtLocked(data []byte, offset int64) error {
	written := 0
	for written < len(data) {
		n, err := f.ops.readBytesAt(f.file, data[written:], offset+int64(written))
		written += n
		if err != nil {
			if errors.Is(err, io.EOF) && written == len(data) {
				return nil
			}
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

type opWriter struct {
	file *os.File
	ops  fileOps
}

func (w opWriter) Write(data []byte) (int, error) {
	return w.ops.writeBytes(w.file, data)
}

var _ io.Closer = (*File)(nil)
