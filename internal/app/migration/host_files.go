package migration

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func importedHostFile(file legacy.SourceFile, relative string) execution.HostImportFile {
	return execution.HostImportFile{Path: relative, SHA256: file.SHA256, Mode: file.Mode, Data: file.Data}
}

func (b *Bundle) hostFiles(agent legacy.Agent, target string, prepared PreparedBundle) (execution.HostImport, error) {
	request := execution.HostImport{SourceSHA256: b.digest, Threads: map[string][]execution.HostImportFile{}, Extensions: map[string]execution.HostExtensionImport{}}
	namespace, err := uuid.Parse(target)
	if err != nil {
		return request, err
	}
	for _, thread := range agent.Threads {
		id := mappedIdentity(namespace, "thread", thread.Metadata.ThreadID).String()
		prefix := path.Join("threads", thread.Metadata.ThreadID, "scratchpad") + "/"
		if thread.Metadata.RetentionState == "archived" {
			prefix = "archive/" + prefix
		}
		// Existing files are preserved even when their guidance was disabled.
		if prepared.Agents[agent.Definition.ID].Configuration.Modules[agentpolicy.WorkingFiles] {
			request.Threads[id] = []execution.HostImportFile{}
		}
		for _, file := range agent.Files {
			if relative, ok := strings.CutPrefix(file.Path, prefix); ok {
				request.Threads[id] = append(request.Threads[id], importedHostFile(file, relative))
			}
		}
	}
	for _, manifest := range prepared.Extensions[agent.Definition.ID] {
		// Extension name is the source's installation identity and remains stable
		// when its manifest or executable changes in a later upgrade.
		binding := mappedIdentity(namespace, "extension", manifest.Name).String()
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return request, err
		}
		value := execution.HostExtensionImport{Installation: []execution.HostImportFile{{Path: "juex.extension.json", SHA256: bundleDigest(encoded), Mode: 0600, Data: encoded}}}
		for _, file := range agent.Files {
			if relative, ok := strings.CutPrefix(file.Path, "extensions/"+manifest.Name+"/"); ok {
				value.Private = append(value.Private, importedHostFile(file, relative))
			}
		}
		request.Extensions[binding] = value
	}
	if len(request.Threads)+len(request.Extensions) == 0 {
		return request, nil
	}
	return request, request.Validate()
}

func restoredWorkingFiles(agent legacy.Agent, target string, receipt execution.HostImportReceipt) map[string]managedruntime.WorkingFiles {
	result := map[string]managedruntime.WorkingFiles{}
	namespace := uuid.MustParse(target)
	for _, thread := range agent.Threads {
		id := mappedIdentity(namespace, "thread", thread.Metadata.ThreadID).String()
		if directory := receipt.ThreadDirectories[id]; directory != "" {
			result[thread.Metadata.ThreadID] = managedruntime.WorkingFiles{EnvironmentID: receipt.EnvironmentID, Directory: directory}
		}
	}
	return result
}
