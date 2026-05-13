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
)

// verbosePrinter formats lifecycle events into a human-readable transcript
// with a spinning loading indicator while the LLM is thinking. Unlike a raw
// event dump it foregrounds the things a human cares about: the model's
// thinking content, its text response, each tool call + result, and the
// total turn duration.
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

	p, _ := e.Payload.(map[string]any)

	switch e.Type {
	case "turn.started":
		input, _ := p["input"].(string)
		vp.turn = 0
		vp.tStart = time.Now()
		vp.printlnDim("› user: " + truncOneLine(input, 200))
	case "llm.requested":
		vp.turn++
		vp.printlnDim(fmt.Sprintf("[turn %d]", vp.turn))
		vp.spin.start("thinking")
	case "llm.responded":
		vp.spin.halt()
		if think, _ := p["thinking"].(string); think != "" {
			vp.printIndentedBlock("thinking", think, true)
		}
		if text, _ := p["text"].(string); text != "" {
			vp.printIndentedBlock("assistant", text, false)
		}
		if usage, ok := usageFrom(p["token_usage"]); ok {
			vp.printlnDim("  " + FormatTokenUsage(usage))
		}
	case "tool.requested":
		name, _ := p["name"].(string)
		input := p["input"]
		vp.printlnDim("  → " + name + "(" + oneLineJSON(input) + ")")
	case "tool.completed":
		name, _ := p["name"].(string)
		n := intFrom(p["len"])
		vp.printlnDim(fmt.Sprintf("  ← %s: ok (%d bytes)", name, n))
	case "tool.errored":
		name, _ := p["name"].(string)
		errMsg, _ := p["error"].(string)
		vp.printlnRed(fmt.Sprintf("  ← %s: ERROR %s", name, errMsg))
	case "turn.completed":
		elapsed := time.Since(vp.tStart).Round(time.Millisecond)
		vp.printlnDim(fmt.Sprintf("✓ done in %s", elapsed))
	case "turn.errored":
		errMsg, _ := p["error"].(string)
		vp.printlnRed("✗ " + errMsg)
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

// truncOneLine collapses newlines into spaces and truncates with an
// ellipsis so multi-line user prompts don't blow up the transcript.
func truncOneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// oneLineJSON serialises v to a compact JSON one-liner. Used to render
// tool inputs in the transcript without exploding them across lines.
func oneLineJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

func intFrom(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func usageFrom(v any) (llm.Usage, bool) {
	switch u := v.(type) {
	case llm.Usage:
		return u, true
	case map[string]any:
		return llm.Usage{
			InputTokens:  intFrom(u["input_tokens"]),
			OutputTokens: intFrom(u["output_tokens"]),
		}, true
	}
	return llm.Usage{}, false
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
	stopCh := s.stop
	go s.run(stopCh)
}

func (s *spinner) run(stopCh chan struct{}) {
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
	s.stop = nil
	s.mu.Unlock()
	if ch != nil {
		close(ch)
		// Give the goroutine a moment to clear the line.
		time.Sleep(5 * time.Millisecond)
	}
}
