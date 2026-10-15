package tasks

import "strings"

func InstructionPrompt(args string) string {
	prompt := "The user wants to inspect or update this Thread's tasks. Use list_tasks first, then create_task, update_task or delete_task as appropriate. Use only these tools to change task state. Do not treat the slash command itself as a task."
	if args = strings.TrimSpace(args); args != "" {
		prompt += "\n\nUser task request:\n" + args
	}
	return prompt
}
