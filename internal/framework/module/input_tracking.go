package module

import "context"

// InputReminder is an immutable projection of an accepted, delivered input.
type InputReminder struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// InputTracker keeps acceptance and completion durability with the Framework.
type InputTracker interface {
	UncheckedInputs(context.Context) ([]InputReminder, error)
	CheckInputs(context.Context, []string) ([]string, error)
}
