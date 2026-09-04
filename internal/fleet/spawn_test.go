package fleet

import (
	"reflect"
	"testing"
)

func TestRuntimeCommandArgsSelectRegisteredAgent(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	want := []string{"--agent-id", entry.ID, "listen"}
	if got := runtimeCommandArgs(entry); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime args = %#v, want %#v", got, want)
	}
}
