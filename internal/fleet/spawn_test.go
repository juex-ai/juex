package fleet

import (
	"reflect"
	"testing"
)

func TestRuntimeCommandArgsSelectRegisteredAgent(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	want := []string{"listen", "--agent-id", entry.ID}
	if got := runtimeCommandArgs(entry); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime args = %#v, want %#v", got, want)
	}
}
