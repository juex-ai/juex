package goal

import (
	"strings"
)

func InstructionPrompt(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "The user wants to inspect or update the Thread goal. Use get_goal first, then create_goal or update_goal if a goal should be created, changed, marked success, or marked failure. Do not treat this slash command text itself as the goal description."
	}
	return "The user wants to create or update the Thread goal. Use get_goal first, then call create_goal or update_goal as appropriate. Do not write goal state directly; use the goal tools only.\n\nUser goal request:\n" + args
}
