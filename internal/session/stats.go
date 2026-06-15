package session

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/toolevents"
)

type RuntimeStats struct {
	LLMRequests   int `json:"llm_requests"`
	LLMSuccesses  int `json:"llm_successes"`
	ToolRequests  int `json:"tool_requests"`
	ToolSuccesses int `json:"tool_successes"`
}

func (s *Session) RuntimeStats() RuntimeStats {
	if s == nil {
		return RuntimeStats{}
	}
	s.mu.Lock()
	dir := s.Dir
	s.mu.Unlock()
	stats, _ := LoadRuntimeStats(dir)
	return stats
}

func LoadRuntimeStats(dir string) (RuntimeStats, error) {
	file, err := os.Open(filepath.Join(dir, eventsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeStats{}, nil
		}
		return RuntimeStats{}, err
	}
	defer file.Close()

	var stats RuntimeStats
	dec := json.NewDecoder(file)
	for {
		var e events.Event
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return stats, err
		}
		switch e.Type {
		case "llm.requested":
			stats.LLMRequests++
		case "llm.responded":
			stats.LLMSuccesses++
		case toolevents.RequestedType:
			stats.ToolRequests++
		case toolevents.CompletedType:
			stats.ToolSuccesses++
		}
	}
	return stats, nil
}
