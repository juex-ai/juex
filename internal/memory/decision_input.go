package memory

import (
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

// Tool qualifiers are explicit key/value rows. Open-ended JSON objects are not
// represented consistently by model providers; the business API remains typed.
type QualifierInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type FactInput struct {
	mc.Fact
	Qualifiers []QualifierInput `json:"qualifiers,omitempty"`
}
type EntryInput struct {
	mc.Entry
	Facts []FactInput `json:"facts,omitempty"`
}
type ChangeInput struct {
	Entry            EntryInput `json:"entry"`
	ExpectedRevision uint64     `json:"expected_revision"`
	Delete           bool       `json:"delete,omitempty"`
}
type DecisionInput struct {
	Outcome string        `json:"outcome"`
	Reason  string        `json:"reason"`
	Changes []ChangeInput `json:"changes,omitempty"`
}

func (d DecisionInput) Decision() (mc.Decision, error) {
	value := mc.Decision{Outcome: d.Outcome, Reason: d.Reason}
	for _, change := range d.Changes {
		entry := change.Entry.Entry
		entry.Facts = nil
		for _, input := range change.Entry.Facts {
			fact := input.Fact
			fact.Qualifiers = map[string]string{}
			for _, q := range input.Qualifiers {
				if _, exists := fact.Qualifiers[q.Key]; exists {
					return value, invalid("duplicate qualifier key")
				}
				fact.Qualifiers[q.Key] = q.Value
			}
			entry.Facts = append(entry.Facts, fact)
		}
		value.Changes = append(value.Changes, mc.Change{Entry: entry, ExpectedRevision: change.ExpectedRevision, Delete: change.Delete})
	}
	return value, nil
}
