package session

import (
	"bufio"
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

func replayEventJournal(dir string, visit func(events.Event)) (uint64, error) {
	return replayEventJournalWithCatalog(dir, nil, visit)
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
