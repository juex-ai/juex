package memoryclient

import (
	"errors"
	"fmt"
	"regexp"
)

const EntryIDPattern = `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`
const EntryIDDescription = "Use 1-64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit (for example birthday-example). A proposal key is a separate request identity, not an entry ID."

var entryIDPattern = regexp.MustCompile(EntryIDPattern)

func ValidateEntryID(id string) error {
	if !entryIDPattern.MatchString(id) {
		return fmt.Errorf("invalid memory entry ID %q: %s", id, EntryIDDescription)
	}
	return nil
}

func ValidateSource(ref Source, fleet string) error {
	if ref.FleetID != fleet || ref.AgentID == "" || ref.ThreadID == "" || ref.GenerationID == "" || ref.From == 0 || ref.Through < ref.From {
		return errors.New("invalid memory source reference: copy a source returned by memory_read or supplied in the assignment; use this Fleet, non-empty Agent/Thread/Generation IDs, and 1 <= from <= through; do not guess source identifiers or cursors")
	}
	return nil
}
