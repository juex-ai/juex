package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

const pickerMaxAttempts = 3

// pickSession prints a numbered list to out, reads a line from in, and
// returns the chosen session id. Empty input, "q", or 3 invalid attempts
// in a row return an error so the caller can exit cleanly.
func pickSession(in io.Reader, out io.Writer, infos []session.Info) (string, error) {
	if len(infos) == 0 {
		return "", errors.New("no sessions to choose from")
	}
	fmt.Fprintln(out, "juex sessions — pick one to resume:")
	fmt.Fprintln(out)
	for i, s := range infos {
		fmt.Fprintf(out, "  %d) %s   %s   %s\n",
			i+1, s.ID, humanAgo(s.LastActiveAt), truncateRunes(s.Preview, 60))
	}
	fmt.Fprintln(out)
	scanner := bufio.NewScanner(in)
	for attempt := 0; attempt < pickerMaxAttempts; attempt++ {
		fmt.Fprintf(out, "Enter 1-%d (q to cancel): ", len(infos))
		if !scanner.Scan() {
			return "", errors.New("session selection cancelled")
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "q" || line == "Q" {
			return "", errors.New("session selection cancelled")
		}
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= len(infos) {
			return infos[n-1].ID, nil
		}
		fmt.Fprintf(out, "  invalid selection: %q\n", line)
	}
	return "", errors.New("session selection cancelled")
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Why: picker rows are visually clipped, so we append an ellipsis to
// signal truncation; the session-package twin omits it because callers
// there compose the preview into structured output.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
