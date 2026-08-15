package session

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/events"
)

// Finalized Shell content contains an approximately 1 MiB bounded base plus a
// 128 KiB bounded hook/error suffix. encoding/json expands HTML-sensitive
// runes such as '<' to six-byte escapes, so the reader needs bounded headroom.
const maxEventLineBytes = 8 * 1024 * 1024

// ReadEvents loads the durable event journal for status and replay projections.
func ReadEvents(dir string) ([]events.Event, error) {
	return readEvents(dir, nil)
}

// ReadEventsWithCatalog loads events and applies the stable schema contract to
// each complete journal record before returning it to cross-module consumers.
func ReadEventsWithCatalog(dir string, catalog events.SchemaCatalog) ([]events.Event, error) {
	return readEvents(dir, catalog)
}

// ReadLatestCommittedEventID returns the final durably appended event without
// replaying or repairing the journal. Bytes after the final newline are not a
// complete record and are ignored.
func ReadLatestCommittedEventID(dir string) (string, error) {
	path := filepath.Join(dir, eventsFile)
	guard, err := acquireEventJournalLock(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = guard.Close() }()
	return readLatestCommittedEventID(path, filepath.Base(dir))
}

func readLatestCommittedEventID(path, sessionID string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer file.Close()

	st, err := file.Stat()
	if err != nil {
		return "", err
	}
	lastNewline, err := findLastEventNewline(file, st.Size())
	if err != nil {
		return "", fmt.Errorf("session: locate latest committed event: %w", err)
	}
	if lastNewline < 0 {
		return "", nil
	}
	previousNewline, err := findLastEventNewline(file, lastNewline)
	if err != nil {
		return "", fmt.Errorf("session: locate latest committed event start: %w", err)
	}
	start := previousNewline + 1
	recordBytes := lastNewline + 1 - start
	if recordBytes > int64(maxEventLineBytes) {
		return "", fmt.Errorf("session: read latest events.jsonl record: %w", errEventLineTooLong)
	}
	raw := make([]byte, int(recordBytes))
	if _, err := io.ReadFull(io.NewSectionReader(file, start, recordBytes), raw); err != nil {
		return "", fmt.Errorf("session: read latest events.jsonl record: %w", err)
	}
	line := raw[:len(raw)-1]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	if len(line) == 0 {
		return "", errors.New("session: decode latest events.jsonl record: empty journal record")
	}
	event, _, err := decodeEventJournalLine(line, journalRecordExpectation{
		kind:      journalKindEvents,
		sessionID: sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("session: decode latest events.jsonl record: %w", err)
	}
	if event.ID == "" {
		return "", errors.New("session: latest events.jsonl record has no event id")
	}
	return event.ID, nil
}

func findLastEventNewline(file *os.File, end int64) (int64, error) {
	buf := make([]byte, reverseLineBlockBytes)
	for end > 0 {
		start := max(int64(0), end-int64(len(buf)))
		n, err := file.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if newline := bytes.LastIndexByte(buf[:n], '\n'); newline >= 0 {
			return start + int64(newline), nil
		}
		end = start
	}
	return -1, nil
}

func readEvents(dir string, catalog events.SchemaCatalog) ([]events.Event, error) {
	var result []events.Event
	err := replayEvents(dir, catalog, func(event events.Event) {
		result = append(result, event)
	})
	return result, err
}

// ReplayEvents visits complete committed records in order. An incomplete final
// record is truncated and synced before replay succeeds; corruption in any
// complete record is a hard error and leaves the journal unchanged.
func ReplayEvents(dir string, visit func(events.Event)) error {
	return replayEvents(dir, nil, visit)

}

// ReplayEventsWithCatalog validates and decodes stable payloads through the
// supplied Catalog while preserving opaque ignorable records in journal order.
func ReplayEventsWithCatalog(dir string, catalog events.SchemaCatalog, visit func(events.Event)) error {
	return replayEvents(dir, catalog, visit)
}

func replayEvents(dir string, catalog events.SchemaCatalog, visit func(events.Event)) error {
	_, err := replayEventJournalWithCatalog(dir, catalog, visit)
	return err
}

func replayEventJournalWithCatalog(
	dir string,
	catalog events.SchemaCatalog,
	visit func(events.Event),
) (uint64, error) {
	path := filepath.Join(dir, eventsFile)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	canRepair := true
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		file, err = os.Open(path)
		canRepair = false
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64*1024)
	var validOffset int64
	var sequence uint64
	for line := 1; ; line++ {
		raw, complete, err := readEventLine(reader)
		if err != nil {
			if errors.Is(err, errEventLineTooLong) {
				terminated, drainErr := discardEventLineRemainder(reader)
				if drainErr != nil {
					return sequence, fmt.Errorf("session: read events.jsonl line %d: %w", line, drainErr)
				}
				if !terminated && canRepair {
					if repairErr := truncateJournalTailDurably(file, path, validOffset, journalFileOps{}); repairErr != nil {
						return sequence, errors.Join(err, repairErr)
					}
					return sequence, nil
				}
			}
			return sequence, fmt.Errorf("session: read events.jsonl line %d: %w", line, err)
		}
		if len(raw) == 0 && !complete {
			return sequence, nil
		}
		if !complete {
			if !canRepair {
				return sequence, &tornJournalTailError{path: path, offset: validOffset}
			}
			if err := truncateJournalTailDurably(file, path, validOffset, journalFileOps{}); err != nil {
				return sequence, errors.Join(
					&tornJournalTailError{path: path, offset: validOffset},
					fmt.Errorf("session: repair torn events journal: %w", err),
				)
			}
			return sequence, nil
		}
		encoded := raw[:len(raw)-1]
		if len(encoded) > 0 && encoded[len(encoded)-1] == '\r' {
			encoded = encoded[:len(encoded)-1]
		}
		if len(encoded) == 0 {
			return sequence, fmt.Errorf("session: decode events.jsonl line %d: empty journal record", line)
		}
		event, header, err := decodeEventJournalLine(encoded, journalRecordExpectation{
			kind:      journalKindEvents,
			sessionID: filepath.Base(dir),
			sequence:  sequence + 1,
		})
		if err != nil {
			return sequence, fmt.Errorf("session: decode events.jsonl line %d: %w", line, err)
		}
		if catalog != nil {
			event, err = catalog.Decode(event)
			if err != nil {
				return sequence, fmt.Errorf("session: decode events.jsonl line %d: %w", line, err)
			}
		}
		sequence = header.Sequence
		if visit != nil {
			visit(event)
		}
		validOffset += int64(len(raw))
	}
}

func discardEventLineRemainder(reader *bufio.Reader) (bool, error) {
	for {
		_, err := reader.ReadSlice('\n')
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return false, nil
		default:
			return false, err
		}
	}
}

var errEventLineTooLong = errors.New("event line exceeds 8 MiB")

func readEventLine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxEventLineBytes {
			return nil, false, errEventLineTooLong
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			return line, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return line, false, nil
		default:
			return nil, false, err
		}
	}
}
