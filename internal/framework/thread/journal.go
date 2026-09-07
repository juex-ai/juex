package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
)

const (
	projectionFile    = "thread.json"
	maxCommitBytes    = 16 << 20
	maxFactsPerCommit = 64
)

type scannedCommit struct {
	Commit
	GenerationID string
	StartOffset  int64
	EndOffset    int64
}

func decodeCommit(data []byte, commit *Commit) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(commit); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateCommit(threadID string, commit Commit) error {
	if commit.Version != JournalVersion {
		return fmt.Errorf("unsupported version %d", commit.Version)
	}
	if commit.Seq == 0 {
		return fmt.Errorf("zero sequence")
	}
	if commit.At.IsZero() {
		return fmt.Errorf("zero timestamp")
	}
	if len(commit.Facts) == 0 || len(commit.Facts) > maxFactsPerCommit {
		return fmt.Errorf("fact count %d outside 1..%d", len(commit.Facts), maxFactsPerCommit)
	}
	for index, fact := range commit.Facts {
		if err := validateFactShape(threadID, fact); err != nil {
			return fmt.Errorf("fact %d: %w", index, err)
		}
	}
	if len(commit.Facts) != 1 {
		for _, fact := range commit.Facts {
			if fact.Type == FactThreadCreated || fact.Type == FactContextRenewed || fact.Type == FactContextCompacted {
				return fmt.Errorf("%w: %s must be the only fact in its commit", ErrInvalidFact, fact.Type)
			}
		}
	}
	return nil
}

func validateFactShape(threadID string, fact Fact) error {
	if fact.Type == "" {
		return fmt.Errorf("%w: empty type", ErrInvalidFact)
	}
	if fact.ThreadID != "" && fact.ThreadID != threadID {
		return fmt.Errorf("%w: fact thread %q does not match %q", ErrInvalidFact, fact.ThreadID, threadID)
	}
	switch fact.Type {
	case FactThreadCreated:
		if fact.ThreadID != threadID || fact.GenerationID != InitialGeneration || fact.Alias == "" {
			return fmt.Errorf("%w: invalid thread.created", ErrInvalidFact)
		}
		if threadID == MainID && (fact.Alias != MainAlias || fact.ParentThreadID != "") {
			return fmt.Errorf("%w: invalid Main identity", ErrInvalidFact)
		}
		if threadID != MainID && !ValidID(fact.ParentThreadID) {
			return fmt.Errorf("%w: Worker parent is invalid", ErrInvalidFact)
		}
	case FactMessageAppended:
		if fact.Message == nil || fact.Message.Role == "" || fact.GenerationID == "" {
			return fmt.Errorf("%w: invalid message.appended", ErrInvalidFact)
		}
	case FactEventRecorded:
		if fact.Event == nil || fact.Event.Type == "" {
			return fmt.Errorf("%w: invalid event.recorded", ErrInvalidFact)
		}
	case FactContextRenewed, FactContextCompacted:
		if fact.FromGenerationID == "" || fact.ToGenerationID == "" {
			return fmt.Errorf("%w: generation boundary required", ErrInvalidFact)
		}
		if fact.Seed == nil || fact.Seed.Version != ProjectionVersion {
			return fmt.Errorf("%w: Generation seed required", ErrInvalidFact)
		}
		if fact.Type == FactContextRenewed {
			if len(fact.Seed.ProviderMessages) != 0 || fact.Seed.ContextUsage != nil {
				return fmt.Errorf("%w: renewed Generation context is not empty", ErrInvalidFact)
			}
			break
		}
		if fact.Summary == nil || len(fact.Seed.ProviderMessages) == 0 ||
			!reflect.DeepEqual(*fact.Summary, fact.Seed.ProviderMessages[0]) {
			return fmt.Errorf("%w: compact Generation seed does not begin with its summary", ErrInvalidFact)
		}
	case FactUsageRecorded:
		if fact.Usage == nil || fact.TurnID == "" {
			return fmt.Errorf("%w: usage required", ErrInvalidFact)
		}
		if err := validateModelRef(fact.ModelRef); err != nil {
			return err
		}
		if err := validateUsage(*fact.Usage); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFact, err)
		}
	case FactTurnStarted, FactTurnCompleted, FactTurnFailed, FactTurnCancelled, FactThreadSettled:
	default:
		return fmt.Errorf("%w: unknown fact type %q", ErrInvalidFact, fact.Type)
	}
	return nil
}
