package legacy

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func (i Input) validate() error {
	if i.ID == "" || strings.TrimSpace(i.ID) != i.ID || i.MessageID == "" || strings.TrimSpace(i.MessageID) != i.MessageID || i.Message.ID != i.MessageID || i.Message.Role != llm.RoleUser || i.Message.Blocks == nil {
		return errors.New("invalid input or message identity")
	}
	if i.CreatedAt.IsZero() || i.Attempts < 0 || i.TurnID == "" || (i.Origin != "queued" && i.Origin != "turn") {
		return errors.New("invalid input creation, Turn or origin")
	}
	if i.State != "settled" && i.State != "dead_lettered" {
		return fmt.Errorf("input %s is not terminal (%s)", i.ID, i.State)
	}
	if i.State == "settled" && i.LastErrorKind != "" {
		return errors.New("settled input retains an error kind")
	}
	return nil
}

func validUsage(u llm.Usage) bool {
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && u.CachedInputTokens >= 0 && u.CachedInputTokens <= u.InputTokens
}

func validModelRef(ref string) bool {
	provider, model, ok := strings.Cut(ref, ":")
	return ok && provider != "" && model != "" && strings.TrimSpace(provider) == provider && strings.TrimSpace(model) == model
}

func addUsage(a, b llm.Usage) (llm.Usage, error) {
	if !validUsage(a) || !validUsage(b) || b.InputTokens > math.MaxInt-a.InputTokens || b.OutputTokens > math.MaxInt-a.OutputTokens || b.CachedInputTokens > math.MaxInt-a.CachedInputTokens {
		return llm.Usage{}, errors.New("invalid or overflowing usage")
	}
	a.Add(b)
	return a, nil
}

func (u UsageAggregate) validate() error {
	if u.ByModel == nil || !validUsage(u.Total) {
		return errors.New("invalid token usage total or by_model")
	}
	var sum llm.Usage
	for model, usage := range u.ByModel {
		if !validModelRef(model) {
			return errors.New("invalid usage model")
		}
		var err error
		sum, err = addUsage(sum, usage)
		if err != nil {
			return err
		}
	}
	if sum != u.Total {
		return errors.New("token usage total differs from by_model")
	}
	return nil
}

func (t Thread) validateUsageCursor() error {
	cursor := t.Metadata.UsageAggregatedThrough
	if cursor.Seq == 0 || cursor.Seq > uint64(len(t.Commits)) {
		return errors.New("thread.json: invalid usage cursor")
	}
	end := t.Commits[cursor.Seq-1]
	if end.GenerationID != cursor.GenerationID || end.EndOffset != cursor.Offset {
		return errors.New("thread.json: usage cursor differs from journal")
	}
	usage := UsageAggregate{ByModel: map[string]llm.Usage{}}
	for _, c := range t.Commits[:cursor.Seq] {
		for _, f := range c.Facts {
			if f.Type != "usage.recorded" {
				continue
			}
			var err error
			usage.Total, err = addUsage(usage.Total, *f.Usage)
			if err != nil {
				return err
			}
			usage.ByModel[f.ModelRef], err = addUsage(usage.ByModel[f.ModelRef], *f.Usage)
			if err != nil {
				return err
			}
		}
	}
	if !reflect.DeepEqual(usage, t.Metadata.TokenUsage) {
		return errors.New("thread.json: usage aggregate differs from recorded facts")
	}
	return nil
}
