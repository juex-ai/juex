package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type sendReceipt struct {
	AgentID      string            `json:"agent_id,omitempty"`
	ThreadID     string            `json:"thread_id"`
	InputID      string            `json:"input_id,omitempty"`
	AcceptedAt   string            `json:"accepted_at,omitempty"`
	State        string            `json:"state,omitempty"`
	Cursor       string            `json:"cursor,omitempty"`
	TurnID       string            `json:"turn_id,omitempty"`
	Queued       bool              `json:"queued,omitempty"`
	PendingCount int               `json:"pending_count,omitempty"`
	Command      json.RawMessage   `json:"command,omitempty"`
	Warnings     []json.RawMessage `json:"warnings,omitempty"`
}

func newAgentSendCmd() *cobra.Command {
	selectors := &agentSelectorFlags{}
	cmd := newSendCommand(selectors, false)
	bindAgentSelectorFlags(cmd, selectors)
	return cmd
}

func newThreadSendCmd(selectors *agentSelectorFlags) *cobra.Command {
	return newSendCommand(selectors, true)
}

func newSendCommand(selectors *agentSelectorFlags, explicitThread bool) *cobra.Command {
	var attachments []string
	var wait bool
	var jsonOut bool
	use := "send [message...]"
	short := "Durably send one Input to the Main Thread"
	argsValidator := cobra.ArbitraryArgs
	if explicitThread {
		use = "send <thread> [message...]"
		short = "Durably send one Input to a Worker Thread"
		argsValidator = cobra.MinimumNArgs(1)
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  usageArgs(argsValidator),
		RunE: func(cmd *cobra.Command, args []string) error {
			threadSelector := "0"
			messageArgs := args
			if explicitThread {
				threadSelector = strings.TrimSpace(strings.TrimPrefix(args[0], "#"))
				if threadSelector == "" || threadSelector == "0" || strings.EqualFold(threadSelector, "main") {
					return &usageError{msg: "juex thread send: target must be a Worker Thread; use juex agent send for Main"}
				}
				messageArgs = args[1:]
			}
			message, err := readSendInput(cmd, messageArgs, len(attachments) > 0)
			if err != nil {
				return err
			}
			manager, state, err := resolveSelectedAgent(selectors)
			if err != nil {
				return err
			}
			client, err := connectSelectedAgent(cmd.Context(), manager, state)
			if err != nil {
				return err
			}
			defer client.Close()
			entry, err := client.resolveThread(cmd.Context(), threadSelector, false)
			if err != nil {
				return err
			}
			refs := make([]map[string]any, 0, len(attachments))
			for _, attachment := range attachments {
				path := resolveAttachmentPath(client.workspace, attachment)
				ref, err := client.upload(cmd.Context(), entry.ThreadID, path)
				if err != nil {
					return err
				}
				refs = append(refs, ref)
			}
			var receipt sendReceipt
			err = client.doJSON(cmd.Context(), http.MethodPost,
				"/api/threads/"+entry.ThreadID+"/inputs",
				map[string]any{"prompt": message, "attachments": refs}, &receipt)
			if err != nil {
				return err
			}
			receipt.AgentID = client.agentID
			if jsonOut {
				cmdPrintln(cmd, compactJSON(receipt))
			} else if receipt.InputID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "accepted #%s input=%s state=%s pending=%d\n", receipt.ThreadID, receipt.InputID, receipt.State, receipt.PendingCount)
			} else if len(receipt.Command) > 0 {
				cmdPrintln(cmd, string(receipt.Command))
			}
			if !wait || receipt.InputID == "" {
				return nil
			}
			return waitForInput(cmd, client, receipt, jsonOut)
		},
	}
	cmd.Flags().StringArrayVarP(&attachments, "attach", "a", nil, "attach one file (repeatable)")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "stream the consuming Turn until it settles")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit a receipt or NDJSON event stream")
	return cmd
}

func resolveAttachmentPath(workspace, attachment string) string {
	if filepath.IsAbs(attachment) {
		return filepath.Clean(attachment)
	}
	return filepath.Join(workspace, attachment)
}

func readSendInput(cmd *cobra.Command, args []string, hasAttachments bool) (string, error) {
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " ")), nil
	}
	info, err := os.Stdin.Stat()
	if err == nil && info.Mode()&os.ModeCharDevice == 0 {
		data, readErr := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8<<20))
		if readErr != nil {
			return "", readErr
		}
		if message := strings.TrimSpace(string(data)); message != "" || hasAttachments {
			return message, nil
		}
	}
	if hasAttachments {
		return "", nil
	}
	return "", &usageError{msg: cmd.CommandPath() + ": message, piped stdin, or --attach required"}
}

func waitForInput(cmd *cobra.Command, client *agentClient, receipt sendReceipt, jsonOut bool) error {
	turnID := receipt.TurnID
	settled := false
	err := client.stream(cmd.Context(), receipt.ThreadID, receipt.Cursor, func(event streamEvent) (bool, error) {
		if turnID == "" && (event.Type == "pending_input.promoted" || event.Type == "pending_input.draining" || event.Type == "pending_input.drained") {
			var payload struct {
				InputIDs []string `json:"input_ids"`
			}
			_ = json.Unmarshal(event.Payload, &payload)
			for _, inputID := range payload.InputIDs {
				if inputID == receipt.InputID {
					turnID = event.TurnID
					break
				}
			}
		}
		if turnID == "" || event.TurnID != turnID {
			return false, nil
		}
		if jsonOut {
			cmdPrintln(cmd, compactJSON(event))
		} else {
			printWaitEvent(cmd, event)
		}
		switch event.Type {
		case "turn.completed":
			settled = true
			if jsonOut {
				cmdPrintln(cmd, compactJSON(map[string]any{"type": "input.terminal", "input_id": receipt.InputID, "turn_id": turnID, "state": "succeeded"}))
			}
			return true, nil
		case "turn.errored":
			settled = true
			if jsonOut {
				cmdPrintln(cmd, compactJSON(map[string]any{"type": "input.terminal", "input_id": receipt.InputID, "turn_id": turnID, "state": "failed"}))
			}
			return true, errors.New("consuming Turn failed")
		case "turn.cancelled":
			settled = true
			if jsonOut {
				cmdPrintln(cmd, compactJSON(map[string]any{"type": "input.terminal", "input_id": receipt.InputID, "turn_id": turnID, "state": "cancelled"}))
			}
			return true, errors.New("consuming Turn was cancelled")
		default:
			return false, nil
		}
	})
	if err != nil {
		return err
	}
	if !settled {
		return errors.New("event stream closed before the consuming Turn settled")
	}
	return nil
}

func printWaitEvent(cmd *cobra.Command, event streamEvent) {
	if event.Type == "llm.output_delta" {
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil {
			fmt.Fprint(cmd.OutOrStdout(), payload.Text)
		}
		return
	}
	if event.Type == "turn.completed" {
		fmt.Fprintln(cmd.OutOrStdout())
		return
	}
	if strings.HasPrefix(event.Type, "tool.") || strings.HasPrefix(event.Type, "context.") || event.Type == "llm.retry" {
		fmt.Fprintf(cmd.OutOrStdout(), "[%s]\n", event.Type)
	}
}

func compactJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
