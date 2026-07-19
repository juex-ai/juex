package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/events"
)

const maxEventLineBytes = 4 * 1024 * 1024

// ReadEvents loads the durable event journal for status and replay projections.
// A corrupt suffix is truncated after the valid prefix before the error is
// returned, so later appends remain replayable. Lazy sessions may not have
// created the journal yet.
func ReadEvents(dir string) ([]events.Event, error) {
	path := filepath.Join(dir, eventsFile)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	canRepair := true
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		file, err = os.Open(path)
		canRepair = false
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var result []events.Event
	reader := bufio.NewReaderSize(file, 64*1024)
	var validOffset int64
	for line := 1; ; line++ {
		raw, complete, err := readEventLine(reader)
		if err != nil {
			readErr := fmt.Errorf("session: read events.jsonl line %d: %w", line, err)
			if canRepair && errors.Is(err, errEventLineTooLong) {
				readErr = repairEventJournalTail(file, validOffset, readErr)
			}
			return result, readErr
		}
		if len(raw) == 0 && !complete {
			return result, nil
		}
		encoded := raw
		if complete {
			encoded = encoded[:len(encoded)-1]
			if len(encoded) > 0 && encoded[len(encoded)-1] == '\r' {
				encoded = encoded[:len(encoded)-1]
			}
		}
		var event events.Event
		if err := json.Unmarshal(encoded, &event); err != nil {
			decodeErr := fmt.Errorf("session: decode events.jsonl line %d: %w", line, err)
			if canRepair {
				decodeErr = repairEventJournalTail(file, validOffset, decodeErr)
			}
			return result, decodeErr
		}
		result = append(result, event)
		validOffset += int64(len(raw))
		if !complete {
			if canRepair {
				if _, err := file.WriteAt([]byte{'\n'}, validOffset); err != nil {
					return result, fmt.Errorf("session: terminate events.jsonl line %d: %w", line, err)
				}
				if err := file.Sync(); err != nil {
					return result, fmt.Errorf("session: sync events.jsonl line %d repair: %w", line, err)
				}
			}
			return result, nil
		}
	}
}

var errEventLineTooLong = errors.New("event line exceeds 4 MiB")

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

func repairEventJournalTail(file *os.File, validOffset int64, cause error) error {
	if err := file.Truncate(validOffset); err != nil {
		return errors.Join(cause, fmt.Errorf("session: truncate corrupt events.jsonl tail: %w", err))
	}
	if err := file.Sync(); err != nil {
		return errors.Join(cause, fmt.Errorf("session: sync repaired events.jsonl: %w", err))
	}
	return fmt.Errorf("%w; repaired corrupt tail at byte %d", cause, validOffset)
}
