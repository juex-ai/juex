package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

type MemoryAgentBinding struct {
	Scope   application.Scope
	Runtime RuntimeConversion
}

// MemoryBindings supplies fresh owner authority and the same Runtime conversion
// that will be imported. AdvancedSince is the fixed cutover boundary, not now().
type MemoryBindings struct {
	SourceSHA256  string
	Control       application.Control
	AdvancedSince time.Time
	Agents        map[string]MemoryAgentBinding
}

type memoryThread struct {
	scope       application.Scope
	id          string
	generations map[string]int
	commits     map[uint64]legacy.Commit
	spans       map[uint64]SequenceSpan
}

// ConvertMemory preserves the fixed snapshot as inert owner history. It neither
// opens stores nor infers processing success from a Worker or a terminal receipt.
func ConvertMemory(owner application.Scope, source legacy.Fleet, bindings MemoryBindings) (memory.FleetImport, error) {
	if !owner.Valid() || owner.AgentID != "" || source.Memory == nil || source.ID == "" || source.Memory.State.Fleet != source.ID {
		return memory.FleetImport{}, errors.New("source Memory conversion requires a matching source Fleet and target owner")
	}
	threads, err := memoryThreads(owner, source.Agents, bindings.Agents)
	if err != nil {
		return memory.FleetImport{}, err
	}
	encoded, err := json.Marshal(source.Memory)
	if err != nil {
		return memory.FleetImport{}, err
	}
	var original legacy.Memory
	if err := json.Unmarshal(encoded, &original); err != nil {
		return memory.FleetImport{}, err
	}
	value := memory.FleetImport{Source: "juex/281889e5/fleet/" + source.ID + "/memory", SourceSHA256: bindings.SourceSHA256, Control: bindings.Control, Fence: original.State.Fence + 1, Strategy: original.State.Strategy, AdvancedSince: bindings.AdvancedSince, Entries: original.Entries, Suppressed: original.State.Suppressed}
	convert := func(ref mc.Source, suppression bool) (mc.Source, error) {
		thread, ok := threads[ref.AgentID+"/"+ref.ThreadID]
		if !ok || ref.FleetID != source.ID || ref.From == 0 || ref.Through < ref.From {
			return mc.Source{}, errors.New("source Memory source does not identify a captured Thread range")
		}
		first, last := thread.spans[ref.From], thread.spans[ref.Through]
		if first.First < 1 || last.Last < first.First {
			return mc.Source{}, errors.New("source Memory range has no complete Runtime interval")
		}
		generation := ""
		if ref.GenerationID != "" {
			ordinal := thread.generations[ref.GenerationID]
			if ordinal == 0 || thread.commits[ref.From].GenerationID != ref.GenerationID || thread.commits[ref.Through].GenerationID != ref.GenerationID {
				return mc.Source{}, errors.New("source Memory range does not belong to its recorded Generation")
			}
			generation = strconv.Itoa(ordinal)
		} else if !suppression {
			return mc.Source{}, errors.New("source Memory provenance requires a recorded Generation")
		}
		return mc.Source{FleetID: owner.FleetID, AgentID: thread.scope.AgentID, ThreadID: thread.id, GenerationID: generation, From: uint64(first.First), Through: uint64(last.Last)}, nil
	}
	ranges := func(refs []mc.Source, suppression bool) error {
		for i := range refs {
			ref, err := convert(refs[i], suppression)
			if err != nil {
				return err
			}
			refs[i] = ref
		}
		return nil
	}
	evidence := func(records []mc.Evidence) error {
		for i := range records {
			ref, err := convert(records[i].Source, false)
			if err != nil {
				return err
			}
			records[i].Source = ref
		}
		return nil
	}
	for i := range value.Entries {
		e := &value.Entries[i]
		if err := ranges(e.Sources, false); err != nil {
			return memory.FleetImport{}, fmt.Errorf("source Memory entry %s: %w", e.ID, err)
		}
		for j := range e.Facts {
			if err := ranges(e.Facts[j].Sources, false); err != nil {
				return memory.FleetImport{}, err
			}
		}
	}
	retainedKeys := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(original.State.Requests)) {
		work := original.State.Requests[id]
		if work == nil || work.Receipt.ID != id || work.Caller.FleetID != source.ID {
			return memory.FleetImport{}, errors.New("inconsistent Memory review identity")
		}
		imported := memory.ImportedReview{Proposal: work.Proposal, Receipt: work.Receipt, Automatic: work.Automatic, History: memory.ReviewHistory{SourceSHA256: bindings.SourceSHA256, Fingerprint: work.Fingerprint, DecisionHash: work.DecisionHash}}
		if work.Caller.Profile == "user" {
			if work.Caller.AgentID != "user" || work.Caller.ThreadID != "0" || original.State.Keys["admin/"+work.Proposal.Key] != id {
				return memory.FleetImport{}, errors.New("source Memory administration identity is unproven")
			}
			imported.Scope = owner
			retainedKeys["admin/"+work.Proposal.Key] = id
		} else {
			thread, ok := threads[work.Caller.AgentID+"/"+work.Caller.ThreadID]
			if !ok || work.Caller.Profile != "agent" && work.Caller.Profile != "supervisor" {
				return memory.FleetImport{}, errors.New("source Memory review caller has no Runtime binding")
			}
			imported.Scope, imported.ThreadID = thread.scope, thread.id
			key := "proposal/" + work.Caller.AgentID + "/" + work.Caller.ThreadID + "/" + work.Proposal.Key
			if work.Automatic {
				key = work.Proposal.Key
			}
			imported.RetainKey = original.State.Keys[key] == id
			if imported.RetainKey {
				retainedKeys[key] = id
			}
		}
		if err := ranges(work.Proposal.Sources, false); err != nil {
			return memory.FleetImport{}, err
		}
		if err := evidence(work.Proposal.Evidence); err != nil {
			return memory.FleetImport{}, err
		}
		value.Reviews = append(value.Reviews, imported)
	}
	if !maps.Equal(retainedKeys, original.State.Keys) {
		return memory.FleetImport{}, errors.New("source Memory request index disagrees with its receipts")
	}
	for _, key := range slices.Sorted(maps.Keys(original.State.Sources)) {
		s := original.State.Sources[key]
		thread, ok := threads[key]
		if !ok || s == nil || s.Caller.FleetID != source.ID || key != s.Caller.AgentID+"/"+s.Caller.ThreadID || s.Job != "" || s.Pending != 0 {
			return memory.FleetImport{}, errors.New("source Memory source is unsettled or has no Runtime binding")
		}
		accepted, err := thread.cursor(s.AcceptedThrough)
		if err != nil {
			return memory.FleetImport{}, err
		}
		processed, err := thread.cursor(s.ProcessedThrough)
		if err != nil {
			return memory.FleetImport{}, err
		}
		if err := evidence(s.Evidence); err != nil {
			return memory.FleetImport{}, err
		}
		for i, generation := range s.EndedGenerations {
			ordinal := thread.generations[generation]
			if ordinal == 0 {
				return memory.FleetImport{}, errors.New("source Memory source ended an unknown Generation")
			}
			s.EndedGenerations[i] = strconv.Itoa(ordinal)
		}
		value.Sources = append(value.Sources, memory.HistoricalSource{Scope: thread.scope, ThreadID: thread.id, SourceSHA256: bindings.SourceSHA256, Epoch: s.Epoch, Enabled: s.Enabled, AcceptedThrough: accepted, ProcessedThrough: processed, EndedGenerations: s.EndedGenerations, Evidence: s.Evidence})
	}
	for _, id := range slices.Sorted(maps.Keys(original.State.Deleted)) {
		if original.State.Deleted[id] {
			value.Deleted = append(value.Deleted, id)
		}
	}
	if err := ranges(value.Suppressed, true); err != nil {
		return memory.FleetImport{}, err
	}
	if _, err := value.BuildState(owner); err != nil {
		return memory.FleetImport{}, fmt.Errorf("converted Memory import: %w", err)
	}
	return value, nil
}

func (t memoryThread) cursor(old uint64) (uint64, error) {
	if old == 0 {
		return 0, nil
	}
	span := t.spans[old]
	if span.Last < 1 {
		return 0, errors.New("source Memory cursor has no complete Runtime interval")
	}
	return uint64(span.Last), nil
}

func memoryThreads(owner application.Scope, agents []legacy.Agent, bindings map[string]MemoryAgentBinding) (map[string]memoryThread, error) {
	result := map[string]memoryThread{}
	seen := map[string]bool{}
	for _, agent := range agents {
		binding, ok := bindings[agent.Definition.ID]
		s := binding.Scope
		if !ok || !s.Valid() || s.AgentID == "" || seen[s.AgentID] || s.TenantID != owner.TenantID || s.UserID != owner.UserID || s.FleetID != owner.FleetID || binding.Runtime.Import.Source != "juex/281889e5/agent/"+agent.Definition.ID {
			return nil, errors.New("source Memory requires distinct same-owner Runtime bindings")
		}
		seen[s.AgentID] = true
		if err := binding.Runtime.Import.Validate(s.AgentID); err != nil {
			return nil, err
		}
		imported := map[string]managedruntime.ImportedThread{}
		for _, thread := range binding.Runtime.Import.Threads {
			imported[thread.Thread.ID] = thread
		}
		for _, source := range agent.Threads {
			id := source.Metadata.ThreadID
			t := memoryThread{scope: s, id: binding.Runtime.Identities.Threads[id], generations: map[string]int{}, commits: map[uint64]legacy.Commit{}, spans: binding.Runtime.CommitSpans[id]}
			destination, ok := imported[t.id]
			if !ok || len(t.spans) != len(source.Commits) {
				return nil, errors.New("source Memory Thread mapping is incomplete")
			}
			for _, g := range source.Metadata.Generations {
				t.generations[g.ID] = g.Ordinal
			}
			last := int64(0)
			for _, commit := range source.Commits {
				span := t.spans[commit.Seq]
				if span.First != last+1 || span.Last < span.First || span.Last > int64(len(destination.Events)) {
					return nil, errors.New("source Memory Commit mapping has a gap or overlapping interval")
				}
				event := destination.Events[span.First-1]
				data, err := json.Marshal(SourceCommit{id, commit.GenerationID, commit})
				if err != nil || event.Kind != "import.commit" || !bytes.Equal(data, event.Data) || event.Generation != int64(t.generations[commit.GenerationID]) {
					return nil, errors.New("source Memory Commit mapping differs from captured Runtime history")
				}
				for _, e := range destination.Events[span.First:span.Last] {
					if e.Kind != "message.appended" || e.Generation != event.Generation {
						return nil, errors.New("source Memory Commit mapping contains unrelated events")
					}
				}
				if span.Last < int64(len(destination.Events)) && destination.Events[span.Last].Kind == "message.appended" {
					return nil, errors.New("source Memory Commit mapping omits a materialized message")
				}
				t.commits[commit.Seq], last = commit, span.Last
			}
			key := agent.Definition.ID + "/" + id
			if _, exists := result[key]; exists {
				return nil, errors.New("duplicate source Memory Thread")
			}
			result[key] = t
		}
	}
	return result, nil
}
