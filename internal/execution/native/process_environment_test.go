package native

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestPrivateProcessDefaultsStayOutOfJournalAndRecoverWithoutReplay(t *testing.T) {
	dir, state := t.TempDir(), filepath.Join(t.TempDir(), "executor")
	config := Config{StateDirectory: state, EnvironmentID: "private-env", WorkingDirectory: dir, Grants: map[string][]execprotocol.Capability{"agent": {execprotocol.Shell}}}
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if engine != nil {
			_ = engine.Close()
		}
	}()
	values := map[string]string{"PRIVATE_TOKEN": "private-only-in-process-20261010", "OVERRIDE": "default"}
	arguments, _ := json.Marshal(CommandArguments{Command: `printf once >> calls; printf '%s' "$PRIVATE_TOKEN" | wc -c; printf '%s' "$OVERRIDE"`, Environment: map[string]string{"OVERRIDE": "operation"}})
	request := execprotocol.Request{Version: execprotocol.Version, ID: "private", AgentID: "agent", Kind: "exec_command", Arguments: arguments}
	if _, err := engine.SubmitWithEnvironment(request, values); err != nil {
		t.Fatal(err)
	}
	var snapshot execprotocol.Snapshot
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err = engine.Snapshot("agent", "private", 0, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.State.Terminal() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	expected := strconv.Itoa(len(values["PRIVATE_TOKEN"]))
	if snapshot.State != execprotocol.Completed || !strings.Contains(string(snapshot.Output), expected) || !strings.HasSuffix(string(snapshot.Output), "operation") {
		t.Fatal("private defaults were not delivered", snapshot.State)
	}
	if _, err := engine.Submit(request); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("credential-less replay accepted", err)
	}
	changed := map[string]string{"PRIVATE_TOKEN": "replacement"}
	if _, err := engine.SubmitWithEnvironment(request, changed); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("same operation adopted new credentials", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine = nil
	err = filepath.WalkDir(state, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), values["PRIVATE_TOKEN"]) {
				t.Fatal("private defaults persisted")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = engine.SubmitWithEnvironment(request, values)
	if err != nil || snapshot.State != execprotocol.Completed {
		t.Fatal("durable receipt lost", err, snapshot.State)
	}
	data, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || string(data) != "once" {
		t.Fatal("recovery repeated execution", err)
	}
}
