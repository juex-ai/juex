package legacy

import (
	"errors"
	"path"
	"slices"
	"strings"
)

func rebuildCapturedThread(source Thread) (Thread, error) {
	metadata, err := capturedFile(source.Files, "thread.json")
	if err != nil {
		return Thread{}, err
	}
	var m ThreadMetadata
	if err := decode(metadata.Data, &m); err != nil || !sameCaptureJSON(m, source.Metadata) {
		return Thread{}, errors.New("captured Thread metadata differs from original bytes")
	}
	expected := map[string]bool{"thread.json": true, "inputs.json": true}
	for _, generation := range m.Generations {
		expected["generations/"+generation.ID+".jsonl"] = true
	}
	for _, file := range source.Files {
		if !expected[file.Path] {
			return Thread{}, errors.New("captured Thread has an unexpected journal file")
		}
	}
	if len(source.AbsentFiles) > 1 || len(source.AbsentFiles) == 1 && source.AbsentFiles[0] != "inputs.json" {
		return Thread{}, errors.New("captured Thread has unexpected absence records")
	}
	read := func(name string) ([]byte, error) {
		file, err := capturedFile(source.Files, name)
		return file.Data, err
	}
	optional := func(name string) ([]byte, error) {
		if slices.Contains(source.AbsentFiles, name) {
			return nil, nil
		}
		return read(name)
	}
	rebuilt, err := decodeThread(m, source.Metadata.ThreadID, read, optional)
	if err != nil {
		return Thread{}, err
	}
	rebuilt.Files, rebuilt.AbsentFiles = source.Files, source.AbsentFiles
	if !sameCaptureJSON(rebuilt, source) {
		return Thread{}, errors.New("captured Thread projection differs from original journal")
	}
	return rebuilt, nil
}

func validateCapturedFleet(f *Fleet) error {
	if !captureDirectory(f.SourceHome) || !captureDirectory(f.DefaultHome.Directory) {
		return errors.New("capture source Homes must be explicit absolute paths")
	}
	if err := captureConfigInventory(f.DefaultHome.Files, f.DefaultHome.AbsentFiles, []string{"juex.yaml"}); err != nil {
		return err
	}
	if !captureHasFileOrAbsence(f.Files, f.AbsentFiles, "juex.yaml") {
		return errors.New("capture is missing instance Home configuration evidence")
	}
	identity, err := capturedFile(f.Files, "fleet.json")
	if err != nil {
		return err
	}
	var id struct {
		ID string `json:"id"`
	}
	if err := decode(identity.Data, &id); err != nil || id.ID == "" || id.ID != f.ID || strings.TrimSpace(id.ID) != id.ID {
		return errors.New("capture Fleet identity differs from original bytes")
	}
	agents := map[string]*Agent{}
	for i := range f.Agents {
		a := &f.Agents[i]
		id := a.Definition.ID
		if !validAgentID(id) || agents[id] != nil || strings.TrimSpace(a.Definition.Name) == "" || !captureDirectory(a.Definition.Workspace) || a.Definition.CreatedAt.IsZero() {
			return errors.New("capture has invalid or repeated Agent identity")
		}
		agents[id] = a
		if !captureHasFileOrAbsence(a.Files, a.AbsentFiles, "juex.yaml") {
			return errors.New("capture is missing Agent configuration evidence")
		}
		file, err := capturedFile(a.Files, "agent.json")
		if err != nil {
			return err
		}
		var definition AgentDefinition
		if err := decode(file.Data, &definition); err != nil || !sameCaptureJSON(definition, a.Definition) {
			return errors.New("capture Agent definition differs from original bytes")
		}
		if err := captureProjection(f.Files, f.AbsentFiles, "agents/"+id, a.Files, a.AbsentFiles, true); err != nil {
			return err
		}
		threads := map[string]bool{}
		for j := range a.Threads {
			thread := &a.Threads[j]
			base := "threads/" + thread.Metadata.ThreadID
			if thread.Metadata.RetentionState == "archived" {
				base = "archive/" + base
			}
			threads[base+"/thread.json"] = true
			if err := captureProjection(a.Files, a.AbsentFiles, base, thread.Files, thread.AbsentFiles, false); err != nil {
				return err
			}
			rebuilt, err := rebuildCapturedThread(*thread)
			if err != nil {
				return err
			}
			*thread = rebuilt
		}
		for _, file := range a.Files {
			parts := strings.Split(strings.TrimPrefix(file.Path, "archive/"), "/")
			if len(parts) == 3 && parts[0] == "threads" && parts[2] == "thread.json" && !threads[file.Path] {
				return errors.New("capture omitted a Thread projection")
			}
		}
		if err := a.validateThreads(); err != nil {
			return err
		}
		references := a.References
		a.References = nil
		if err := a.verifyReferences(a.Files); err != nil {
			return err
		}
		if !sameCaptureJSON(references, a.References) {
			return errors.New("capture media/spool projection differs from original references")
		}
	}
	for _, file := range f.Files {
		parts := strings.Split(file.Path, "/")
		if len(parts) >= 2 && parts[0] == "agents" && agents[parts[1]] == nil {
			return errors.New("capture omitted an Agent projection")
		}
		if strings.HasPrefix(file.Path, "services/memory/") && f.Memory == nil {
			return errors.New("capture omitted Memory state")
		}
	}
	workspaces := map[string]bool{}
	for _, w := range f.Workspaces {
		a := agents[w.AgentID]
		if a == nil || workspaces[w.AgentID] || w.Path != a.Definition.Workspace || !captureDirectory(w.ResolvedPath) {
			return errors.New("capture has invalid Workspace ownership")
		}
		workspaces[w.AgentID] = true
		config := ".juex/juex.yaml"
		if path.Base(w.Path) == ".juex" {
			config = "juex.yaml"
		}
		if err := captureConfigInventory(w.Files, w.AbsentFiles, []string{config, ".env", "AGENTS.md", ".agents/AGENTS.md"}); err != nil {
			return err
		}
	}
	if len(workspaces) != len(agents) {
		return errors.New("capture is missing a Workspace")
	}
	if f.Memory != nil {
		if err := rebuildCapturedMemory(f); err != nil {
			return err
		}
	}
	return captureSharedPaths(f)
}

func captureHasFileOrAbsence(files []SourceFile, absent []string, name string) bool {
	_, err := capturedFile(files, name)
	return err == nil || slices.Contains(absent, name)
}

func captureConfigInventory(files []SourceFile, absent, required []string) error {
	if len(files)+len(absent) != len(required) {
		return errors.New("capture configuration inventory is incomplete")
	}
	for _, name := range required {
		if !captureHasFileOrAbsence(files, absent, name) {
			return errors.New("capture configuration inventory is incomplete")
		}
	}
	return nil
}

// Overlapping Homes and Workspaces retain separate provenance, but a shared
// literal path cannot simultaneously supply different bytes or prove absence.
func captureSharedPaths(f *Fleet) error {
	files := map[string]SourceFile{}
	absent := map[string]bool{}
	add := func(root string, values []SourceFile, missing []string) error {
		for _, value := range values {
			name := path.Join(root, value.Path)
			prior, exists := files[name]
			if absent[name] || exists && (prior.SHA256 != value.SHA256 || prior.Mode != value.Mode || prior.Size != value.Size) {
				return errors.New("overlapping capture paths disagree")
			}
			files[name] = value
		}
		for _, value := range missing {
			name := path.Join(root, value)
			if _, exists := files[name]; exists {
				return errors.New("overlapping capture paths disagree")
			}
			absent[name] = true
		}
		return nil
	}
	if err := add(f.SourceHome, f.Files, f.AbsentFiles); err != nil {
		return err
	}
	if err := add(f.DefaultHome.Directory, f.DefaultHome.Files, f.DefaultHome.AbsentFiles); err != nil {
		return err
	}
	for _, workspace := range f.Workspaces {
		if err := add(workspace.ResolvedPath, workspace.Files, workspace.AbsentFiles); err != nil {
			return err
		}
	}
	return nil
}

func rebuildCapturedMemory(f *Fleet) error {
	m := f.Memory
	if err := captureProjection(f.Files, f.AbsentFiles, "services/memory", m.Files, m.AbsentFiles, true); err != nil {
		return err
	}
	if !slices.Contains(m.AbsentFiles, "state/intent.json") {
		return errors.New("capture does not prove absence of a Memory transaction")
	}
	file, err := capturedFile(m.Files, "state/state.json")
	if err != nil {
		return err
	}
	rebuilt := Memory{Files: m.Files, AbsentFiles: m.AbsentFiles, Skipped: m.Skipped}
	if err := decode(file.Data, &rebuilt.State); err != nil {
		return errors.New("invalid captured Memory state")
	}
	if err := rebuilt.State.validate(f.ID); err != nil {
		return err
	}
	for _, file := range m.Files {
		if !strings.HasPrefix(file.Path, "memory/") {
			continue
		}
		name := strings.TrimPrefix(file.Path, "memory/")
		if path.Base(name) != name || !strings.HasSuffix(name, ".md") {
			return errors.New("invalid captured Memory entry path")
		}
		entry, err := decodeMemoryEntry(name, file.Data, rebuilt.State, f.ID)
		if err != nil {
			return err
		}
		rebuilt.Entries = append(rebuilt.Entries, entry)
	}
	if !sameCaptureJSON(rebuilt, *m) {
		return errors.New("captured Memory projection differs from original bytes")
	}
	*m = rebuilt
	return nil
}
