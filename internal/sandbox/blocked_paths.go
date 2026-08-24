package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type FilePolicyOptions struct {
	Policy        Policy
	WorkDir       string
	AgentStateDir string
	ReadOnlyPaths []string
}

type FilePolicy struct {
	enabled           bool
	restrictWrites    bool
	base              string
	blockedPaths      []blockedPath
	canonicalRoots    []string
	readOnlyRoots     []string
	scratchRoot       string
	commandWriteCheck *commandWriteCheckState
}

type PathGuard = FilePolicy

type blockedPath struct {
	original string
	variants []string
}

func NewFilePolicy(opts FilePolicyOptions) FilePolicy {
	base := sandboxPathBase(opts.WorkDir)
	readOnlyRoots := make([]string, 0, len(opts.ReadOnlyPaths))
	for _, raw := range opts.ReadOnlyPaths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if canonical, ok := canonicalPath(base, raw); ok {
			readOnlyRoots = append(readOnlyRoots, canonical)
		}
	}
	readOnlyRoots = dedupePaths(readOnlyRoots)
	if !opts.Policy.Enabled {
		return FilePolicy{base: base, readOnlyRoots: readOnlyRoots}
	}
	writable := []string{opts.WorkDir}
	if strings.TrimSpace(opts.AgentStateDir) != "" {
		writable = append(writable, opts.AgentStateDir)
	}
	canonicalRoots := make([]string, 0, len(writable))
	for _, raw := range writable {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if canonical, ok := canonicalPath(base, raw); ok {
			canonicalRoots = append(canonicalRoots, canonical)
		}
	}
	canonicalRoots = dedupePaths(canonicalRoots)
	scratchRoot := ""
	if strings.TrimSpace(opts.AgentStateDir) != "" {
		if root, ok := canonicalPath(base, opts.AgentStateDir); ok {
			scratchRoot = root
		}
	}

	roots := make([]blockedPath, 0, len(opts.Policy.FileSystem.BlockedPaths))
	for _, raw := range opts.Policy.FileSystem.BlockedPaths {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		variants := normalizedPathVariants(base, trimmed)
		if len(variants) == 0 {
			continue
		}
		roots = append(roots, blockedPath{original: trimmed, variants: variants})
	}
	return FilePolicy{
		enabled:           true,
		restrictWrites:    opts.Policy.FileSystem.OutsideWorkspace != OutsideWorkspaceReadWrite,
		base:              base,
		blockedPaths:      roots,
		canonicalRoots:    canonicalRoots,
		readOnlyRoots:     readOnlyRoots,
		scratchRoot:       scratchRoot,
		commandWriteCheck: &commandWriteCheckState{},
	}
}

type commandWriteCheckState struct {
	mu      sync.Mutex
	safe    bool
	running bool
	done    chan struct{}
}

func NewPathGuard(workDir string, policy Policy) PathGuard {
	return NewFilePolicy(FilePolicyOptions{Policy: policy, WorkDir: workDir})
}

func (g FilePolicy) Check(path string) error {
	return g.CheckRead(path)
}

func (g FilePolicy) CheckRead(path string) error {
	target, blocked, ok := g.blockedPath(path)
	if !ok {
		return nil
	}
	return fmt.Errorf("sandbox: blocked path %s matches file_system.blocked_paths entry %s", target, blocked)
}

func (g FilePolicy) CheckWrite(path string) error {
	if err := g.CheckRead(path); err != nil {
		return err
	}
	if !g.enabled && len(g.readOnlyRoots) == 0 {
		return nil
	}
	target, ok := canonicalPath(g.base, path)
	if !ok {
		return fmt.Errorf("sandbox: invalid write path %q", path)
	}
	for _, root := range g.readOnlyRoots {
		if pathWithinOrEqualFilesystem(root, target) {
			return fmt.Errorf("sandbox: write path %s is inside read-only root %s", target, root)
		}
	}
	if len(g.readOnlyRoots) > 0 {
		metadata, multiple, err := readHardLinkMetadata(target)
		if err != nil {
			return fmt.Errorf("sandbox: inspect write path %s: %w", target, err)
		}
		if multiple {
			root, matched, err := g.hardLinkReadOnlyRoot(context.Background(), metadata)
			if err != nil {
				return fmt.Errorf("sandbox: inspect read-only hard links for write path %s: %w", target, err)
			}
			if matched {
				return fmt.Errorf("sandbox: write path %s is a hard-link alias of read-only root %s", target, root)
			}
		}
	}
	if !g.restrictWrites {
		return nil
	}
	for _, root := range g.canonicalRoots {
		if pathWithinOrEqualFilesystem(root, target) {
			metadata, multiple, err := readHardLinkMetadata(target)
			if err != nil {
				return fmt.Errorf("sandbox: inspect write path %s: %w", target, err)
			}
			if multiple && !g.commandWriteCheckKnownSafe() {
				contained, err := g.hardLinkContained(context.Background(), metadata)
				if err != nil {
					return fmt.Errorf("sandbox: inspect hard links for write path %s: %w", target, err)
				}
				if contained {
					return nil
				}
				return fmt.Errorf("sandbox: write path %s has multiple hard links", target)
			}
			return nil
		}
	}
	return fmt.Errorf("sandbox: write path %s is outside writable roots", target)
}

func (g FilePolicy) CheckCommandWrites(ctx context.Context) error {
	if !g.enabled || !g.restrictWrites {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state := g.commandWriteCheck
	if state == nil {
		return g.scanCommandHardLinks(ctx)
	}
	for {
		state.mu.Lock()
		if state.safe {
			state.mu.Unlock()
			return nil
		}
		if state.running {
			done := state.done
			state.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		state.running = true
		state.done = make(chan struct{})
		done := state.done
		state.mu.Unlock()

		err := g.scanCommandHardLinks(ctx)
		state.mu.Lock()
		if err == nil {
			state.safe = true
		}
		state.running = false
		close(done)
		state.mu.Unlock()
		return err
	}
}

type hardLinkRecord struct {
	links   uint64
	aliases uint64
	path    string
}

func (g FilePolicy) scanCommandHardLinks(ctx context.Context) error {
	index, err := g.hardLinkIndex(ctx)
	if err != nil {
		return err
	}
	for _, record := range index {
		if record.aliases < record.links {
			return fmt.Errorf("writable root file with multiple hard links has %d of %d aliases inside writable roots: %s", record.aliases, record.links, record.path)
		}
	}
	return nil
}

func (g FilePolicy) hardLinkContained(ctx context.Context, target hardLinkMetadata) (bool, error) {
	index, err := g.hardLinkIndex(ctx)
	if err != nil {
		return false, err
	}
	record := index[target.identity]
	if record == nil {
		return false, nil
	}
	links := target.links
	if record.links > links {
		links = record.links
	}
	return record.aliases >= links, nil
}

func (g FilePolicy) hardLinkReadOnlyRoot(ctx context.Context, target hardLinkMetadata) (string, bool, error) {
	for _, root := range g.readOnlyRoots {
		matched := false
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			metadata, multiple, err := readHardLinkMetadata(path)
			if err != nil {
				return err
			}
			if multiple && metadata.identity == target.identity {
				matched = true
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", false, err
		}
		if matched {
			return root, true, nil
		}
	}
	return "", false, nil
}

func (g FilePolicy) hardLinkIndex(ctx context.Context) (map[hardLinkIdentity]*hardLinkRecord, error) {
	index := map[hardLinkIdentity]*hardLinkRecord{}
	for _, root := range g.commandWritableRoots() {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && g.isReadOnlyPath(path) {
				return filepath.SkipDir
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			metadata, multiple, err := readHardLinkMetadata(path)
			if err != nil {
				return err
			}
			if !multiple {
				return nil
			}
			record := index[metadata.identity]
			if record == nil {
				record = &hardLinkRecord{links: metadata.links, path: path}
				index[metadata.identity] = record
			}
			if metadata.links > record.links {
				record.links = metadata.links
			}
			record.aliases++
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return index, nil
}

func (g FilePolicy) commandWriteCheckKnownSafe() bool {
	state := g.commandWriteCheck
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.safe
}

func (g FilePolicy) commandWritableRoots() []string {
	var roots []string
	for _, candidate := range g.canonicalRoots {
		covered := false
		for _, root := range roots {
			if pathWithinOrEqualFilesystem(root, candidate) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		kept := roots[:0]
		for _, root := range roots {
			if !pathWithinOrEqualFilesystem(candidate, root) {
				kept = append(kept, root)
			}
		}
		roots = append(kept, candidate)
	}
	return roots
}

func (g FilePolicy) WritableRoots() []string {
	return append([]string(nil), g.canonicalRoots...)
}

func (g FilePolicy) ReadOnlyRoots() []string {
	return append([]string(nil), g.readOnlyRoots...)
}

func (g FilePolicy) ScratchRoot() string {
	return g.scratchRoot
}

func (g FilePolicy) IsBlocked(path string) bool {
	_, _, ok := g.blockedPath(path)
	return ok
}

func (g FilePolicy) isReadOnlyPath(path string) bool {
	target, ok := canonicalPath(g.base, path)
	if !ok {
		return false
	}
	for _, root := range g.readOnlyRoots {
		if pathWithinOrEqualFilesystem(root, target) {
			return true
		}
	}
	return false
}

func (g FilePolicy) blockedPath(path string) (string, string, bool) {
	if !g.enabled || len(g.blockedPaths) == 0 || strings.TrimSpace(path) == "" {
		return "", "", false
	}
	for _, target := range normalizedPathVariants(g.base, path) {
		for _, root := range g.blockedPaths {
			for _, variant := range root.variants {
				if pathWithinOrEqual(variant, target) {
					return target, root.original, true
				}
			}
		}
	}
	return "", "", false
}

func canonicalPath(base, raw string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", false
	}
	path = filepath.FromSlash(expandHomePath(path))
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	canonical, err := evalExistingPathPrefix(filepath.Clean(abs))
	if err != nil {
		return "", false
	}
	return canonical, true
}

func AppendBlockedPaths(existing []string, incoming []string) ([]string, error) {
	out := make([]string, 0, len(existing)+len(incoming))
	seen := map[string]struct{}{}
	for _, value := range existing {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, value := range incoming {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, fmt.Errorf("sandbox.file_system.blocked_paths entries must not be empty")
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out, nil
}

func normalizedBlockedPaths(base string, paths []string) []string {
	base = sandboxPathBase(base)
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, raw := range paths {
		for _, path := range normalizedPathVariants(base, raw) {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

func sandboxPathBase(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			workDir = cwd
		}
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	return filepath.Clean(workDir)
}

func normalizedPathVariants(base, raw string) []string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return nil
	}
	path = expandHomePath(path)
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	abs = filepath.Clean(abs)
	out := []string{abs}
	if eval, err := evalExistingPathPrefix(abs); err == nil && eval != abs {
		out = append(out, eval)
	}
	return dedupePaths(out)
}

func expandHomePath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func evalExistingPathPrefix(path string) (string, error) {
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(eval), nil
	}
	current := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			eval, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				eval = filepath.Join(eval, suffix[i])
			}
			return filepath.Clean(eval), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path), nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithinOrEqual(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if caseInsensitivePathMatch() {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathWithinOrEqualExact(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathWithinOrEqualFilesystem(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if pathWithinOrEqualExact(root, target) {
		return true
	}
	rootDepth := pathComponentCount(root)
	targetDepth := pathComponentCount(target)
	if targetDepth < rootDepth {
		return false
	}
	candidateRoot := target
	for range targetDepth - rootDepth {
		candidateRoot = filepath.Dir(candidateRoot)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	candidateInfo, err := os.Stat(candidateRoot)
	return err == nil && os.SameFile(rootInfo, candidateInfo)
}

func pathComponentCount(path string) int {
	rest := strings.TrimPrefix(filepath.Clean(path), filepath.VolumeName(path))
	rest = strings.Trim(rest, string(filepath.Separator))
	if rest == "" {
		return 0
	}
	return len(strings.Split(rest, string(filepath.Separator)))
}

func caseInsensitivePathMatch() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

func dedupePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}
