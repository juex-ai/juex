package legacy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

const timestampLayout = "2006-01-02T15:04:05.000Z"

// ReadThread captures only settled, complete state. It never repairs a source,
// resumes a Turn, or dispatches an Input. A caller must still stop source writers
// and preserve a verified whole-Fleet snapshot before importing user data.
func ReadThread(dir string) (Thread, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Thread{}, err
	}
	defer func() { _ = root.Close() }()
	r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	return readThread(&r, filepath.Base(filepath.Clean(dir)))
}

func readThread(r *sourceReader, directoryID string) (Thread, error) {
	data, err := r.read("thread.json")
	if err != nil {
		return Thread{}, err
	}
	var result Thread
	if err := decode(data, &result.Metadata); err != nil {
		return Thread{}, fmt.Errorf("thread.json: %w", err)
	}
	if err := result.Metadata.validate(directoryID); err != nil {
		return Thread{}, err
	}
	if err := checkGenerationFiles(r.root, result.Metadata.Generations); err != nil {
		return Thread{}, err
	}
	for i, generation := range result.Metadata.Generations {
		path := "generations/" + generation.ID + ".jsonl"
		data, err := r.read(path)
		if err != nil {
			return Thread{}, err
		}
		commits, err := readCommits(data, generation.ID)
		if err != nil {
			return Thread{}, fmt.Errorf("%s: %w", path, err)
		}
		if commits[0].Seq != generation.BoundarySeq {
			return Thread{}, fmt.Errorf("%s: generation boundary differs from metadata", path)
		}
		for n, commit := range commits {
			if commit.Seq != uint64(len(result.Commits)+1) {
				return Thread{}, fmt.Errorf("%s: noncontiguous sequence %d", path, commit.Seq)
			}
			if err := result.apply(commit, i, n == 0); err != nil {
				return Thread{}, fmt.Errorf("%s: sequence %d: %w", path, commit.Seq, err)
			}
			result.Commits = append(result.Commits, commit)
		}
		if i == len(result.Metadata.Generations)-1 {
			cursor := result.Metadata.EventCursor
			if cursor.GenerationID != generation.ID || cursor.Offset != int64(len(data)) || cursor.Seq != commits[len(commits)-1].Seq {
				return Thread{}, errors.New("thread.json: event cursor differs from complete journal")
			}
		}
	}
	// Archive/unarchive are metadata-only in the source format. A restored
	// idle Worker may still have a terminal turn.failed in its journal.
	if result.executionState == "working" || result.turnCount != result.Metadata.Counts.TurnCount {
		return Thread{}, errors.New("thread.json: lifecycle differs from settled journal")
	}
	if result.Commits[0].At != result.Metadata.CreatedAt || result.Commits[len(result.Commits)-1].At != result.Metadata.LastActivityAt {
		return Thread{}, errors.New("thread.json: timestamps differ from journal")
	}
	if err := llm.ValidateToolTranscript(result.Context); err != nil {
		return Thread{}, fmt.Errorf("unsettled current tool transcript: %w", err)
	}
	if err := result.validateUsageCursor(); err != nil {
		return Thread{}, err
	}
	data, err = r.optionalRead("inputs.json")
	if err != nil {
		return Thread{}, err
	}
	var inputs struct {
		Version int     `json:"v"`
		Records []Input `json:"records"`
	}
	if data == nil {
		inputs.Version = 1
	} else {
		if err := decode(data, &inputs); err != nil {
			return Thread{}, fmt.Errorf("inputs.json: %w", err)
		}
	}
	if inputs.Version != 1 {
		return Thread{}, errors.New("inputs.json: unsupported version")
	}
	seen := make(map[string]bool)
	messages := make(map[string]bool)
	for _, input := range inputs.Records {
		if seen[input.ID] || messages[input.MessageID] {
			return Thread{}, errors.New("inputs.json: invalid or duplicate input")
		}
		if err := input.validate(); err != nil {
			return Thread{}, fmt.Errorf("inputs.json: %w", err)
		}
		seen[input.ID] = true
		messages[input.MessageID] = true
	}
	result.Inputs = inputs.Records
	if err := checkGenerationFiles(r.root, result.Metadata.Generations); err != nil {
		return Thread{}, err
	}
	if err := r.unchanged(); err != nil {
		return Thread{}, err
	}
	result.Files = r.files
	result.AbsentFiles = r.absent
	return result, nil
}

func (m ThreadMetadata) validate(directoryID string) error {
	if m.Version != 1 || m.ThreadID != directoryID || !validThreadID(m.ThreadID) || m.Alias == "" || m.Revision == 0 {
		return errors.New("thread.json: invalid version or identity")
	}
	if m.ThreadID == "0" && (m.Alias != "main" || m.ParentThreadID != "" || m.RetentionState != "active") || m.ThreadID != "0" && (!validThreadID(m.ParentThreadID) || m.ParentThreadID == m.ThreadID) {
		return errors.New("thread.json: invalid Main/Worker relationship")
	}
	if m.RetentionState == "active" {
		if m.ExecutionState != "idle" || m.ArchivedAt != nil {
			return errors.New("thread.json: source Thread is not idle")
		}
	} else if m.RetentionState != "archived" || m.ExecutionState != "" || m.ArchivedAt == nil || !validTimestamp(*m.ArchivedAt) {
		return errors.New("thread.json: invalid archived lifecycle")
	}
	if !validTimestamp(m.CreatedAt) || !validTimestamp(m.UpdatedAt) || !validTimestamp(m.LastActivityAt) || m.Counts.PendingInputCount != 0 || m.Counts.TurnCount < 0 {
		return errors.New("thread.json: invalid timestamps or unsettled input count")
	}
	if m.CreatedAt > m.LastActivityAt || m.LastActivityAt > m.UpdatedAt || m.ArchivedAt != nil && (*m.ArchivedAt < m.CreatedAt || *m.ArchivedAt > m.UpdatedAt) {
		return errors.New("thread.json: timestamps are out of order")
	}
	if err := m.TokenUsage.validate(); err != nil {
		return fmt.Errorf("thread.json: %w", err)
	}
	if m.ContextUsage != nil && m.ContextUsage.CalibratedAt != nil && !validTimestamp(*m.ContextUsage.CalibratedAt) {
		return errors.New("thread.json: invalid calibrated_at")
	}
	if len(m.Generations) == 0 || m.Counts.GenerationCount != len(m.Generations) || m.CurrentGeneration != m.Generations[len(m.Generations)-1] {
		return errors.New("thread.json: inconsistent generations")
	}
	for i, g := range m.Generations {
		if g.Ordinal != i+1 || g.ID != fmt.Sprintf("g%06d", i+1) || g.BoundarySeq == 0 || i == 0 && g.BoundarySeq != 1 || i > 0 && g.BoundarySeq <= m.Generations[i-1].BoundarySeq {
			return errors.New("thread.json: invalid generation order")
		}
	}
	return nil
}

func validThreadID(id string) bool {
	if id == "0" {
		return true
	}
	return len(id) == 6 && strings.IndexFunc(id, func(c rune) bool { return !strings.ContainsRune("0123456789abcdefghjkmnpqrstvwxyz", c) }) == -1
}

func validTimestamp(value string) bool {
	t, err := time.Parse(timestampLayout, value)
	return err == nil && !t.IsZero() && t.Format(timestampLayout) == value
}

func checkGenerationFiles(root *os.Root, generations []Generation) error {
	info, err := root.Lstat("generations")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("generations: expected a directory, not a symlink")
	}
	dir, err := root.Open("generations")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	expected := make(map[string]bool, len(generations))
	for _, g := range generations {
		expected[g.ID+".jsonl"] = true
	}
	if len(entries) != len(expected) {
		return errors.New("generations: file inventory differs from metadata")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] || entry.Type() != 0 {
			return errors.New("generations: unexpected file or symlink")
		}
	}
	return nil
}

func readCommits(data []byte, generation string) ([]Commit, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, errors.New("empty journal or incomplete final line")
	}
	var commits []Commit
	nextIndex, batchCount := 0, 0
	var offset int64
	for lineNumber, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var frame struct {
			Version int             `json:"v"`
			Index   int             `json:"index"`
			Count   int             `json:"count"`
			Data    json.RawMessage `json:"data"`
		}
		if err := decode(line, &frame); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
		if frame.Version != 1 || frame.Count < 1 || frame.Index != nextIndex || frame.Index >= frame.Count || nextIndex > 0 && frame.Count != batchCount {
			return nil, fmt.Errorf("line %d: invalid batch framing", lineNumber+1)
		}
		batchCount, nextIndex = frame.Count, frame.Index+1
		if nextIndex == batchCount {
			nextIndex = 0
		}
		var commit Commit
		if len(frame.Data) > 16<<20 {
			return nil, errors.New("commit exceeds legacy size limit")
		}
		if err := decode(frame.Data, &commit); err != nil {
			return nil, err
		}
		if commit.Version != 1 || !validTimestamp(commit.At) || len(commit.Facts) < 1 || len(commit.Facts) > 64 {
			return nil, errors.New("invalid commit version, timestamp or facts")
		}
		commit.GenerationID = generation
		offset += int64(len(line) + 1)
		commit.EndOffset = offset
		commits = append(commits, commit)
	}
	if nextIndex != 0 {
		return nil, errors.New("incomplete final batch")
	}
	return commits, nil
}

func (t *Thread) apply(commit Commit, generationIndex int, first bool) error {
	for _, fact := range commit.Facts {
		if fact.ThreadID != "" && fact.ThreadID != t.Metadata.ThreadID {
			return errors.New("fact belongs to a different Thread")
		}
		boundary := fact.Type == "thread.created" || fact.Type == "context.renewed" || fact.Type == "context.compacted"
		if boundary != first || boundary && len(commit.Facts) != 1 {
			return errors.New("generation must begin with exactly one boundary fact")
		}
		switch fact.Type {
		case "thread.created":
			if commit.Seq != 1 || fact.ThreadID != t.Metadata.ThreadID || fact.GenerationID != "g000001" || fact.ParentThreadID != t.Metadata.ParentThreadID || fact.Alias == "" || t.Metadata.ThreadID == "0" && fact.Alias != "main" {
				return errors.New("invalid thread.created identity")
			}
			t.ContextScopeID, t.executionState = "g000001", "idle"
		case "message.appended":
			if fact.Message == nil || fact.Message.ID == "" || fact.Message.Role == "" || fact.GenerationID != commit.GenerationID {
				return errors.New("invalid message.appended")
			}
			t.Context = append(t.Context, *fact.Message)
		case "context.renewed", "context.compacted":
			if generationIndex == 0 || fact.FromGenerationID != t.Metadata.Generations[generationIndex-1].ID || fact.ToGenerationID != commit.GenerationID || fact.Seed == nil || fact.Seed.Version != 1 {
				return errors.New("invalid generation transition")
			}
			if fact.Type == "context.renewed" {
				if fact.Seed.ContextScopeID != commit.GenerationID || len(fact.Seed.ProviderMessages) != 0 || fact.Seed.ContextUsage != nil {
					return errors.New("renewed context is not empty")
				}
			} else if err := t.validateCompaction(fact); err != nil {
				return err
			}
			t.Context = append([]llm.Message(nil), fact.Seed.ProviderMessages...)
			t.ContextScopeID = fact.Seed.ContextScopeID
		case "event.recorded":
			var event struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(fact.Event, &event) != nil || event.Type == "" {
				return errors.New("invalid recorded event")
			}
		case "usage.recorded":
			if fact.Usage == nil || fact.TurnID == "" || !validModelRef(fact.ModelRef) || !validUsage(*fact.Usage) {
				return errors.New("invalid usage record")
			}
		case "turn.started":
			t.executionState = "working"
		case "turn.completed", "turn.cancelled":
			t.turnCount++
		case "turn.failed":
			t.turnCount++
			t.executionState = "failed"
		case "thread.settled":
			t.executionState = "idle"
		default:
			return fmt.Errorf("unsupported fact type %q", fact.Type)
		}
	}
	return nil
}

func (t *Thread) validateCompaction(f Fact) error {
	if f.Seed.ContextScopeID != t.ContextScopeID || f.Seed.CompactionCount < 0 || f.Summary == nil || f.Summary.ID == "" || f.Summary.Kind != llm.MessageKindCompact {
		return errors.New("invalid compaction scope or summary")
	}
	expected := []llm.Message{*f.Summary}
	retained := make(map[string]bool)
	if f.Summary.Compaction != nil {
		for _, id := range f.Summary.Compaction.RetainedMessageIDs {
			if id == "" || retained[id] {
				return errors.New("invalid retained message identity")
			}
			retained[id] = true
		}
	}
	for _, message := range t.Context {
		if retained[message.ID] {
			expected = append(expected, message)
			delete(retained, message.ID)
		}
	}
	if len(retained) != 0 || !reflect.DeepEqual(expected, f.Seed.ProviderMessages) {
		return errors.New("compact seed differs from original retained messages")
	}
	return nil
}
