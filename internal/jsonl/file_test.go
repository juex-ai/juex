package jsonl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
)

func TestFileAppendsBatchesAndReadsForward(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	first, err := file.Append(
		json.RawMessage("{\n  \"id\": 1\n}"),
		json.RawMessage(`{"id":2,"text":"two"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := file.Append(json.RawMessage(`{"id":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.Start != 0 || first.End <= first.Start || first.Count != 2 {
		t.Fatalf("first batch = %+v", first)
	}
	if second.Start != first.End || second.End != file.Size() || second.Count != 1 {
		t.Fatalf("second batch = %+v, size = %d", second, file.Size())
	}

	var records []Record
	end, err := file.ReadForward(0, func(record Record) error {
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if end != file.Size() {
		t.Fatalf("end = %d, want %d", end, file.Size())
	}
	assertRecordData(t, records, `{"id":1}`, `{"id":2,"text":"two"}`, `{"id":3}`)
	for i := range records {
		if records[i].End <= records[i].Start {
			t.Fatalf("record %d offsets = %d..%d", i, records[i].Start, records[i].End)
		}
		if i > 0 && records[i].Start != records[i-1].End {
			t.Fatalf("record %d starts at %d, previous ended at %d", i, records[i].Start, records[i-1].End)
		}
	}

	var suffix []Record
	end, err = file.ReadForward(records[0].End, func(record Record) error {
		suffix = append(suffix, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if end != file.Size() {
		t.Fatalf("suffix end = %d, want %d", end, file.Size())
	}
	assertRecordData(t, suffix, `{"id":2,"text":"two"}`, `{"id":3}`)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := append(
		mustEncodeBatch(t, json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2,"text":"two"}`)),
		mustEncodeBatch(t, json.RawMessage(`{"id":3}`))...,
	)
	if got := string(data); got != string(want) {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestFileReadsCapturedForwardBoundaryAfterLaterAppend(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	first, err := file.Append(json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Append(json.RawMessage(`{"id":3}`)); err != nil {
		t.Fatal(err)
	}
	var records []Record
	end, err := file.ReadForwardTo(0, first.End, func(record Record) error {
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if end != first.End {
		t.Fatalf("end = %d, want %d", end, first.End)
	}
	assertRecordData(t, records, `{"id":1}`, `{"id":2}`)
}

func TestFileRejectsInvalidAppendBeforeWriting(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Append(json.RawMessage(`{"ok":true}`), json.RawMessage(`not-json`)); err == nil {
		t.Fatal("Append error = nil, want invalid JSON")
	}
	if file.Size() != 0 {
		t.Fatalf("size = %d, want 0", file.Size())
	}
	if _, err := file.Append(); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("empty Append error = %v, want ErrEmptyBatch", err)
	}
}

func TestFileRollsBackPartialWriteAndCanResume(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	wantSize := file.Size()
	injected := errors.New("injected partial write")
	file.ops.write = func(target *os.File, data []byte) (int, error) {
		written, err := target.Write(data[:len(data)/2])
		if err != nil {
			return written, err
		}
		return written, injected
	}
	if _, err := file.Append(json.RawMessage(`{"id":2}`), json.RawMessage(`{"id":3}`)); !errors.Is(err, injected) {
		t.Fatalf("Append error = %v, want injected error", err)
	}
	if file.Size() != wantSize {
		t.Fatalf("resident size = %d, want %d", file.Size(), wantSize)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != wantSize {
		t.Fatalf("disk size = %d, want %d", info.Size(), wantSize)
	}

	file.ops.write = nil
	if _, err := file.Append(json.RawMessage(`{"id":4}`)); err != nil {
		t.Fatal(err)
	}
	var records []Record
	if _, err := file.ReadForward(0, func(record Record) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertRecordData(t, records, `{"id":1}`, `{"id":4}`)
}

func TestFileRollsBackSyncFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	wantSize := file.Size()
	injected := errors.New("injected sync failure")
	calls := 0
	file.ops.sync = func(target *os.File) error {
		calls++
		if calls == 1 {
			return injected
		}
		return target.Sync()
	}
	if _, err := file.Append(json.RawMessage(`{"id":2}`)); !errors.Is(err, injected) {
		t.Fatalf("Append error = %v, want injected error", err)
	}
	if calls != 2 {
		t.Fatalf("sync calls = %d, want append and rollback sync", calls)
	}
	if file.Size() != wantSize {
		t.Fatalf("resident size = %d, want %d", file.Size(), wantSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := mustEncodeBatch(t, json.RawMessage(`{"id":1}`))
	if got := string(data); got != string(want) {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestFileRollsBackZeroProgressWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	file.ops.write = func(*os.File, []byte) (int, error) { return 0, nil }
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Append error = %v, want io.ErrShortWrite", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Size() != 0 || info.Size() != 0 {
		t.Fatalf("sizes = resident %d disk %d, want 0", file.Size(), info.Size())
	}
}

func TestFileReportsRollbackFailure(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	writeErr := errors.New("write failed")
	truncateErr := errors.New("truncate failed")
	file.ops.write = func(target *os.File, data []byte) (int, error) {
		written, err := target.Write(data[:1])
		if err != nil {
			return written, err
		}
		return written, writeErr
	}
	file.ops.truncate = func(*os.File, int64) error { return truncateErr }
	_, err = file.Append(json.RawMessage(`{"id":1}`))
	if !errors.Is(err, writeErr) || !errors.Is(err, truncateErr) {
		t.Fatalf("Append error = %v, want write and truncate failures", err)
	}
}

func TestOpenRepairsOnlyTornFinalLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	want := mustEncodeBatch(t, json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`))
	if err := os.WriteFile(path, append(append([]byte(nil), want...), []byte(`{"id":3`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Size() != int64(len(want)) {
		t.Fatalf("size = %d, want %d", file.Size(), len(want))
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repaired file = %q, want %q", got, want)
	}
}

func TestOpenReportsTornTailRepairFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ops  fileOps
		want error
	}{
		{
			name: "truncate",
			want: errors.New("truncate repair failed"),
		},
		{
			name: "sync",
			want: errors.New("sync repair failed"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte("{\"id\":1}\n{\"id\":2"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch test.name {
			case "truncate":
				test.ops.truncate = func(*os.File, int64) error { return test.want }
			case "sync":
				test.ops.sync = func(*os.File) error { return test.want }
			}
			file, err := open(path, test.ops)
			if file != nil {
				_ = file.Close()
				t.Fatal("open file is non-nil after repair failure")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Open error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOpenSyncsNewFileAndParentDirectoryEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "first", "second", "events.jsonl")
	var synced []string
	file, err := open(path, fileOps{
		syncDir: func(path string) error {
			synced = append(synced, path)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "first", "second"),
		filepath.Join(root, "first"),
		root,
	}
	if !reflect.DeepEqual(synced, want) {
		t.Fatalf("synced directories = %#v, want %#v", synced, want)
	}
}

func TestOpenReportsNewFileDirectorySyncFailure(t *testing.T) {
	t.Parallel()
	injected := errors.New("sync directory failed")
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")
	file, err := open(path, fileOps{
		syncDir: func(string) error { return injected },
	})
	if file != nil {
		_ = file.Close()
		t.Fatal("open file is non-nil after directory sync failure")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("Open error = %v, want injected error", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("created file remains after failed durability sync: %v", statErr)
	}
}

func TestOpenDiscardsCompletePrefixOfInterruptedBatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	wantSize := file.Size()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := encodeBatch([]json.RawMessage{
		json.RawMessage(`{"id":2}`),
		json.RawMessage(`{"id":3}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstEnd := bytes.IndexByte(payload, '\n') + 1
	appendBytes(t, path, payload[:firstEnd])

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.Size() != wantSize {
		t.Fatalf("repaired size = %d, want %d", reopened.Size(), wantSize)
	}
	var records []Record
	if _, err := reopened.ReadForward(0, func(record Record) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertRecordData(t, records, `{"id":1}`)
}

func TestOpenDiscardsInterruptedBatchAfterTornRecord(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	wantSize := file.Size()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := encodeBatch([]json.RawMessage{
		json.RawMessage(`{"id":2}`),
		json.RawMessage(`{"id":3}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstEnd := bytes.IndexByte(payload, '\n') + 1
	appendBytes(t, path, payload[:firstEnd+3])

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.Size() != wantSize {
		t.Fatalf("repaired size = %d, want %d", reopened.Size(), wantSize)
	}
}

func TestReadersRejectCompleteMalformedRecordWithoutRepairingIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	first := mustEncodeBatch(t, json.RawMessage(`{"id":1}`))
	last := mustEncodeBatch(t, json.RawMessage(`{"id":2}`))
	body := append(append(append([]byte(nil), first...), []byte("not-json\n")...), last...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	visits := 0
	_, err = file.ReadForward(0, func(Record) error {
		visits++
		return nil
	})
	assertCorruptionOffset(t, err, int64(len(first)))
	if visits != 1 {
		t.Fatalf("visitor calls = %d, want one complete batch before malformed record", visits)
	}
	_, err = file.ReadReverse(file.Size(), 2)
	assertCorruptionOffset(t, err, int64(len(first)))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, body) {
		t.Fatalf("file was changed: %q", got)
	}
}

func TestReadersRejectBatchPositionGapAcrossPageBoundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	first := mustEncodeDiskRecord(t, diskRecord{
		Version: diskVersion,
		Index:   0,
		Count:   3,
		Data:    json.RawMessage(`{"id":1}`),
	})
	last := mustEncodeDiskRecord(t, diskRecord{
		Version: diskVersion,
		Index:   2,
		Count:   3,
		Data:    json.RawMessage(`{"id":3}`),
	})
	tail := mustEncodeBatch(t, json.RawMessage(`{"id":4}`))
	body := append(append(append([]byte(nil), first...), last...), tail...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	visits := 0
	_, err = file.ReadForward(0, func(Record) error {
		visits++
		return nil
	})
	assertCorruptionOffset(t, err, int64(len(first)))
	if visits != 0 {
		t.Fatalf("visitor calls = %d, want 0 before malformed batch is rejected", visits)
	}
	_, err = file.ReadForward(int64(len(first)), func(Record) error { return nil })
	assertCorruptionOffset(t, err, int64(len(first)))
	_, err = file.ReadReverse(file.Size(), 2)
	assertCorruptionOffset(t, err, int64(len(first)))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("file was changed: %q", got)
	}
}

func TestReadersRejectBatchThatStartsMidSequence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	body := mustEncodeDiskRecord(t, diskRecord{
		Version: diskVersion,
		Index:   1,
		Count:   2,
		Data:    json.RawMessage(`{"id":2}`),
	})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err == nil {
		_ = file.Close()
		t.Fatal("Open error = nil, want interrupted leading batch corruption")
	}
	assertCorruptionOffset(t, err, 0)
}

func TestReadReversePagesFromEOFWithBoundedReads(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var input []json.RawMessage
	var want []string
	for i := 0; i < 10; i++ {
		value := fmt.Sprintf(`{"id":%d,"text":%q}`, i, strings.Repeat("x", i))
		input = append(input, json.RawMessage(value))
		var compact any
		if err := json.Unmarshal([]byte(value), &compact); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(compact)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, string(encoded))
	}
	if _, err := file.Append(input...); err != nil {
		t.Fatal(err)
	}

	file.readBlockSize = 7
	maxRead := 0
	file.ops.readAt = func(target *os.File, data []byte, offset int64) (int, error) {
		if len(data) > maxRead {
			maxRead = len(data)
		}
		return target.ReadAt(data, offset)
	}
	end := file.Size()
	var pages [][]string
	for {
		page, err := file.ReadReverse(end, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) > 3 {
			t.Fatalf("page has %d records", len(page.Records))
		}
		var values []string
		for _, record := range page.Records {
			values = append(values, string(record.Data))
		}
		pages = append(pages, values)
		if !page.HasPrevious {
			break
		}
		if page.PreviousEnd != page.Records[0].Start {
			t.Fatalf("previous end = %d, first start = %d", page.PreviousEnd, page.Records[0].Start)
		}
		end = page.PreviousEnd
	}
	var got []string
	for i := len(pages) - 1; i >= 0; i-- {
		got = append(got, pages[i]...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	if maxRead > file.readBlockSize {
		t.Fatalf("largest ReadAt = %d, block size = %d", maxRead, file.readBlockSize)
	}
}

func TestReadersSupportRecordLargerThanReadBlock(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	encoded, err := json.Marshal(map[string]string{"text": strings.Repeat("x", 256<<10)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Append(encoded); err != nil {
		t.Fatal(err)
	}
	var forward []Record
	if _, err := file.ReadForward(0, func(record Record) error {
		forward = append(forward, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertRecordData(t, forward, string(encoded))
	page, err := file.ReadReverse(file.Size(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordData(t, page.Records, string(encoded))
}

func TestReadOffsetsMustBeRecordBoundaries(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := file.ReadForward(1, func(Record) error { return nil }); !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("ReadForward error = %v, want ErrInvalidOffset", err)
	}
	if _, err := file.ReadReverse(1, 1); !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("ReadReverse error = %v, want ErrInvalidOffset", err)
	}
	if _, err := file.ReadReverse(file.Size(), 0); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("ReadReverse error = %v, want ErrInvalidLimit", err)
	}
}

func TestReadReverseAcceptsMaximumLimitWithoutOverflow(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Append(json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`)); err != nil {
		t.Fatal(err)
	}
	page, err := file.ReadReverse(file.Size(), math.MaxInt)
	if err != nil {
		t.Fatal(err)
	}
	if page.HasPrevious || page.PreviousEnd != 0 {
		t.Fatalf("page cursor = previous:%t end:%d", page.HasPrevious, page.PreviousEnd)
	}
	assertRecordData(t, page.Records, `{"id":1}`, `{"id":2}`)
}

func TestReversePagingRoundTripProperty(t *testing.T) {
	root := t.TempDir()
	caseNumber := 0
	property := func(values []uint8, requestedPageSize uint8) bool {
		caseNumber++
		file, err := Open(filepath.Join(root, strconv.Itoa(caseNumber), "events.jsonl"))
		if err != nil {
			return false
		}
		defer func() { _ = file.Close() }()
		input := make([]json.RawMessage, len(values))
		want := make([]string, len(values))
		for i, value := range values {
			want[i] = strconv.Itoa(int(value))
			input[i] = json.RawMessage(want[i])
		}
		if len(input) > 0 {
			if _, err := file.Append(input...); err != nil {
				return false
			}
		}
		pageSize := int(requestedPageSize%11) + 1
		end := file.Size()
		var pages [][]string
		for {
			page, err := file.ReadReverse(end, pageSize)
			if err != nil {
				return false
			}
			values := make([]string, len(page.Records))
			for i, record := range page.Records {
				values[i] = string(record.Data)
			}
			pages = append(pages, values)
			if !page.HasPrevious {
				break
			}
			end = page.PreviousEnd
		}
		var got []string
		for i := len(pages) - 1; i >= 0; i-- {
			got = append(got, pages[i]...)
		}
		return slices.Equal(got, want)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestReadForwardReturnsVisitorErrorAtLastCommittedBoundary(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Append(json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`)); err != nil {
		t.Fatal(err)
	}
	payload := mustEncodeBatch(t, json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`))
	injected := errors.New("stop")
	end, err := file.ReadForward(0, func(record Record) error {
		if string(record.Data) == `{"id":2}` {
			return injected
		}
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("ReadForward error = %v, want injected", err)
	}
	wantEnd := int64(bytes.IndexByte(payload, '\n') + 1)
	if end != wantEnd {
		t.Fatalf("end = %d, want first record boundary", end)
	}
}

func TestReadReverseReportsReadFailure(t *testing.T) {
	t.Parallel()
	file, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Append(json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	file.ops.readAt = func(*os.File, []byte, int64) (int, error) { return 0, io.ErrUnexpectedEOF }
	if _, err := file.ReadReverse(file.Size(), 1); err == nil {
		t.Fatal("ReadReverse error = nil, want read failure")
	}
}

func assertRecordData(t *testing.T, records []Record, want ...string) {
	t.Helper()
	got := make([]string, len(records))
	for i, record := range records {
		got[i] = string(record.Data)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func assertCorruptionOffset(t *testing.T, err error, want int64) {
	t.Helper()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error = %v, want ErrCorrupt", err)
	}
	var corruption *CorruptionError
	if !errors.As(err, &corruption) {
		t.Fatalf("error = %T %v, want *CorruptionError", err, err)
	}
	if corruption.Offset != want {
		t.Fatalf("corruption offset = %d, want %d", corruption.Offset, want)
	}
}

func appendBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustEncodeBatch(t *testing.T, records ...json.RawMessage) []byte {
	t.Helper()
	payload, err := encodeBatch(records)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustEncodeDiskRecord(t *testing.T, record diskRecord) []byte {
	t.Helper()
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}
