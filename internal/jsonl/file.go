// Package jsonl provides domain-neutral durable append and bounded reads for
// newline-delimited JSON files. Each physical line carries a logical record
// plus batch-position framing so crash repair never exposes part of a batch.
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

	"github.com/juex-ai/juex/internal/homestore"
)

const (
	diskVersion          = 1
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

// diskRecord frames every logical record with enough domain-neutral batch
// metadata to remove a complete prefix of an interrupted append on reopen.
type diskRecord struct {
	Version int             `json:"v"`
	Index   int             `json:"index"`
	Count   int             `json:"count"`
	Data    json.RawMessage `json:"data"`
}

type decodedRecord struct {
	Record Record
	Disk   diskRecord
}

type fileOps struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	truncate func(*os.File, int64) error
	readAt   func(*os.File, []byte, int64) (int, error)
	syncDir  func(string) error
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

func (ops fileOps) syncDirectory(path string) error {
	if ops.syncDir != nil {
		return ops.syncDir(path)
	}
	return homestore.SyncDir(path)
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
	snapshot      bool
}

// Open opens or creates path, durably removes an incomplete final line, and
// rolls back a complete prefix of an interrupted batch. Complete records are
// otherwise preserved and validated when read.
func Open(path string) (*File, error) {
	return open(path, fileOps{})
}

// OpenSnapshot opens a read-only handle whose visible size is fixed at end.
// The handle remains usable if the path is renamed or unlinked, and ignores
// bytes appended beyond the captured boundary.
func OpenSnapshot(path string, end int64) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("jsonl: path is required")
	}
	if end < 0 {
		return nil, fmt.Errorf("%w: negative snapshot end %d", ErrInvalidOffset, end)
	}
	path = filepath.Clean(path)
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("jsonl: inspect snapshot %s: %w", path, err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: snapshot path is not a regular file: %s", ErrCorrupt, path)
	}
	target, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jsonl: open snapshot %s: %w", path, err)
	}
	targetInfo, err := target.Stat()
	if err != nil {
		_ = target.Close()
		return nil, fmt.Errorf("jsonl: stat snapshot %s: %w", path, err)
	}
	if !os.SameFile(pathInfo, targetInfo) {
		_ = target.Close()
		return nil, fmt.Errorf("%w: snapshot path changed while opening: %s", ErrCorrupt, path)
	}
	file := &File{
		path:          path,
		file:          target,
		size:          end,
		readBlockSize: defaultReadBlockSize,
		snapshot:      true,
	}
	if err := file.checkSizeLocked(); err != nil {
		_ = target.Close()
		return nil, err
	}
	if err := file.validateBoundaryLocked(end); err != nil {
		_ = target.Close()
		return nil, err
	}
	return file, nil
}

func open(path string, ops fileOps) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("jsonl: path is required")
	}
	path = filepath.Clean(path)
	target, created, missingDirs, err := openTarget(path)
	if err != nil {
		return nil, fmt.Errorf("jsonl: open %s: %w", path, err)
	}
	file := &File{
		path:          path,
		file:          target,
		readBlockSize: defaultReadBlockSize,
		ops:           ops,
	}
	if created {
		if err := file.syncCreatedPathLocked(missingDirs); err != nil {
			closeErr := target.Close()
			removeErr := os.Remove(path)
			if removeErr == nil {
				_ = ops.syncDirectory(filepath.Dir(path))
			}
			return nil, errors.Join(err, closeErr, removeErr)
		}
	}
	if err := file.repairTornTailLocked(); err != nil {
		_ = target.Close()
		return nil, err
	}
	if err := file.repairIncompleteBatchLocked(); err != nil {
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

// Append validates and compacts records, adds domain-neutral batch framing,
// and writes them as one durable batch. A failed write, flush, or fsync is
// rolled back to the prior boundary.
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
	if f.snapshot {
		return Batch{}, errors.New("jsonl: snapshot is read-only")
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

// ReadBytesTo returns an exact copy of the file prefix through end.
func (f *File) ReadBytesTo(end int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureOpenLocked(); err != nil {
		return nil, err
	}
	if err := f.checkSizeLocked(); err != nil {
		return nil, err
	}
	if err := f.validateBoundaryLocked(end); err != nil {
		return nil, err
	}
	data := make([]byte, end)
	if err := f.readFullAtLocked(data, 0); err != nil {
		return nil, fmt.Errorf("jsonl: read prefix through %d: %w", end, err)
	}
	return data, nil
}

// ReadForward visits records from start through the committed file boundary.
// Each complete batch is validated before any of its records are visited. It
// returns the byte boundary after the last record accepted by the visitor. The
// visitor must not call methods on f.
func (f *File) ReadForward(start int64, visit func(Record) error) (int64, error) {
	return f.readForwardTo(start, -1, visit)
}

// ReadForwardTo visits records from start through the captured end boundary.
// It is used by higher-level stores that need a stable snapshot while later
// appends may continue beyond end.
func (f *File) ReadForwardTo(start, end int64, visit func(Record) error) (int64, error) {
	return f.readForwardTo(start, end, visit)
}

func (f *File) readForwardTo(start, end int64, visit func(Record) error) (int64, error) {
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
	if end < 0 {
		end = f.size
	}
	if err := f.validateBoundaryLocked(start); err != nil {
		return start, err
	}
	if err := f.validateBoundaryLocked(end); err != nil {
		return start, err
	}
	if end < start {
		return start, fmt.Errorf("%w: end %d is before start %d", ErrInvalidOffset, end, start)
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(f.file, start, end-start), defaultReadBlockSize)
	committed := start
	offset := start
	var previous *diskRecord
	var pending []Record
	for offset < end {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return committed, &CorruptionError{Offset: offset, Err: fmt.Errorf("incomplete record: %w", err)}
		}
		end := offset + int64(len(line))
		disk, err := decodeDiskRecord(line[:len(line)-1], offset)
		if err != nil {
			return committed, err
		}
		if offset == start {
			if err := f.validateForwardStartLocked(start, disk); err != nil {
				return committed, err
			}
		} else if err := validateBatchAdjacent(*previous, disk, offset); err != nil {
			return committed, err
		}
		record := Record{Start: offset, End: end, Data: append(json.RawMessage(nil), disk.Data...)}
		pending = append(pending, record)
		offset = end
		previous = &disk
		if disk.Index != disk.Count-1 {
			continue
		}
		for _, pendingRecord := range pending {
			if err := visit(pendingRecord); err != nil {
				return committed, err
			}
			committed = pendingRecord.End
		}
		pending = nil
	}
	if len(pending) > 0 {
		return committed, validateBatchEnd(*previous, pending[len(pending)-1].Start)
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
	for position > 0 && newlineCount <= limit {
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
	decoded, err := decodePage(suffix[cut:], base)
	if err != nil {
		return Page{}, err
	}
	if err := f.validatePageBoundariesLocked(decoded, base, end); err != nil {
		return Page{}, err
	}
	records := make([]Record, len(decoded))
	for index := range decoded {
		records[index] = decoded[index].Record
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
		var compact bytes.Buffer
		if err := json.Compact(&compact, record); err != nil {
			return nil, fmt.Errorf("jsonl: compact record %d: %w", index, err)
		}
		line, err := json.Marshal(diskRecord{
			Version: diskVersion,
			Index:   index,
			Count:   len(records),
			Data:    json.RawMessage(compact.Bytes()),
		})
		if err != nil {
			return nil, fmt.Errorf("jsonl: encode record %d: %w", index, err)
		}
		_, _ = payload.Write(line)
		_ = payload.WriteByte('\n')
	}
	return payload.Bytes(), nil
}

func decodePage(data []byte, base int64) ([]decodedRecord, error) {
	if len(data) == 0 {
		return nil, nil
	}
	records := make([]decodedRecord, 0, bytes.Count(data, []byte{'\n'}))
	offset := base
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			return nil, &CorruptionError{Offset: offset, Err: errors.New("incomplete record")}
		}
		line := data[:newline]
		disk, err := decodeDiskRecord(line, offset)
		if err != nil {
			return nil, err
		}
		end := offset + int64(newline) + 1
		records = append(records, decodedRecord{
			Record: Record{
				Start: offset,
				End:   end,
				Data:  append(json.RawMessage(nil), disk.Data...),
			},
			Disk: disk,
		})
		data = data[newline+1:]
		offset = end
	}
	return records, nil
}

func decodeDiskRecord(data []byte, offset int64) (diskRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record diskRecord
	if err := decoder.Decode(&record); err != nil {
		return diskRecord{}, &CorruptionError{Offset: offset, Err: fmt.Errorf("invalid record: %w", err)}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return diskRecord{}, &CorruptionError{Offset: offset, Err: fmt.Errorf("invalid record: %w", err)}
	}
	if record.Version != diskVersion {
		return diskRecord{}, &CorruptionError{Offset: offset, Err: fmt.Errorf("unsupported version %d", record.Version)}
	}
	if record.Count <= 0 || record.Index < 0 || record.Index >= record.Count {
		return diskRecord{}, &CorruptionError{
			Offset: offset,
			Err:    fmt.Errorf("invalid batch position %d of %d", record.Index, record.Count),
		}
	}
	if !json.Valid(record.Data) {
		return diskRecord{}, &CorruptionError{Offset: offset, Err: errors.New("invalid JSON data")}
	}
	return record, nil
}

func validateBatchStart(record diskRecord, offset int64) error {
	if record.Index == 0 {
		return nil
	}
	return &CorruptionError{
		Offset: offset,
		Err:    fmt.Errorf("batch starts at position %d of %d", record.Index, record.Count),
	}
}

func validateBatchAdjacent(previous, current diskRecord, currentOffset int64) error {
	if previous.Index == previous.Count-1 {
		if current.Index == 0 {
			return nil
		}
		return &CorruptionError{
			Offset: currentOffset,
			Err: fmt.Errorf(
				"batch position %d of %d follows completed position %d of %d",
				current.Index, current.Count, previous.Index, previous.Count,
			),
		}
	}
	if current.Count == previous.Count && current.Index == previous.Index+1 {
		return nil
	}
	return &CorruptionError{
		Offset: currentOffset,
		Err: fmt.Errorf(
			"batch position %d of %d follows position %d of %d",
			current.Index, current.Count, previous.Index, previous.Count,
		),
	}
}

func validateBatchEnd(record diskRecord, offset int64) error {
	if record.Index == record.Count-1 {
		return nil
	}
	return &CorruptionError{
		Offset: offset,
		Err:    fmt.Errorf("batch ends at position %d of %d", record.Index, record.Count),
	}
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
	if f.snapshot && info.Size() >= f.size {
		return nil
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

func openTarget(path string) (*os.File, bool, []string, error) {
	dir := filepath.Dir(path)
	missingDirs, err := missingParentDirectories(dir)
	if err != nil {
		return nil, false, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, nil, fmt.Errorf("create parent: %w", err)
	}
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		return target, true, missingDirs, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, false, nil, err
	}
	target, err = os.OpenFile(path, os.O_RDWR, 0o600)
	return target, false, nil, err
}

func missingParentDirectories(path string) ([]string, error) {
	var missing []string
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("%s is not a directory", dir)
			}
			return missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return missing, nil
		}
	}
}

func (f *File) syncCreatedPathLocked(missingDirs []string) error {
	if err := f.ops.syncFile(f.file); err != nil {
		return fmt.Errorf("jsonl: sync new file %s: %w", f.path, err)
	}
	dir := filepath.Dir(f.path)
	if err := f.ops.syncDirectory(dir); err != nil {
		return fmt.Errorf("jsonl: sync new file directory %s: %w", dir, err)
	}
	for _, created := range missingDirs {
		parent := filepath.Dir(created)
		if err := f.ops.syncDirectory(parent); err != nil {
			return fmt.Errorf("jsonl: sync created directory parent %s: %w", parent, err)
		}
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

func (f *File) repairIncompleteBatchLocked() error {
	if f.size == 0 {
		return nil
	}
	line, start, err := f.previousLineLocked(f.size)
	if err != nil {
		return err
	}
	last, err := decodeDiskRecord(line, start)
	if err != nil {
		return err
	}
	complete := last.Index == last.Count-1
	batchStart := start
	for want := last.Index - 1; want >= 0; want-- {
		if batchStart == 0 {
			return &CorruptionError{
				Offset: batchStart,
				Err:    fmt.Errorf("batch is missing position %d of %d", want, last.Count),
			}
		}
		line, previousStart, err := f.previousLineLocked(batchStart)
		if err != nil {
			return err
		}
		previous, err := decodeDiskRecord(line, previousStart)
		if err != nil {
			return err
		}
		if previous.Count != last.Count || previous.Index != want {
			return &CorruptionError{
				Offset: previousStart,
				Err: fmt.Errorf(
					"interrupted batch position %d of %d follows %d of %d",
					last.Index, last.Count, previous.Index, previous.Count,
				),
			}
		}
		batchStart = previousStart
	}
	if complete {
		return nil
	}
	if err := f.ops.truncateFile(f.file, batchStart); err != nil {
		return fmt.Errorf("jsonl: truncate interrupted batch at %d: %w", batchStart, err)
	}
	if err := f.ops.syncFile(f.file); err != nil {
		return fmt.Errorf("jsonl: sync interrupted-batch repair at %d: %w", batchStart, err)
	}
	f.size = batchStart
	return nil
}

func (f *File) validatePageBoundariesLocked(records []decodedRecord, start, end int64) error {
	if len(records) == 0 {
		return nil
	}
	for index := 1; index < len(records); index++ {
		if err := validateBatchAdjacent(records[index-1].Disk, records[index].Disk, records[index].Record.Start); err != nil {
			return err
		}
	}
	first := records[0]
	if start == 0 {
		if err := validateBatchStart(first.Disk, first.Record.Start); err != nil {
			return err
		}
	} else {
		line, previousStart, err := f.previousLineLocked(start)
		if err != nil {
			return err
		}
		previous, err := decodeDiskRecord(line, previousStart)
		if err != nil {
			return err
		}
		if err := validateBatchAdjacent(previous, first.Disk, first.Record.Start); err != nil {
			return err
		}
	}
	last := records[len(records)-1]
	if end == f.size {
		return validateBatchEnd(last.Disk, last.Record.Start)
	}
	line, err := f.nextLineLocked(end)
	if err != nil {
		return err
	}
	next, err := decodeDiskRecord(line, end)
	if err != nil {
		return err
	}
	return validateBatchAdjacent(last.Disk, next, end)
}

func (f *File) validateForwardStartLocked(start int64, current diskRecord) error {
	currentStart := start
	for current.Index > 0 {
		if currentStart == 0 {
			return &CorruptionError{
				Offset: currentStart,
				Err:    fmt.Errorf("batch starts at position %d of %d", current.Index, current.Count),
			}
		}
		line, candidateStart, err := f.previousLineLocked(currentStart)
		if err != nil {
			return err
		}
		candidate, err := decodeDiskRecord(line, candidateStart)
		if err != nil {
			return err
		}
		if err := validateBatchAdjacent(candidate, current, currentStart); err != nil {
			return err
		}
		current = candidate
		currentStart = candidateStart
	}
	if currentStart == 0 {
		return validateBatchStart(current, currentStart)
	}
	line, candidateStart, err := f.previousLineLocked(currentStart)
	if err != nil {
		return err
	}
	candidate, err := decodeDiskRecord(line, candidateStart)
	if err != nil {
		return err
	}
	return validateBatchAdjacent(candidate, current, currentStart)
}

func (f *File) previousLineLocked(end int64) ([]byte, int64, error) {
	if end <= 0 || end > f.size {
		return nil, 0, fmt.Errorf("%w: %d outside 1..%d", ErrInvalidOffset, end, f.size)
	}
	newline := []byte{0}
	if err := f.readFullAtLocked(newline, end-1); err != nil {
		return nil, 0, fmt.Errorf("jsonl: read line boundary %d: %w", end, err)
	}
	if newline[0] != '\n' {
		return nil, 0, fmt.Errorf("%w: %d", ErrInvalidOffset, end)
	}
	position := end - 1
	var chunks [][]byte
	total := 0
	startOffset := int64(0)
	for position > 0 {
		start := position - int64(f.readBlockSize)
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, int(position-start))
		if err := f.readFullAtLocked(chunk, start); err != nil {
			return nil, 0, fmt.Errorf("jsonl: read previous line at %d: %w", start, err)
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			chunks = append(chunks, append([]byte(nil), chunk[index+1:]...))
			total += len(chunk) - index - 1
			startOffset = start + int64(index) + 1
			break
		}
		chunks = append(chunks, chunk)
		total += len(chunk)
		position = start
	}
	line := make([]byte, total)
	next := 0
	for index := len(chunks) - 1; index >= 0; index-- {
		next += copy(line[next:], chunks[index])
	}
	return line, startOffset, nil
}

func (f *File) nextLineLocked(start int64) ([]byte, error) {
	if start < 0 || start >= f.size {
		return nil, fmt.Errorf("%w: %d outside 0..%d", ErrInvalidOffset, start, f.size-1)
	}
	position := start
	var line []byte
	for position < f.size {
		end := position + int64(f.readBlockSize)
		if end > f.size {
			end = f.size
		}
		chunk := make([]byte, int(end-position))
		if err := f.readFullAtLocked(chunk, position); err != nil {
			return nil, fmt.Errorf("jsonl: read next line at %d: %w", position, err)
		}
		if index := bytes.IndexByte(chunk, '\n'); index >= 0 {
			line = append(line, chunk[:index]...)
			return line, nil
		}
		line = append(line, chunk...)
		position = end
	}
	return nil, &CorruptionError{Offset: start, Err: errors.New("incomplete record")}
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
