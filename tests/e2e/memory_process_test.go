package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/fleet/services"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

func TestFleetMemoryDefaultProcessRetainsKnowledgeAndIsolatesHomes(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled Memory service")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	call := func(home string, args ...string) string {
		t.Helper()
		stdout, stderr, err := runJuexHomeCommand(binary, home, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, stdout, stderr)
		}
		return stdout
	}
	start := func(home string) services.Status {
		t.Helper()
		var status services.Status
		if err := json.Unmarshal([]byte(call(home, "fleet", "services", "start", "memory")), &status); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _, _ = runJuexHomeCommand(binary, home, "fleet", "services", "stop", "memory") })
		return status
	}
	first := start(home)
	if first.Runtime == nil || first.Phase != "ready" {
		t.Fatalf("default service %+v", first)
	}
	user := mc.Caller{FleetID: first.Runtime.FleetID, AgentID: "user", ThreadID: "0", Profile: mc.ProfileUser}
	client := mc.New(serviceendpoint.FileResolver{Home: home, Fleet: user.FleetID}, "memory", user)
	request := mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: mc.Entry{ID: "persistent", Name: "Persistent", Summary: "shared across restarts", Body: "Persistent Memory process knowledge", Type: "reference"}}}}
	data, _ := json.Marshal(request)
	path := filepath.Join(home, "request.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if body := call(home, "memory", "admin", "--file", path); !strings.Contains(body, `"committed": true`) {
		t.Fatal(body)
	}
	call(home, "fleet", "services", "stop", "memory")
	if _, err := client.Read(context.Background(), user, mc.ReadRequest{ID: "persistent"}); err == nil {
		t.Fatal("stopped service served knowledge")
	}
	second := start(home)
	if second.Runtime.InstanceID == first.Runtime.InstanceID {
		t.Fatal("service did not restart")
	}
	if entry, err := client.Read(context.Background(), user, mc.ReadRequest{ID: "persistent"}); err != nil || entry.Revision != 1 {
		t.Fatalf("reconnect %+v %v", entry, err)
	}
	other := start(t.TempDir())
	if other.Runtime.FleetID == second.Runtime.FleetID {
		t.Fatal("Fleet identity reused")
	}
	if body := call(home, "memory", "read", "persistent"); !strings.Contains(body, "Persistent Memory process knowledge") {
		t.Fatal(body)
	}
}
