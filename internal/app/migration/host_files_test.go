package migration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestHostFilesMapCurrentAndArchivedThreadsAndStableExtensionState(t *testing.T) {
	source := legacy.Agent{Definition: legacy.AgentDefinition{ID: "source"}, Threads: []legacy.Thread{{Metadata: legacy.ThreadMetadata{ThreadID: "0", RetentionState: "active"}}, {Metadata: legacy.ThreadMetadata{ThreadID: "7", RetentionState: "archived"}}}}
	for _, name := range []string{"threads/0/scratchpad/plan.md", "archive/threads/7/scratchpad/report.txt", "extensions/chat/credentials.json", "extensions/chat/context-guard/result.json"} {
		data := []byte(name)
		source.Files = append(source.Files, legacy.SourceFile{Path: name, Data: data, Mode: 0600, Size: int64(len(data)), SHA256: bundleDigest(data)})
	}
	policy := agentpolicy.Policy{}
	prepared := PreparedBundle{Agents: map[string]management.AgentConfig{"source": {Capabilities: &policy}}, Extensions: map[string][]extensionpolicy.Manifest{"source": {{Name: "chat"}}}}
	bag := &Bundle{digest: bundleDigest([]byte("source"))}
	namespace := uuid.New()
	request, err := bag.hostFiles(source, namespace.String(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	main := mappedIdentity(namespace, "thread", "0").String()
	worker := mappedIdentity(namespace, "thread", "7").String()
	binding := mappedIdentity(namespace, "extension", "chat").String()
	if len(request.Threads) != 2 || request.Threads[main][0].Path != "plan.md" || request.Threads[worker][0].Path != "report.txt" || len(request.Extensions[binding].Private) != 2 {
		t.Fatal("incorrect file ownership mapping")
	}
	receipt := execution.HostImportReceipt{EnvironmentID: uuid.NewString(), ThreadDirectories: map[string]string{main: "/owned/main", worker: "/owned/worker"}}
	locations := restoredWorkingFiles(source, namespace.String(), receipt)
	if locations["0"].Directory != "/owned/main" || locations["7"].Directory != "/owned/worker" || locations["0"].EnvironmentID != receipt.EnvironmentID {
		t.Fatal(locations)
	}
	prepared.Extensions["source"][0].Name = "chat"
	again, err := bag.hostFiles(source, namespace.String(), prepared)
	if err != nil || len(again.Extensions[binding].Private) != 2 {
		t.Fatal("unstable binding", err)
	}
}
