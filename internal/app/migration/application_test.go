package migration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func memoryRoleFile(thread, job string) legacy.SourceFile {
	data, _ := json.Marshal(map[string]any{"id": job, "token": "old-assignment-secret", "expires_at": "2026-09-20T02:00:00Z", "proposal": map[string]any{}, "scope": map[string]any{}, "automatic": true})
	return legacy.SourceFile{Path: "modules/memory-client/workers/" + thread + ".json", SHA256: execprotocol.FileDigest(data), Size: int64(len(data)), Data: data}
}

func TestRuntimeConversionDerivesRestrictedRolesWithoutDuplicatingSourceReview(t *testing.T) {
	scope, source, bindings := runtimeFixture()
	source.Files = []legacy.SourceFile{memoryRoleFile("abcdef", "same-source-review")}
	for _, id := range []string{"bcdefg", "cdefgh"} {
		worker := source.Threads[1]
		worker.Metadata.ThreadID, worker.Metadata.Alias = id, id
		worker.Commits = []legacy.Commit{{Version: 1, Seq: 1, At: worker.Metadata.CreatedAt, GenerationID: "g000001", Facts: []legacy.Fact{{Type: "thread.created", ThreadID: id, ParentThreadID: "0"}}}}
		source.Threads = append(source.Threads, worker)
		source.Files = append(source.Files, memoryRoleFile(id, "same-source-review"))
	}
	value, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal("source application role requires invented job bindings", err)
	}
	for _, thread := range value.Import.Threads[1:] {
		if thread.Thread.Application != "memory" || thread.Application == nil || *thread.Application != (managedruntime.ImportedApplication{Application: "memory"}) {
			t.Fatal("Worker role changed or fabricated an execution outcome")
		}
		var found bool
		for _, event := range thread.Events {
			if event.Kind == "import.application" {
				found = strings.Contains(string(event.Data), "same-source-review") && strings.Contains(string(event.Data), "source_sha256")
				if strings.Contains(string(event.Data), "token") || strings.Contains(string(event.Data), "expires_at") {
					t.Fatal("old execution authority copied into role provenance")
				}
			}
		}
		if !found {
			t.Fatal("source Review relationship not preserved")
		}
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "old-assignment-secret") {
		t.Fatal("old execution authority copied into new history")
	}
}

func TestRuntimeConversionRejectsUnprovenWorkerRoleFiles(t *testing.T) {
	for _, scenario := range []string{"changed-bytes", "unknown-thread", "main", "empty-token", "override-job"} {
		t.Run(scenario, func(t *testing.T) {
			scope, source, bindings := runtimeFixture()
			file := memoryRoleFile("abcdef", "source-review")
			switch scenario {
			case "changed-bytes":
				file.Data = append(file.Data, ' ')
			case "unknown-thread":
				file.Path = "modules/memory-client/workers/missing.json"
			case "main":
				file.Path = "modules/memory-client/workers/0.json"
			case "empty-token":
				file.Data = []byte(`{"id":"source-review","token":"","expires_at":"2026-09-20T02:00:00Z"}`)
				file.Size, file.SHA256 = int64(len(file.Data)), execprotocol.FileDigest(file.Data)
			case "override-job":
				bindings.Applications = map[string]managedruntime.ImportedApplication{"abcdef": {Application: "memory", JobID: "invented-job", State: "failed"}}
			}
			source.Files = []legacy.SourceFile{file}
			if _, err := ConvertRuntime(scope, source, bindings); err == nil {
				t.Fatal("unproven role accepted")
			}
		})
	}
}
