package app

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/toolevents"
)

type verboseToolState string

const (
	verboseToolRunning verboseToolState = "running"
	verboseToolDone    verboseToolState = "done"
	verboseToolFailed  verboseToolState = "failed"
)

type verboseTool struct {
	ID     string
	Name   string
	Status verboseToolState
}

type verboseToolBatch struct {
	order []string
	byID  map[string]*verboseTool
	next  int
}

func newVerboseToolBatch(calls []toolevents.ToolCallPayload) *verboseToolBatch {
	batch := &verboseToolBatch{byID: map[string]*verboseTool{}}
	for _, call := range calls {
		batch.upsert(call.ToolUseID, call.Name, verboseToolRunning)
	}
	return batch
}

func (b *verboseToolBatch) upsert(toolUseID, name string, status verboseToolState) {
	if b == nil {
		return
	}
	toolUseID = strings.TrimSpace(toolUseID)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	if toolUseID == "" {
		toolUseID = b.matchNameless(name)
		if toolUseID == "" {
			toolUseID = b.fallbackID(name)
		}
	}
	if tool, ok := b.byID[toolUseID]; ok {
		if name != "" {
			tool.Name = name
		}
		tool.Status = status
		return
	}
	b.order = append(b.order, toolUseID)
	b.byID[toolUseID] = &verboseTool{ID: toolUseID, Name: name, Status: status}
}

func (b *verboseToolBatch) fallbackID(name string) string {
	b.next++
	return fmt.Sprintf("__%s_%d", name, b.next)
}

func (b *verboseToolBatch) matchNameless(name string) string {
	for _, id := range b.order {
		tool := b.byID[id]
		if tool == nil || tool.Name != name {
			continue
		}
		if tool.Status == verboseToolRunning {
			return id
		}
	}
	return ""
}

func (b *verboseToolBatch) status() verboseToolState {
	if b == nil || len(b.order) == 0 {
		return verboseToolDone
	}
	failed := false
	for _, id := range b.order {
		tool := b.byID[id]
		if tool == nil {
			continue
		}
		switch tool.Status {
		case verboseToolRunning:
			return verboseToolRunning
		case verboseToolFailed:
			failed = true
		}
	}
	if failed {
		return verboseToolFailed
	}
	return verboseToolDone
}

func (b *verboseToolBatch) summary() string {
	if b == nil || len(b.order) == 0 {
		return "0 tools"
	}
	counts := map[string]int{}
	var names []string
	for _, id := range b.order {
		tool := b.byID[id]
		if tool == nil {
			continue
		}
		if _, ok := counts[tool.Name]; !ok {
			names = append(names, tool.Name)
		}
		counts[tool.Name]++
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%d %s", counts[name], name))
	}
	return strings.Join(parts, ", ")
}

func (vp *verbosePrinter) startToolBatch(calls []toolevents.ToolCallPayload) {
	vp.toolBatch = newVerboseToolBatch(calls)
	vp.lastToolLineKey = ""
	vp.renderToolBatch()
}

func (vp *verbosePrinter) markToolRunning(toolUseID, name string) {
	if vp.toolBatch == nil {
		vp.toolBatch = newVerboseToolBatch(nil)
		vp.lastToolLineKey = ""
	}
	vp.toolBatch.upsert(toolUseID, name, verboseToolRunning)
	vp.renderToolBatch()
}

func (vp *verbosePrinter) markToolOutputDelta(toolUseID, name string) {
	if vp.toolBatch == nil {
		return
	}
	toolUseID = strings.TrimSpace(toolUseID)
	if toolUseID == "" {
		toolUseID = vp.toolBatch.matchNameless(strings.TrimSpace(name))
	}
	if toolUseID == "" {
		return
	}
	tool := vp.toolBatch.byID[toolUseID]
	if tool == nil || tool.Status != verboseToolRunning {
		return
	}
	vp.renderToolBatch()
}

func (vp *verbosePrinter) markToolDone(toolUseID, name string) {
	if vp.toolBatch == nil {
		vp.toolBatch = newVerboseToolBatch(nil)
		vp.lastToolLineKey = ""
	}
	vp.toolBatch.upsert(toolUseID, name, verboseToolDone)
	vp.renderToolBatch()
}

func (vp *verbosePrinter) markToolFailed(toolUseID, name string) {
	if vp.toolBatch == nil {
		vp.toolBatch = newVerboseToolBatch(nil)
		vp.lastToolLineKey = ""
	}
	vp.toolBatch.upsert(toolUseID, name, verboseToolFailed)
	vp.renderToolBatch()
}

func (vp *verbosePrinter) renderToolBatch() {
	if vp.toolBatch == nil {
		return
	}
	status := vp.toolBatch.status()
	summary := vp.toolBatch.summary()
	lineKey := string(status) + ":" + summary
	if status == verboseToolRunning {
		if vp.isTTY {
			vp.spin.start(summary)
			return
		}
		if lineKey == vp.lastToolLineKey {
			return
		}
		vp.lastToolLineKey = lineKey
		vp.printlnDim("  … " + summary)
		return
	}
	vp.spin.halt()
	if lineKey == vp.lastToolLineKey {
		return
	}
	vp.lastToolLineKey = lineKey
	switch status {
	case verboseToolFailed:
		if vp.isTTY {
			vp.printlnRed("  ● " + summary)
		} else {
			fmt.Fprintln(vp.w, "  ● failed "+summary)
		}
	default:
		vp.printlnGreen("  ● " + summary)
	}
}
