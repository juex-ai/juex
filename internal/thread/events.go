package thread

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/events"
)

func ReplayEvents(dir string, visit func(events.Event)) error {
	if visit == nil {
		return nil
	}
	id := filepath.Base(dir)
	file, err := os.OpenFile(filepath.Join(dir, journalFile), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	commits, _, err := scanJournal(file, id, true)
	if err != nil {
		return err
	}
	for _, commit := range commits {
		for _, fact := range commit.Facts {
			if fact.Type == FactEventRecorded && fact.Event != nil {
				visit(*fact.Event)
			}
		}
	}
	return nil
}

func ReadEvents(dir string) ([]events.Event, error) {
	var result []events.Event
	err := ReplayEvents(dir, func(event events.Event) {
		result = append(result, event)
	})
	if err != nil {
		return nil, fmt.Errorf("thread: read events: %w", err)
	}
	return result, nil
}
