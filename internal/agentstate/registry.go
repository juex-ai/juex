package agentstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type RegistryEntry struct {
	ID      string
	Dir     string
	Agent   Agent
	Problem string
}

type BindingKind string

const (
	WorkspaceBound    BindingKind = "bound"
	WorkspaceOrphaned BindingKind = "orphaned"
	WorkspaceInvalid  BindingKind = "invalid"
)

type WorkspaceBinding struct {
	Kind   BindingKind
	Reason string
}

type AgentUpdate struct {
	Name      *string
	Enabled   *bool
	Autostart *bool
}

var removeWorkspaceMarker = os.Remove

func ListRegistry(homeDir string) ([]RegistryEntry, error) {
	agentsDir := filepath.Join(homeDir, "agents")
	info, err := os.Lstat(agentsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []RegistryEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agentstate: inspect registry %s: %w", agentsDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("agentstate: registry %s is not a directory", agentsDir)
	}

	dirEntries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, fmt.Errorf("agentstate: read registry %s: %w", agentsDir, err)
	}
	entries := make([]RegistryEntry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		if isDeletingTombstone(dirEntry.Name()) {
			continue
		}
		entries = append(entries, loadRegistryEntry(agentsDir, dirEntry.Name()))
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})
	return entries, nil
}

func UpdateAgent(homeDir, agentID string, update AgentUpdate) (Agent, error) {
	if !validAgentID.MatchString(agentID) {
		return Agent{}, fmt.Errorf("agentstate: invalid agent id %q", agentID)
	}
	guard, err := acquireAgentLock(homeDir, agentID)
	if err != nil {
		return Agent{}, err
	}
	defer func() { _ = guard.Close() }()

	entry, err := loadValidRegistryEntry(homeDir, agentID)
	if err != nil {
		return Agent{}, err
	}
	agent := entry.Agent
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if name == "" {
			return Agent{}, errors.New("agentstate: agent name must not be empty")
		}
		agent.Name = name
	}
	if update.Enabled != nil {
		agent.Enabled = *update.Enabled
	}
	if update.Autostart != nil {
		agent.Autostart = *update.Autostart
	}
	if err := atomicWriteJSON(filepath.Join(entry.Dir, agentFileName), agent, 0o644); err != nil {
		return Agent{}, fmt.Errorf("agentstate: update agent %q: %w", agentID, err)
	}
	return agent, nil
}

func WorkspaceHasMarker(workDir string) (bool, error) {
	markerPath := filepath.Join(workDir, ".juex", markerName)
	info, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agentstate: inspect workspace marker %s: %w", markerPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("agentstate: workspace marker %s is not a regular file", markerPath)
	}
	return true, nil
}

func InspectBinding(entry RegistryEntry) WorkspaceBinding {
	if entry.Problem != "" {
		return WorkspaceBinding{Kind: WorkspaceInvalid, Reason: entry.Problem}
	}
	if !validAgentID.MatchString(entry.ID) {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("invalid registry agent id %q", entry.ID),
		}
	}
	if !validAgentID.MatchString(entry.Agent.ID) || entry.Agent.ID != entry.ID {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("agent identity %q does not match registry id %q", entry.Agent.ID, entry.ID),
		}
	}
	if !filepath.IsAbs(entry.Agent.Workspace) {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("workspace %q is not absolute", entry.Agent.Workspace),
		}
	}
	if entry.Agent.CreatedAt.IsZero() {
		return WorkspaceBinding{Kind: WorkspaceInvalid, Reason: "agent created_at is empty"}
	}

	workspaceInfo, err := os.Stat(entry.Agent.Workspace)
	if errors.Is(err, os.ErrNotExist) {
		return WorkspaceBinding{
			Kind:   WorkspaceOrphaned,
			Reason: fmt.Sprintf("workspace %s does not exist", entry.Agent.Workspace),
		}
	}
	if err != nil {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("inspect workspace %s: %v", entry.Agent.Workspace, err),
		}
	}
	if !workspaceInfo.IsDir() {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("workspace %s is not a directory", entry.Agent.Workspace),
		}
	}

	markerPath := filepath.Join(entry.Agent.Workspace, ".juex", markerName)
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("inspect workspace marker %s: %v", markerPath, err),
		}
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("workspace marker %s is not a regular file", markerPath),
		}
	}
	var marker Marker
	if err := readJSON(markerPath, &marker); err != nil {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("read workspace marker %s: %v", markerPath, err),
		}
	}
	if !validAgentID.MatchString(marker.AgentID) {
		return WorkspaceBinding{
			Kind:   WorkspaceInvalid,
			Reason: fmt.Sprintf("workspace marker %s contains invalid agent_id %q", markerPath, marker.AgentID),
		}
	}
	if marker.AgentID != entry.ID {
		return WorkspaceBinding{
			Kind: WorkspaceOrphaned,
			Reason: fmt.Sprintf(
				"workspace marker %s references agent %q instead of %q",
				markerPath, marker.AgentID, entry.ID,
			),
		}
	}
	return WorkspaceBinding{
		Kind:   WorkspaceBound,
		Reason: fmt.Sprintf("workspace marker %s matches agent %q", markerPath, entry.ID),
	}
}

func DeleteOrphan(homeDir, agentID string) error {
	if !validAgentID.MatchString(agentID) {
		return fmt.Errorf("agentstate: invalid agent id %q", agentID)
	}

	guard, err := acquireAgentLock(homeDir, agentID)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()

	agentsDir := filepath.Join(homeDir, "agents")
	registryInfo, err := os.Lstat(agentsDir)
	if err != nil {
		return fmt.Errorf("agentstate: inspect registry %s: %w", agentsDir, err)
	}
	if registryInfo.Mode()&os.ModeSymlink != 0 || !registryInfo.IsDir() {
		return fmt.Errorf("agentstate: registry %s is not a directory", agentsDir)
	}

	entry := loadRegistryEntry(agentsDir, agentID)
	if entry.Problem != "" {
		return fmt.Errorf("agentstate: refuse to delete agent %q: %s", agentID, entry.Problem)
	}
	binding := InspectBinding(entry)
	if binding.Kind != WorkspaceOrphaned {
		return fmt.Errorf(
			"agentstate: refuse to delete agent %q with %s workspace binding: %s",
			agentID, binding.Kind, binding.Reason,
		)
	}

	tombstone, err := renameOrphanToTombstone(agentsDir, entry.Dir, agentID)
	if err != nil {
		return err
	}
	if err := syncRegistryDir(agentsDir); err != nil {
		return fmt.Errorf(
			"agentstate: orphan %q was renamed to tombstone %s but registry sync failed: %w",
			agentID, tombstone, err,
		)
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf(
			"agentstate: remove orphan %q tombstone %s: %w",
			agentID, tombstone, err,
		)
	}
	return nil
}

func DeleteRegistered(homeDir, agentID string) error {
	if !validAgentID.MatchString(agentID) {
		return fmt.Errorf("agentstate: invalid agent id %q", agentID)
	}
	preliminary, err := loadValidRegistryEntry(homeDir, agentID)
	if err != nil {
		return err
	}

	workspaceGuard, err := acquireLockGuard(workspaceLockPath(homeDir, preliminary.Agent.Workspace))
	if err != nil {
		return fmt.Errorf("agentstate: lock workspace %s: %w", preliminary.Agent.Workspace, err)
	}
	defer func() { _ = workspaceGuard.Close() }()

	agentGuard, err := acquireAgentLock(homeDir, agentID)
	if err != nil {
		return err
	}
	defer func() { _ = agentGuard.Close() }()

	entry, err := loadValidRegistryEntry(homeDir, agentID)
	if err != nil {
		return err
	}
	if entry.Agent.Workspace != preliminary.Agent.Workspace {
		return fmt.Errorf(
			"agentstate: refuse to delete agent %q because workspace changed from %s to %s",
			agentID,
			preliminary.Agent.Workspace,
			entry.Agent.Workspace,
		)
	}

	removeMarker, markerPath, err := registeredMarkerCleanup(entry)
	if err != nil {
		return err
	}
	agentsDir := filepath.Join(homeDir, "agents")
	tombstone, err := renameOrphanToTombstone(agentsDir, entry.Dir, agentID)
	if err != nil {
		return err
	}
	if err := syncRegistryDir(agentsDir); err != nil {
		restoreErr := restoreRegistryTombstone(agentsDir, tombstone, entry.Dir)
		return errors.Join(
			fmt.Errorf(
				"agentstate: agent %q was renamed to tombstone %s but registry sync failed: %w",
				agentID,
				tombstone,
				err,
			),
			restoreErr,
		)
	}

	if removeMarker {
		if err := removeWorkspaceMarker(markerPath); err != nil {
			restoreErr := restoreRegistryTombstone(agentsDir, tombstone, entry.Dir)
			return errors.Join(
				fmt.Errorf("agentstate: remove workspace marker %s: %w", markerPath, err),
				restoreErr,
			)
		}
		if err := syncRegistryDir(filepath.Dir(markerPath)); err != nil {
			markerRestoreErr := atomicWriteJSON(markerPath, Marker{AgentID: agentID}, 0o644)
			registryRestoreErr := restoreRegistryTombstone(agentsDir, tombstone, entry.Dir)
			return errors.Join(
				fmt.Errorf("agentstate: sync workspace marker directory %s: %w", filepath.Dir(markerPath), err),
				markerRestoreErr,
				registryRestoreErr,
			)
		}
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("agentstate: remove agent %q tombstone %s: %w", agentID, tombstone, err)
	}
	return nil
}

func registeredMarkerCleanup(entry RegistryEntry) (bool, string, error) {
	workspaceInfo, err := os.Lstat(entry.Agent.Workspace)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("agentstate: inspect workspace %s: %w", entry.Agent.Workspace, err)
	}
	if workspaceInfo.Mode()&os.ModeSymlink != 0 || !workspaceInfo.IsDir() {
		return false, "", fmt.Errorf(
			"agentstate: refuse to delete agent %q because workspace %s is not a real directory",
			entry.ID,
			entry.Agent.Workspace,
		)
	}
	markerPath := filepath.Join(entry.Agent.Workspace, ".juex", markerName)
	marker, exists, err := loadMarker(markerPath)
	if err != nil {
		return false, "", err
	}
	if !exists {
		return false, "", nil
	}
	if !validAgentID.MatchString(marker.AgentID) {
		return false, "", fmt.Errorf(
			"agentstate: marker %s contains invalid agent_id %q",
			markerPath,
			marker.AgentID,
		)
	}
	return marker.AgentID == entry.ID, markerPath, nil
}

func restoreRegistryTombstone(agentsDir, tombstone, agentDir string) error {
	if err := os.Rename(tombstone, agentDir); err != nil {
		return fmt.Errorf("agentstate: restore registry tombstone %s to %s: %w", tombstone, agentDir, err)
	}
	if err := syncRegistryDir(agentsDir); err != nil {
		return fmt.Errorf("agentstate: sync restored registry %s: %w", agentsDir, err)
	}
	return nil
}

func loadValidRegistryEntry(homeDir, agentID string) (RegistryEntry, error) {
	agentsDir := filepath.Join(homeDir, "agents")
	registryInfo, err := os.Lstat(agentsDir)
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("agentstate: inspect registry %s: %w", agentsDir, err)
	}
	if registryInfo.Mode()&os.ModeSymlink != 0 || !registryInfo.IsDir() {
		return RegistryEntry{}, fmt.Errorf("agentstate: registry %s is not a directory", agentsDir)
	}
	entry := loadRegistryEntry(agentsDir, agentID)
	if entry.Problem != "" {
		return RegistryEntry{}, fmt.Errorf("agentstate: invalid agent %q: %s", agentID, entry.Problem)
	}
	return entry, nil
}

func isDeletingTombstone(name string) bool {
	rest := strings.TrimPrefix(name, ".")
	if rest == name {
		return false
	}
	agentID, suffix, ok := strings.Cut(rest, ".deleting-")
	return ok && validAgentID.MatchString(agentID) && validAgentID.MatchString(suffix)
}

func renameOrphanToTombstone(agentsDir, agentDir, agentID string) (string, error) {
	for range 10 {
		suffix, err := randomID()
		if err != nil {
			return "", fmt.Errorf("agentstate: generate orphan tombstone name: %w", err)
		}
		tombstone := filepath.Join(agentsDir, "."+agentID+".deleting-"+suffix)
		if _, err := os.Lstat(tombstone); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("agentstate: inspect orphan tombstone %s: %w", tombstone, err)
		}
		if err := os.Rename(agentDir, tombstone); err != nil {
			return "", fmt.Errorf(
				"agentstate: rename orphan %q to tombstone %s: %w",
				agentID, tombstone, err,
			)
		}
		return tombstone, nil
	}
	return "", fmt.Errorf("agentstate: could not allocate tombstone for orphan %q", agentID)
}

func syncRegistryDir(path string) error {
	err := syncDir(path)
	if errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.ENOSYS) {
		return nil
	}
	return err
}

func loadRegistryEntry(agentsDir, id string) RegistryEntry {
	entry := RegistryEntry{
		ID:  id,
		Dir: filepath.Join(agentsDir, id),
	}
	if !validAgentID.MatchString(id) {
		entry.Problem = fmt.Sprintf("invalid registry agent id %q", id)
		return entry
	}

	info, err := os.Lstat(entry.Dir)
	if err != nil {
		entry.Problem = fmt.Sprintf("inspect agent directory: %v", err)
		return entry
	}
	if info.Mode()&os.ModeSymlink != 0 {
		entry.Problem = "agent directory is a symbolic link"
		return entry
	}
	if !info.IsDir() {
		entry.Problem = "agent registry entry is not a directory"
		return entry
	}

	agentFile := filepath.Join(entry.Dir, agentFileName)
	fileInfo, err := os.Lstat(agentFile)
	if err != nil {
		entry.Problem = fmt.Sprintf("inspect %s: %v", agentFileName, err)
		return entry
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		entry.Problem = fmt.Sprintf("%s is not a regular file", agentFileName)
		return entry
	}
	if err := readJSON(agentFile, &entry.Agent); err != nil {
		entry.Problem = fmt.Sprintf("read %s: %v", agentFileName, err)
		return entry
	}
	if !validAgentID.MatchString(entry.Agent.ID) {
		entry.Problem = fmt.Sprintf("%s contains invalid id %q", agentFileName, entry.Agent.ID)
		return entry
	}
	if entry.Agent.ID != id {
		entry.Problem = fmt.Sprintf("%s contains id %q, want %q", agentFileName, entry.Agent.ID, id)
		return entry
	}
	if !filepath.IsAbs(entry.Agent.Workspace) {
		entry.Problem = fmt.Sprintf("%s contains non-absolute workspace %q", agentFileName, entry.Agent.Workspace)
		return entry
	}
	if entry.Agent.CreatedAt.IsZero() {
		entry.Problem = fmt.Sprintf("%s contains an empty created_at", agentFileName)
	}
	return entry
}
