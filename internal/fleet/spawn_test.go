package fleet

import (
	"reflect"
	"testing"
)

func TestRuntimeCommandArgsPreserveAgentConfig(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	entry.Agent.RuntimeConfigPath = "/configs/provider.yaml"
	want := []string{"-C", entry.Agent.Workspace, "--config", "/configs/provider.yaml", "listen"}
	if got := runtimeCommandArgs(entry); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime args = %#v, want %#v", got, want)
	}
}
