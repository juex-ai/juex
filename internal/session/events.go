package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/events"
)

// ReadEvents loads the durable event journal for status and replay projections.
// Lazy sessions may not have created the journal yet.
func ReadEvents(dir string) ([]events.Event, error) {
	file, err := os.Open(filepath.Join(dir, eventsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var result []events.Event
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("session: decode events.jsonl line %d: %w", line, err)
		}
		result = append(result, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("session: read events.jsonl: %w", err)
	}
	return result, nil
}
