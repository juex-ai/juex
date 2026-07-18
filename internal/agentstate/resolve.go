// Package agentstate resolves the durable identity and state home bound to a
// JueX workspace.
package agentstate

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	markerName       = "juex.local.json"
	agentFileName    = "agent.json"
	globalExclude    = "**/juex.local.json"
	generatedIDBytes = 10
)

var validAgentID = regexp.MustCompile(`^[a-z2-7]{8,32}$`)

type Marker struct {
	AgentID string `json:"agent_id"`
}

type Agent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
	Enabled   bool      `json:"enabled"`
	Autostart bool      `json:"autostart"`
	CreatedAt time.Time `json:"created_at"`
}

type Resolution struct {
	Agent      Agent
	HomeDir    string
	AgentDir   string
	MarkerPath string
	Created    bool
	Notices    []string
}

type Options struct {
	HomeDir string
	WorkDir string
}

type UnknownAgentError struct {
	AgentID   string
	Marker    string
	HomeDir   string
	AgentFile string
}

func (e *UnknownAgentError) Error() string {
	return fmt.Sprintf(
		"agentstate: marker %s references agent %q, but %s is missing under effective home %s; restore that agent directory or remove the marker only if this workspace should mint a new identity",
		e.Marker, e.AgentID, e.AgentFile, e.HomeDir,
	)
}

type WorkspaceCopyError struct {
	AgentID           string
	RecordedWorkspace string
	CurrentWorkspace  string
	Marker            string
}

func (e *WorkspaceCopyError) Error() string {
	return fmt.Sprintf(
		"agentstate: workspace %s appears to be a copy of %s for agent %q; remove %s from the copied workspace and retry to mint a new identity",
		e.CurrentWorkspace, e.RecordedWorkspace, e.AgentID, e.Marker,
	)
}

var (
	now        = time.Now
	generateID = randomID
)

func EffectiveHome() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("JUEX_HOME")); configured != "" {
		return canonicalPath(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agentstate: resolve user home: %w", err)
	}
	return canonicalPath(filepath.Join(home, ".juex"))
}

func Resolve(opts Options) (Resolution, error) {
	workDir, err := canonicalExistingDir(opts.WorkDir)
	if err != nil {
		return Resolution{}, fmt.Errorf("agentstate: resolve workspace: %w", err)
	}
	homeDir, err := resolveHome(opts.HomeDir)
	if err != nil {
		return Resolution{}, err
	}
	if err := os.MkdirAll(filepath.Join(homeDir, "agents"), 0o755); err != nil {
		return Resolution{}, fmt.Errorf("agentstate: create registry under %s: %w", homeDir, err)
	}
	workspaceLock, err := acquireLockGuard(workspaceLockPath(homeDir, workDir))
	if err != nil {
		return Resolution{}, fmt.Errorf("agentstate: lock workspace %s: %w", workDir, err)
	}
	defer func() { _ = workspaceLock.Close() }()

	markerPath := filepath.Join(workDir, ".juex", markerName)
	marker, exists, err := loadMarker(markerPath)
	if err != nil {
		return Resolution{}, err
	}
	if !exists {
		if err := ensureGlobalExclude(); err != nil {
			return Resolution{}, err
		}
		return createIdentity(homeDir, workDir, markerPath)
	}
	result, err := resolveExistingIdentity(homeDir, workDir, markerPath, marker)
	if err != nil {
		return Resolution{}, err
	}
	if err := ensureGlobalExclude(); err != nil {
		return Resolution{}, err
	}
	return result, nil
}

func resolveHome(explicit string) (string, error) {
	if strings.TrimSpace(explicit) == "" {
		return EffectiveHome()
	}
	home, err := canonicalPath(explicit)
	if err != nil {
		return "", fmt.Errorf("agentstate: resolve effective home %q: %w", explicit, err)
	}
	return home, nil
}

func createIdentity(homeDir, workDir, markerPath string) (Resolution, error) {
	agentsDir := filepath.Join(homeDir, "agents")
	var agentID string
	for range 10 {
		candidate, err := generateID()
		if err != nil {
			return Resolution{}, fmt.Errorf("agentstate: generate agent id: %w", err)
		}
		if !validAgentID.MatchString(candidate) {
			return Resolution{}, fmt.Errorf("agentstate: generated invalid agent id %q", candidate)
		}
		if _, err := os.Lstat(filepath.Join(agentsDir, candidate)); errors.Is(err, os.ErrNotExist) {
			agentID = candidate
			break
		} else if err != nil {
			return Resolution{}, fmt.Errorf("agentstate: inspect candidate agent %q: %w", candidate, err)
		}
	}
	if agentID == "" {
		return Resolution{}, errors.New("agentstate: could not allocate a unique agent id")
	}

	agentLock, err := acquireAgentLock(homeDir, agentID)
	if err != nil {
		return Resolution{}, err
	}
	defer func() { _ = agentLock.Close() }()

	agent := Agent{
		ID:        agentID,
		Name:      filepath.Base(workDir),
		Workspace: workDir,
		Enabled:   true,
		Autostart: false,
		CreatedAt: now().UTC(),
	}
	agentDir, migrated, err := publishNewAgent(homeDir, workDir, agent)
	if err != nil {
		return Resolution{}, err
	}
	if err := atomicWriteJSON(markerPath, Marker{AgentID: agentID}, 0o644); err != nil {
		var persisted Marker
		if readErr := readJSON(markerPath, &persisted); readErr != nil || persisted.AgentID != agentID {
			_ = os.RemoveAll(agentDir)
		}
		return Resolution{}, fmt.Errorf("agentstate: write marker %s: %w", markerPath, err)
	}
	if migrated {
		if err := removeLegacyState(workDir); err != nil {
			return Resolution{}, fmt.Errorf("agentstate: agent %q is published but legacy state cleanup failed: %w", agentID, err)
		}
	}
	result := Resolution{
		Agent:      agent,
		HomeDir:    homeDir,
		AgentDir:   agentDir,
		MarkerPath: markerPath,
		Created:    true,
	}
	if migrated {
		result.Notices = append(result.Notices,
			fmt.Sprintf("migrated workspace runtime state from %s to %s", filepath.Join(workDir, ".juex"), agentDir))
	}
	return result, nil
}

func resolveExistingIdentity(homeDir, workDir, markerPath string, marker Marker) (Resolution, error) {
	if !validAgentID.MatchString(marker.AgentID) {
		return Resolution{}, fmt.Errorf("agentstate: marker %s contains invalid agent_id %q", markerPath, marker.AgentID)
	}
	agentLock, err := acquireAgentLock(homeDir, marker.AgentID)
	if err != nil {
		return Resolution{}, err
	}
	defer func() { _ = agentLock.Close() }()

	agentDir := filepath.Join(homeDir, "agents", marker.AgentID)
	agentFile := filepath.Join(agentDir, agentFileName)
	info, statErr := os.Stat(agentDir)
	if errors.Is(statErr, os.ErrNotExist) {
		return Resolution{}, &UnknownAgentError{
			AgentID: marker.AgentID, Marker: markerPath, HomeDir: homeDir, AgentFile: agentFile,
		}
	}
	if statErr != nil {
		return Resolution{}, fmt.Errorf("agentstate: inspect agent directory %s: %w", agentDir, statErr)
	}
	if !info.IsDir() {
		return Resolution{}, fmt.Errorf("agentstate: registry path %s for agent %q is not a directory", agentDir, marker.AgentID)
	}
	var agent Agent
	if err := readJSON(agentFile, &agent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Resolution{}, &UnknownAgentError{
				AgentID: marker.AgentID, Marker: markerPath, HomeDir: homeDir, AgentFile: agentFile,
			}
		}
		return Resolution{}, fmt.Errorf("agentstate: read %s for marker %s: %w", agentFile, markerPath, err)
	}
	if agent.ID != marker.AgentID {
		return Resolution{}, fmt.Errorf("agentstate: marker %s references %q but %s contains id %q", markerPath, marker.AgentID, agentFile, agent.ID)
	}
	if !filepath.IsAbs(agent.Workspace) {
		return Resolution{}, fmt.Errorf("agentstate: %s contains invalid workspace %q; expected an absolute path", agentFile, agent.Workspace)
	}
	if agent.CreatedAt.IsZero() {
		return Resolution{}, fmt.Errorf("agentstate: %s contains an empty created_at", agentFile)
	}

	result := Resolution{Agent: agent, HomeDir: homeDir, AgentDir: agentDir, MarkerPath: markerPath}
	same, recordedExists, err := sameWorkspace(agent.Workspace, workDir)
	if err != nil {
		return Resolution{}, fmt.Errorf("agentstate: compare workspace binding for agent %q: %w", agent.ID, err)
	}
	if !same {
		if recordedExists {
			return Resolution{}, &WorkspaceCopyError{
				AgentID: agent.ID, RecordedWorkspace: agent.Workspace,
				CurrentWorkspace: workDir, Marker: markerPath,
			}
		}
		old := agent.Workspace
		agent.Workspace = workDir
		if err := atomicWriteJSON(agentFile, agent, 0o644); err != nil {
			return Resolution{}, fmt.Errorf("agentstate: rebind moved workspace in %s: %w", agentFile, err)
		}
		result.Agent = agent
		result.Notices = append(result.Notices,
			fmt.Sprintf("workspace for agent %q moved from %s to %s", agent.ID, old, workDir))
	}
	cleaned, err := migratePublishedLegacyState(workDir, agentDir)
	if err != nil {
		return Resolution{}, err
	}
	if cleaned {
		result.Notices = append(result.Notices,
			fmt.Sprintf("migrated remaining workspace runtime state from %s to %s", filepath.Join(workDir, ".juex"), agentDir))
	}
	return result, nil
}

func loadMarker(path string) (Marker, bool, error) {
	var marker Marker
	if err := readJSON(path, &marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Marker{}, false, nil
		}
		return Marker{}, false, fmt.Errorf("agentstate: read marker %s: %w", path, err)
	}
	return marker, true, nil
}

func acquireAgentLock(homeDir, agentID string) (*lockGuard, error) {
	lockDir := filepath.Join(homeDir, ".locks", "agents")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("agentstate: create agent lock directory: %w", err)
	}
	guard, err := acquireLockGuard(filepath.Join(lockDir, agentID+".lock"))
	if err != nil {
		return nil, fmt.Errorf("agentstate: lock agent %q: %w", agentID, err)
	}
	return guard, nil
}

func sameWorkspace(recorded, current string) (same bool, recordedExists bool, err error) {
	recorded = filepath.Clean(recorded)
	current = filepath.Clean(current)
	if recorded == current {
		return true, true, nil
	}
	recordedInfo, err := os.Stat(recorded)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, err
	}
	currentInfo, err := os.Stat(current)
	if err != nil {
		return false, true, err
	}
	return os.SameFile(recordedInfo, currentInfo), true, nil
}

func randomID() (string, error) {
	var raw [generatedIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])), nil
}

func canonicalExistingDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = cwd
	}
	path, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return path, nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return abs, nil
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func atomicWriteJSON(path string, value any, perm os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, perm)
}
