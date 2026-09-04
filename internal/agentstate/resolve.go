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

	"github.com/juex-ai/juex/internal/homestore"
)

const (
	agentFileName    = "agent.json"
	generatedIDBytes = 4
	generatedIDChars = 6
)

var validAgentID = regexp.MustCompile(`^[a-z2-7]{6}$`)

type Agent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
	Enabled   bool      `json:"enabled"`
	Autostart bool      `json:"autostart"`
	CreatedAt time.Time `json:"created_at"`
}

type Resolution struct {
	Agent   Agent
	Address AgentAddress
	Created bool
}

type Options struct {
	HomeDir string
	WorkDir string
}

type NoAgentError struct {
	WorkDir string
}

func (e *NoAgentError) Error() string {
	return fmt.Sprintf(
		"agentstate: no agent is registered for workspace %s; run juex send or listen to create a durable workspace agent",
		e.WorkDir,
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
	workspaceLock, err := homestore.AcquireLock(workspaceLockPath(homeDir, workDir), homestore.LockWait)
	if err != nil {
		return Resolution{}, fmt.Errorf("agentstate: lock workspace %s: %w", workDir, err)
	}
	defer func() { _ = workspaceLock.Close() }()

	existing, found, err := findWorkspaceAgent(homeDir, workDir)
	if err != nil {
		return Resolution{}, err
	}
	if found {
		agentLock, err := acquireAgentLock(homeDir, existing.ID)
		if err != nil {
			return Resolution{}, err
		}
		defer func() { _ = agentLock.Close() }()
		current, err := loadValidRegistryEntry(homeDir, existing.ID)
		if err != nil {
			return Resolution{}, err
		}
		sameWorkspace, err := sameWorkspaceDirectory(current.Agent.Workspace, workDir)
		if err != nil {
			return Resolution{}, fmt.Errorf("agentstate: compare workspace registration for %s: %w", workDir, err)
		}
		if !sameWorkspace {
			return Resolution{}, fmt.Errorf("agentstate: workspace registration changed while resolving %s", workDir)
		}
		return Resolution{Agent: current.Agent, Address: current.Address}, nil
	}
	return createIdentity(homeDir, workDir)
}

// ResolveExisting reads an existing workspace identity without mutating the
// registry.
func ResolveExisting(opts Options) (Resolution, error) {
	workDir, err := canonicalExistingDir(opts.WorkDir)
	if err != nil {
		return Resolution{}, fmt.Errorf("agentstate: resolve workspace: %w", err)
	}
	homeDir, err := resolveHome(opts.HomeDir)
	if err != nil {
		return Resolution{}, err
	}
	entry, found, err := findWorkspaceAgent(homeDir, workDir)
	if err != nil {
		return Resolution{}, err
	}
	if !found {
		return Resolution{}, &NoAgentError{WorkDir: workDir}
	}
	return Resolution{Agent: entry.Agent, Address: entry.Address}, nil
}

// ResolveByID reads an existing Agent directly from the effective Home
// registry. It is used by Fleet-launched runtimes so both Workspace and state
// location come from agent.json rather than process working-directory hints.
func ResolveByID(opts Options, agentID string) (Resolution, error) {
	homeDir, err := resolveHome(opts.HomeDir)
	if err != nil {
		return Resolution{}, err
	}
	entry, err := loadValidRegistryEntry(homeDir, agentID)
	if err != nil {
		return Resolution{}, err
	}
	binding := InspectBinding(entry)
	if binding.Kind != WorkspaceBound {
		return Resolution{}, fmt.Errorf("agentstate: agent %q has %s workspace binding: %s", agentID, binding.Kind, binding.Reason)
	}
	return Resolution{Agent: entry.Agent, Address: entry.Address}, nil
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

func createIdentity(homeDir, workDir string) (Resolution, error) {
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
	address, err := NewAgentAddress(homeDir, agentID)
	if err != nil {
		return Resolution{}, err
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
	if err := publishNewAgent(address, agent); err != nil {
		return Resolution{}, err
	}
	return Resolution{
		Agent:   agent,
		Address: address,
		Created: true,
	}, nil
}

func findWorkspaceAgent(homeDir, workDir string) (RegistryEntry, bool, error) {
	entries, err := ListRegistry(homeDir)
	if err != nil {
		return RegistryEntry{}, false, err
	}
	var matched *RegistryEntry
	for i := range entries {
		entry := entries[i]
		if strings.TrimSpace(entry.Agent.Workspace) == "" {
			continue
		}
		sameWorkspace, err := sameWorkspaceDirectory(entry.Agent.Workspace, workDir)
		if err != nil {
			return RegistryEntry{}, false, fmt.Errorf("agentstate: compare registered workspace %s with %s: %w", entry.Agent.Workspace, workDir, err)
		}
		if !sameWorkspace {
			continue
		}
		if entry.Problem != "" {
			return RegistryEntry{}, false, fmt.Errorf("agentstate: workspace %s is registered by invalid agent %q: %s", workDir, entry.ID, entry.Problem)
		}
		if matched != nil {
			return RegistryEntry{}, false, fmt.Errorf("agentstate: workspace %s is registered by multiple agents %q and %q", workDir, matched.ID, entry.ID)
		}
		matched = &entry
	}
	if matched == nil {
		return RegistryEntry{}, false, nil
	}
	return *matched, true, nil
}

func sameWorkspaceDirectory(left, right string) (bool, error) {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true, nil
	}
	leftInfo, err := os.Stat(left)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func acquireAgentLock(homeDir, agentID string) (*homestore.Lock, error) {
	guard, err := homestore.New(homeDir).Lock(homestore.AgentLocks, agentID, homestore.LockWait)
	if err != nil {
		return nil, fmt.Errorf("agentstate: lock agent %q: %w", agentID, err)
	}
	return guard, nil
}

func randomID() (string, error) {
	var raw [generatedIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	return strings.ToLower(encoded[:generatedIDChars]), nil
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
	return homestore.WriteFileAtomic(path, data, perm, 0o755)
}
