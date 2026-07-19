package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/usermedia"
)

const replAttachCommand = "/attach"

// REPL reads stdin lines, runs one turn for each non-empty prompt, and prints
// results until the reader closes. /attach stages local images for the next
// ordinary user prompt. Non-blocking capability warnings use errOut.
func (a *App) REPL(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	if a == nil || a.Engine == nil {
		return errors.New("app: REPL requires an initialized session and engine")
	}
	if _, ok := a.SessionIdentity(); !ok {
		return errors.New("app: REPL requires an initialized session and engine")
	}
	if errOut == nil {
		errOut = io.Discard
	}
	sc := bufio.NewScanner(in)
	var staged []llm.MediaRef
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if imagePath, handled, err := parseREPLAttach(line); handled {
			if err == nil && len(staged) >= usermedia.DefaultMaxCount {
				err = fmt.Errorf("user media: too many images (%d/%d)", len(staged)+1, usermedia.DefaultMaxCount)
			}
			if err != nil {
				if writeErr := writeREPLError(out, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			identity, ok := a.SessionIdentity()
			if !ok {
				return ErrSessionUnavailable
			}
			ref, err := usermedia.StoreFile(a.cfg.WorkDir, identity.ID, imagePath, usermedia.Limits{})
			if err != nil {
				if writeErr := writeREPLError(out, err); writeErr != nil {
					return writeErr
				}
				continue
			}
			staged = append(staged, ref)
			if _, err := fmt.Fprintf(out, "attached: %s (%d/%d staged)\n", llm.FormatImagePlaceholder(&ref), len(staged), usermedia.DefaultMaxCount); err != nil {
				return err
			}
			continue
		}

		cmd, handled, parseErr := ParseSlashCommand(line)
		var (
			text string
			err  error
		)
		if handled || parseErr != nil {
			previous, _ := a.SessionIdentity()
			text, err = a.Run(ctx, line)
			current, _ := a.SessionIdentity()
			if parseErr == nil && cmd.Name == SlashNew && current.ID != previous.ID {
				staged = nil
			}
		} else {
			attachments := staged
			staged = nil
			writeREPLTurnWarnings(errOut, a.AttachmentWarnings(len(attachments)))
			text, err = a.RunWithAttachments(ctx, line, attachments)
		}
		if err != nil {
			if writeErr := writeREPLError(out, err); writeErr != nil {
				return writeErr
			}
			continue
		}
		if _, err := fmt.Fprintln(out, text); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, FormatTokenUsage(a.TokenUsage())); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Capability warnings are best-effort diagnostics and must not stop the REPL turn.
func writeREPLTurnWarnings(w io.Writer, warnings []TurnWarning) {
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(w, "juex: warning: %s; %s\n", warning.Message, warning.Suggestion); err != nil {
			return
		}
	}
}

func parseREPLAttach(input string) (string, bool, error) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, replAttachCommand) {
		return "", false, nil
	}
	rest := strings.TrimPrefix(input, replAttachCommand)
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false, nil
	}
	rest = strings.TrimSpace(rest)
	if len(rest) >= 2 && ((rest[0] == '"' && rest[len(rest)-1] == '"') || (rest[0] == '\'' && rest[len(rest)-1] == '\'')) {
		rest = strings.TrimSpace(rest[1 : len(rest)-1])
	}
	if rest == "" {
		return "", true, errors.New("usage: /attach <image-path>")
	}
	return rest, true, nil
}

func writeREPLError(out io.Writer, err error) error {
	_, writeErr := fmt.Fprintln(out, "error:", err)
	return writeErr
}
