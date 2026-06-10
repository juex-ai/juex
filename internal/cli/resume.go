package cli

import (
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/juex-ai/juex/internal/session"
)

const resumePick = "__pick__"

// resumeFlags collects the two CLI flags that select a session to resume.
// They are exposed on both `run` and `repl`.
type resumeFlags struct {
	Resume  string // "last", session id, alias, or resumePick for interactive picker
	Session string // direct session ID
	Alias   string // optional alias to set/update on the opened session
}

// stdinIsTTY is overridable in tests; in production it inspects os.Stdin.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// resolveSessionDir maps the two CLI flags to an absolute session
// directory. An empty string + nil error means "no resume requested".
//
// sessionsRoot is the parent dir under which session ids are stored.
// in / out are used only when invoking the picker.
// interactive lets tests bypass the os.Stdin TTY check.
func resolveSessionDir(rf resumeFlags, sessionsRoot, historyPath string, in io.Reader, out io.Writer, interactive bool) (string, error) {
	if rf.Resume != "" && rf.Session != "" {
		return "", &usageError{msg: "pass --resume or --session, not both"}
	}
	if rf.Session != "" {
		dir, ok := existingSessionDir(sessionsRoot, rf.Session)
		if !ok {
			return "", &notFoundError{msg: "session not found: " + rf.Session}
		}
		return dir, nil
	}
	if rf.Resume == "" {
		return "", nil
	}
	if rf.Resume != resumePick {
		return resolveSessionSelector(rf.Resume, sessionsRoot, historyPath)
	}
	if !interactive {
		return "", &usageError{msg: "--resume requires an interactive terminal; pass --session <id>"}
	}
	infos, err := session.List(sessionsRoot)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", &notFoundError{msg: "no sessions to resume"}
	}
	id, err := pickSession(in, out, infos)
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionsRoot, id), nil
}

func existingSessionDir(sessionsRoot, id string) (string, bool) {
	dir := filepath.Join(sessionsRoot, id)
	if !session.HasConversation(dir) {
		return "", false
	}
	return dir, true
}

func resolveSessionSelector(selector, sessionsRoot, historyPath string) (string, error) {
	if selector == "last" {
		h, err := session.LoadHistory(historyPath)
		if err != nil {
			return "", err
		}
		if h.Active == nil || h.Active.ID == "" {
			return "", &notFoundError{msg: "no active session to resume"}
		}
		dir := session.InfoDir(sessionsRoot, *h.Active)
		if _, ok := existingSessionDir(filepath.Dir(dir), filepath.Base(dir)); !ok {
			return "", &notFoundError{msg: "session not found: " + h.Active.ID}
		}
		return dir, nil
	}
	if dir, ok := existingSessionDir(sessionsRoot, selector); ok {
		return dir, nil
	}

	matches, err := aliasMatches(selector, sessionsRoot, historyPath)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", &notFoundError{msg: "session not found: " + selector}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if !matches[i].LastActiveAt.Equal(matches[j].LastActiveAt) {
			return matches[i].LastActiveAt.After(matches[j].LastActiveAt)
		}
		return matches[i].StartedAt.After(matches[j].StartedAt)
	})
	for _, match := range matches {
		dir := session.InfoDir(sessionsRoot, match)
		if _, ok := existingSessionDir(filepath.Dir(dir), filepath.Base(dir)); ok {
			return dir, nil
		}
	}
	return "", &notFoundError{msg: "session not found: " + selector}
}

func aliasMatches(alias, sessionsRoot, historyPath string) ([]session.Info, error) {
	h, err := session.LoadHistory(historyPath)
	if err != nil {
		return nil, err
	}
	var matches []session.Info
	seen := map[string]bool{}
	for _, info := range h.Sessions {
		if info.Alias == alias {
			matches = append(matches, info)
			seen[info.ID] = true
		}
	}
	infos, err := session.List(sessionsRoot)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Alias == alias && !seen[info.ID] {
			matches = append(matches, info)
		}
	}
	return matches, nil
}
