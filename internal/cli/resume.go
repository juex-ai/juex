package cli

import (
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/session"
)

// resumeFlags collects the two CLI flags that select a session to resume.
// They are exposed on both `run` and `repl`.
type resumeFlags struct {
	Resume  bool   // open the interactive picker
	Session string // direct session ID
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
func resolveSessionDir(rf resumeFlags, sessionsRoot string, in io.Reader, out io.Writer, interactive bool) (string, error) {
	if rf.Resume && rf.Session != "" {
		return "", &usageError{msg: "pass --resume or --session, not both"}
	}
	if rf.Session != "" {
		dir := filepath.Join(sessionsRoot, rf.Session)
		if _, err := os.Stat(filepath.Join(dir, "conversation.jsonl")); err != nil {
			return "", &notFoundError{msg: "session not found: " + rf.Session}
		}
		return dir, nil
	}
	if !rf.Resume {
		return "", nil
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
