package legacy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

type MemoryCaller struct {
	FleetID      string   `json:"fleet_id"`
	AgentID      string   `json:"agent_id"`
	ThreadID     string   `json:"thread_id"`
	Profile      string   `json:"profile"`
	Scope        mc.Scope `json:"scope"`
	Purpose      string   `json:"purpose,omitempty"`
	AssignmentID string   `json:"assignment_id,omitempty"`
	Token        string   `json:"token,omitempty"`
}

type MemoryWork struct {
	Receipt      mc.Receipt   `json:"receipt"`
	Proposal     mc.Proposal  `json:"proposal"`
	Caller       MemoryCaller `json:"caller"`
	Fingerprint  string       `json:"fingerprint"`
	DecisionHash string       `json:"decision_hash,omitempty"`
	Executor     string       `json:"executor,omitempty"`
	Token        string       `json:"token,omitempty"`
	Expires      time.Time    `json:"expires,omitempty"`
	RetryAt      time.Time    `json:"retry_at,omitempty"`
	Fence        uint64       `json:"fence"`
	Automatic    bool         `json:"automatic"`
	SourceKey    string       `json:"source_key,omitempty"`
	Through      uint64       `json:"through,omitempty"`
}

type MemorySource struct {
	AcceptedThrough  uint64        `json:"accepted_through"`
	ProcessedThrough uint64        `json:"processed_through"`
	Epoch            string        `json:"epoch"`
	Enabled          bool          `json:"enabled"`
	Caller           MemoryCaller  `json:"caller"`
	Evidence         []mc.Evidence `json:"evidence"`
	EndedGenerations []string      `json:"ended_generations"`
	IdleSince        time.Time     `json:"idle_since"`
	FirstPending     time.Time     `json:"first_pending"`
	Pending          int           `json:"pending"`
	Job              string        `json:"job,omitempty"`
	LiveUntil        time.Time     `json:"live_until"`
}

type MemoryState struct {
	Fleet      string                   `json:"fleet"`
	Strategy   string                   `json:"strategy"`
	Fence      uint64                   `json:"fence"`
	Clock      uint64                   `json:"clock"`
	Access     map[string]uint64        `json:"access"`
	Uses       map[string]uint64        `json:"uses"`
	Requests   map[string]*MemoryWork   `json:"requests"`
	Keys       map[string]string        `json:"keys"`
	Sources    map[string]*MemorySource `json:"sources"`
	Suppressed []mc.Source              `json:"suppressed"`
	Deleted    map[string]bool          `json:"deleted"`
}

type Memory struct {
	State       MemoryState
	Entries     []mc.Entry
	Files       []SourceFile
	AbsentFiles []string
	Skipped     []SkippedSource
}

// ReadMemory never calls the legacy Store.Open: that call publishes pending
// intents and persists state even on a successful read. An unresolved intent
// must be recovered by its original owner before snapshotting, not by import.
func ReadMemory(dir, fleetID string) (Memory, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return Memory{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Memory{}, errors.New("Memory source must be a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Memory{}, err
	}
	defer func() { _ = root.Close() }()
	r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	return readMemory(&r, fleetID)
}

func readMemory(r *sourceReader, fleetID string) (Memory, error) {
	if data, err := r.optionalRead("state/intent.json"); err != nil {
		return Memory{}, err
	} else if data != nil {
		return Memory{}, errors.New("Memory has an unresolved commit intent; source recovery is required")
	}
	data, err := r.read("state/state.json")
	if err != nil {
		return Memory{}, err
	}
	var result Memory
	if err := decode(data, &result.State); err != nil {
		return Memory{}, fmt.Errorf("Memory state: %w", err)
	}
	if err := result.State.validate(fleetID); err != nil {
		return Memory{}, err
	}
	files, err := r.list("memory", false)
	if err != nil {
		return Memory{}, err
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".md") {
			return Memory{}, fmt.Errorf("memory/%s: unknown knowledge file", file.Name())
		}
		data, err := r.read(path.Join("memory", file.Name()))
		if err != nil {
			return Memory{}, err
		}
		entry, err := decodeMemoryEntry(file.Name(), data, result.State, fleetID)
		if err != nil {
			return Memory{}, err
		}
		result.Entries = append(result.Entries, entry)
	}
	stateFiles, err := r.list("state", false)
	if err != nil {
		return Memory{}, err
	}
	for _, file := range stateFiles {
		if file.Name() != "state.json" {
			return Memory{}, fmt.Errorf("state/%s: unknown Memory state file", file.Name())
		}
	}
	entries, err := r.list(".", false)
	if err != nil {
		return Memory{}, err
	}
	for _, entry := range entries {
		switch name := entry.Name(); name {
		case "memory", "state":
		case "desired.json", "MEMORY.md":
			if _, err := r.read(name); err != nil {
				return Memory{}, err
			}
		case "writer.lock", "service.log", "launch.json":
			result.Skipped = append(result.Skipped, SkippedSource{name, "operational service process, lock or log state"})
		default:
			if strings.HasPrefix(name, "ready-") && strings.HasSuffix(name, ".json") {
				result.Skipped = append(result.Skipped, SkippedSource{name, "operational service readiness state"})
			} else {
				return Memory{}, fmt.Errorf("unsupported Memory source path %s", name)
			}
		}
	}
	if err := r.unchanged(); err != nil {
		return Memory{}, err
	}
	result.Files, result.AbsentFiles = r.files, r.absent
	return result, nil
}

func decodeMemoryEntry(name string, data []byte, state MemoryState, fleetID string) (mc.Entry, error) {
	parts := bytes.SplitN(data, []byte("\n---\n"), 2)
	if len(parts) != 2 || !bytes.HasPrefix(parts[0], []byte("---\n")) || !utf8.Valid(data) {
		return mc.Entry{}, fmt.Errorf("memory/%s: invalid entry framing or text", name)
	}
	var entry mc.Entry
	if err := decode(bytes.TrimPrefix(parts[0], []byte("---\n")), &entry); err != nil {
		return mc.Entry{}, err
	}
	if entry.Body != "" {
		return mc.Entry{}, errors.New("Memory entry body conflicts with its Markdown payload")
	}
	entry.Body = string(parts[1])
	if err := mc.ValidateEntryID(entry.ID); err != nil {
		return mc.Entry{}, err
	}
	if entry.ID+".md" != name || entry.Revision == 0 || entry.CreatedAt.IsZero() || entry.UpdatedAt.Before(entry.CreatedAt) || state.Deleted[entry.ID] {
		return mc.Entry{}, fmt.Errorf("memory/%s: inconsistent identity, revision or retention", name)
	}
	if err := validateMemorySources(entry.Sources, fleetID); err != nil {
		return mc.Entry{}, err
	}
	for _, fact := range entry.Facts {
		if err := validateMemorySources(fact.Sources, fleetID); err != nil {
			return mc.Entry{}, err
		}
	}
	return entry, nil
}

func (s MemoryState) validate(fleet string) error {
	if fleet == "" || s.Fleet != fleet || (s.Strategy != mc.Basic && s.Strategy != mc.Advanced) || s.Access == nil || s.Uses == nil || s.Requests == nil || s.Keys == nil || s.Sources == nil || s.Deleted == nil {
		return errors.New("invalid Memory Fleet, strategy or state maps")
	}
	for id, work := range s.Requests {
		if work == nil || id == "" || work.Receipt.ID != id || work.Receipt.Attempts < 0 || work.Receipt.UpdatedAt.IsZero() || work.Fingerprint == "" {
			return errors.New("invalid Memory request receipt")
		}
		switch work.Receipt.State {
		case "applied", "no_change", "rejected", "failed":
		default:
			return errors.New("Memory still has nonterminal review work")
		}
		if err := work.Caller.validate(fleet); err != nil {
			return err
		}
		if err := validateMemorySources(work.Proposal.Sources, fleet); err != nil {
			return err
		}
		for _, e := range work.Proposal.Evidence {
			if err := validateMemorySources([]mc.Source{e.Source}, fleet); err != nil {
				return err
			}
		}
	}
	for key, id := range s.Keys {
		if key == "" || s.Requests[id] == nil {
			return errors.New("Memory request key has no receipt")
		}
	}
	for key, source := range s.Sources {
		if source == nil || key != source.Caller.AgentID+"/"+source.Caller.ThreadID || source.ProcessedThrough > source.AcceptedThrough || source.Pending < 0 {
			return errors.New("invalid Memory source cursor or identity")
		}
		if err := source.Caller.validate(fleet); err != nil {
			return err
		}
		if source.Job != "" {
			return errors.New("Memory source still has a review assignment")
		}
		for _, e := range source.Evidence {
			if err := validateMemorySources([]mc.Source{e.Source}, fleet); err != nil {
				return err
			}
		}
	}
	for id := range s.Deleted {
		if err := mc.ValidateEntryID(id); err != nil {
			return err
		}
	}
	// Legacy no_store ranges may span Generations. Requiring a Generation here
	// would reject valid deletion constraints or narrow them during import.
	for _, ref := range s.Suppressed {
		if ref.FleetID != fleet || ref.AgentID == "" || ref.ThreadID == "" || ref.From == 0 || ref.Through < ref.From {
			return errors.New("invalid Memory no_store range")
		}
	}
	return nil
}

func (c MemoryCaller) validate(fleet string) error {
	if c.FleetID != fleet || c.AgentID == "" || c.ThreadID == "" || (c.Profile != "agent" && c.Profile != "supervisor" && c.Profile != "user") {
		return errors.New("invalid Memory caller provenance")
	}
	return nil
}

func validateMemorySources(sources []mc.Source, fleet string) error {
	for _, source := range sources {
		if err := mc.ValidateSource(source, fleet); err != nil {
			return err
		}
	}
	return nil
}
