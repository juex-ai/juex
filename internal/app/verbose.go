package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	runtimeevents "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/toolevents"
)

// verbosePrinter formats lifecycle events into a human-readable transcript
// with a spinning loading indicator while the LLM is thinking. Unlike a raw
// event dump it foregrounds the things a human cares about: the model's
// thinking content, its text response, compact tool batch status, and the total
// turn duration.
//
// On a non-TTY writer the spinner is suppressed and we print plain status
// lines instead so CI logs stay readable.
type verbosePrinter struct {
	w      io.Writer
	isTTY  bool
	spin   *spinner
	mu     sync.Mutex
	turn   int
	tStart time.Time

	toolBatch       *verboseToolBatch
	lastToolLineKey string
}

func newVerbosePrinter(w io.Writer) *verbosePrinter {
	isTTY := isWriterTTY(w)
	return &verbosePrinter{w: w, isTTY: isTTY, spin: newSpinner(w, isTTY)}
}

func isWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func (vp *verbosePrinter) handle(e events.Event) {
	vp.mu.Lock()
	defer vp.mu.Unlock()

	switch e.Type {
	case "turn.started":
		payload, _ := payloadAs[runtimeevents.TurnStartedPayload](e.Payload)
		vp.turn = 0
		vp.tStart = time.Now()
		vp.printTurnStarted(payload)
	case "llm.requested":
		vp.turn++
		vp.printlnDim(fmt.Sprintf("[turn %d]", vp.turn))
		vp.spin.start("thinking")
	case "llm.responded":
		vp.spin.halt()
		payload, _ := payloadAs[runtimeevents.LLMRespondedPayload](e.Payload)
		if len(payload.Blocks) > 0 {
			vp.printResponseBlocks(payload.Blocks)
		} else {
			if payload.Thinking != "" {
				vp.printIndentedBlock("thinking", payload.Thinking, true)
			}
			if payload.Text != "" {
				vp.printIndentedBlock("assistant", payload.Text, false)
			}
		}
		if payloadFieldPresent(e.Payload, "token_usage") {
			vp.printlnDim("  " + FormatTokenUsage(payload.TokenUsage))
		}
		if len(payload.ToolCalls) > 0 {
			vp.startToolBatch(payload.ToolCalls)
		}
	case toolevents.RequestedType:
		payload, _ := payloadAs[toolevents.RequestedPayload](e.Payload)
		vp.markToolRunning(payload.ToolUseID, payload.Name)
	case toolevents.CompletedType:
		payload, _ := payloadAs[toolevents.CompletedPayload](e.Payload)
		vp.markToolDone(payload.ToolUseID, payload.Name)
	case toolevents.ErroredType:
		payload, _ := payloadAs[toolevents.ErroredPayload](e.Payload)
		vp.markToolFailed(payload.ToolUseID, payload.Name)
	case toolevents.OutputDeltaType:
		payload, _ := payloadAs[toolevents.OutputDeltaPayload](e.Payload)
		vp.markToolOutputDelta(payload.ToolUseID, payload.Name)
	case "pending_input.queued":
		payload, _ := payloadAs[runtimeevents.PendingInputQueuedPayload](e.Payload)
		if payload.MaxPendingInputs > 0 {
			vp.printlnDim(fmt.Sprintf("  + pending input (%d/%d)", payload.PendingCount, payload.MaxPendingInputs))
		} else {
			vp.printlnDim(fmt.Sprintf("  + pending input (%d)", payload.PendingCount))
		}
	case "pending_input.drained":
		payload, _ := payloadAs[runtimeevents.PendingInputDrainedPayload](e.Payload)
		vp.printlnDim(fmt.Sprintf("  + drained %d pending input(s)", payload.Count))
	case "pending_input.dropped":
		payload, _ := payloadAs[runtimeevents.PendingInputDroppedPayload](e.Payload)
		vp.printlnRed(fmt.Sprintf("  + dropped %d pending input(s)", payload.Count))
	case "pending_input.rejected":
		payload, _ := payloadAs[runtimeevents.PendingInputRejectedPayload](e.Payload)
		vp.printlnRed(fmt.Sprintf("  + rejected pending input (%d/%d)", payload.PendingCount, payload.MaxPendingInputs))
	case "turn.completed":
		vp.spin.halt()
		elapsed := time.Since(vp.tStart).Round(time.Millisecond)
		vp.printlnDim(fmt.Sprintf("✓ done in %s", elapsed))
	case "turn.errored":
		vp.spin.halt()
		payload, _ := payloadAs[runtimeevents.TurnErroredPayload](e.Payload)
		vp.printlnRed("✗ " + payload.Error)
	}
}

func (vp *verbosePrinter) printTurnStarted(payload runtimeevents.TurnStartedPayload) {
	input := verboseTurnStartInput(payload.Input, payload.Kind)
	if verboseTurnStartIsEvent(payload.Kind) {
		vp.printlnGold("› event: " + truncOneLine(input, 200))
		return
	}
	vp.printlnDim("› user: " + truncOneLine(input, 200))
}

func verboseTurnStartIsEvent(kind string) bool {
	switch kind {
	case llm.MessageKindMCPEvent, llm.MessageKindObservation:
		return true
	default:
		return false
	}
}

func verboseTurnStartInput(input, kind string) string {
	switch kind {
	case llm.MessageKindObservation:
		if content := jsonContentField(input); content != "" {
			return content
		}
	case llm.MessageKindMCPEvent:
		if content := mcpEventContent(input); content != "" {
			return content
		}
	}
	return input
}

func mcpEventContent(input string) string {
	first := strings.Index(input, ":")
	if first < 0 {
		return ""
	}
	second := strings.Index(input[first+1:], ":")
	if second < 0 {
		return ""
	}
	content := input[first+1+second+1:]
	if preview := jsonContentField(content); preview != "" {
		return preview
	}
	return strings.TrimSpace(content)
}

func jsonContentField(input string) string {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		return ""
	}
	return strings.TrimSpace(body.Content)
}

func payloadAs[T any](v any) (T, bool) {
	var out T
	if v == nil {
		return out, false
	}
	data, err := json.Marshal(v)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, false
	}
	return out, true
}

func payloadFieldPresent(v any, key string) bool {
	if v == nil {
		return false
	}
	if p, ok := v.(map[string]any); ok {
		_, ok := p[key]
		return ok
	}
	return true
}

func (vp *verbosePrinter) printResponseBlocks(blocks []llm.Block) {
	for _, block := range blocks {
		switch block.Type {
		case llm.BlockReasoning:
			text := block.Text
			if block.Redacted {
				text = "[redacted]"
			} else if text == "" {
				text = block.Content
			}
			vp.printIndentedBlock("thinking", text, true)
		case llm.BlockText:
			vp.printIndentedBlock("assistant", block.Text, false)
		}
	}
}

// printIndentedBlock prints a multi-line content block, prefixed with a
// label on the first line and "  ..." continuation on subsequent lines.
// `dim` toggles ANSI dim styling on TTY.
func (vp *verbosePrinter) printIndentedBlock(label, body string, dimAll bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		var prefix string
		if i == 0 {
			prefix = "  " + label + ": "
		} else {
			prefix = "  " + strings.Repeat(" ", len(label)+2)
		}
		if dimAll {
			vp.printlnDim(prefix + line)
		} else {
			fmt.Fprintln(vp.w, prefix+line)
		}
	}
}

func (vp *verbosePrinter) printlnDim(s string) {
	if vp.isTTY {
		fmt.Fprintln(vp.w, "\x1b[2m"+s+"\x1b[0m")
	} else {
		fmt.Fprintln(vp.w, s)
	}
}

func (vp *verbosePrinter) printlnRed(s string) {
	if vp.isTTY {
		fmt.Fprintln(vp.w, "\x1b[31m"+s+"\x1b[0m")
	} else {
		fmt.Fprintln(vp.w, s)
	}
}

func (vp *verbosePrinter) printlnGreen(s string) {
	if vp.isTTY {
		fmt.Fprintln(vp.w, "\x1b[32m"+s+"\x1b[0m")
	} else {
		fmt.Fprintln(vp.w, s)
	}
}

func (vp *verbosePrinter) printlnGold(s string) {
	if vp.isTTY {
		fmt.Fprintln(vp.w, "\x1b[33m"+s+"\x1b[0m")
	} else {
		fmt.Fprintln(vp.w, s)
	}
}

// truncOneLine collapses newlines into spaces and truncates with an
// ellipsis so multi-line user prompts don't blow up the transcript.
func truncOneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ---- spinner ----

// spinner renders an animated braille frame plus a status message on a
// single line, using \r to overwrite. start/stop is reentrant — calling
// start while running just updates the message.
type spinner struct {
	w     io.Writer
	isTTY bool
	mu    sync.Mutex
	stop  chan struct{}
	done  chan struct{}
	msg   string
}

func newSpinner(w io.Writer, isTTY bool) *spinner {
	return &spinner{w: w, isTTY: isTTY}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// start begins (or updates the message of) the spinner. On a non-TTY
// writer it's a no-op; the caller's surrounding text already conveys
// what is in flight.
func (s *spinner) start(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msg = msg
	if !s.isTTY || s.stop != nil {
		return
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stopCh := s.stop
	doneCh := s.done
	go s.run(stopCh, doneCh)
}

func (s *spinner) run(stopCh chan struct{}, doneCh chan struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-stopCh:
			fmt.Fprint(s.w, "\r\x1b[2K") // clear line
			return
		case <-ticker.C:
			s.mu.Lock()
			m := s.msg
			s.mu.Unlock()
			fmt.Fprintf(s.w, "\r\x1b[2K\x1b[2m%s %s\x1b[0m", spinnerFrames[i], m)
			i = (i + 1) % len(spinnerFrames)
		}
	}
}

// halt stops the spinner goroutine if running and clears the line.
// Safe to call multiple times. Named "halt" to avoid clashing with
// spinner.stop the channel field.
func (s *spinner) halt() {
	s.mu.Lock()
	ch := s.stop
	done := s.done
	s.stop = nil
	s.done = nil
	s.mu.Unlock()
	if ch != nil {
		close(ch)
		<-done
	}
}
