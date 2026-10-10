package native

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const maxPatchFileBytes = 8 << 20
const maxPatchBytes = 32 << 20

type patchFile struct {
	data []byte
	info os.FileInfo
}

type patchMutation struct {
	path          string
	before, after patchFile
	parent        *os.Root
	name, staged  string
	changed       bool
}

func runPatch(ctx context.Context, directory, text string, output io.Writer) error {
	if len(text) > 2<<20 || !utf8.ValidString(text) {
		return execprotocol.ErrInvalid
	}
	ops, err := parsePatch(text)
	if err != nil {
		return err
	}
	if len(ops) > 512 {
		return execprotocol.ErrQuota
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	mutations, summary, err := planNativePatch(ctx, root, directory, ops)
	if err != nil {
		return err
	}
	if err := applyNativePatch(ctx, root, mutations); err != nil {
		return err
	}
	if _, err := io.WriteString(output, summary); err != nil {
		// Publication already succeeded. A missing acknowledgement is not proof
		// that a model may safely repeat these edits under another operation ID.
		return errors.Join(execprotocol.ErrOutcomeUnknown, err)
	}
	return nil
}

// Resolve existing symlinks once for identity, then perform every later access
// through os.Root. An absolute path is accepted only inside the bound directory.
func patchPath(directory, path string) (string, error) {
	if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return "", execprotocol.ErrInvalid
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(directory, path)
	}
	path = filepath.Clean(path)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// Removing a named symlink and removing its target are different edits.
		// Reject file symlinks rather than silently changing that identity.
		return "", errors.New("patch does not edit file symlink entries")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var tail []string
	ancestor := path
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return patchRelative(directory, resolved)
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(ancestor) == ancestor {
			return "", err
		}
		tail = append(tail, filepath.Base(ancestor))
		ancestor = filepath.Dir(ancestor)
	}
}

// Compare real ancestor identities, so differently cased absolute paths on a
// case-insensitive volume remain valid without needing write access to cwd.
func patchRelative(directory, path string) (string, error) {
	rootInfo, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	var parts []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil && os.SameFile(info, rootInfo) {
			if len(parts) == 0 {
				return "", execprotocol.ErrInvalid
			}
			for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
				parts[i], parts[j] = parts[j], parts[i]
			}
			return filepath.Join(parts...), nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", execprotocol.ErrDenied
		}
		parts = append(parts, filepath.Base(current))
	}
}

type patchIdentity struct {
	parent          os.FileInfo
	suffix          string
	parentPath      string
	caseInsensitive bool
}

func patchPathIdentity(root *os.Root, path string) (patchIdentity, error) {
	parent, suffix := filepath.Dir(path), filepath.Base(path)
	for {
		directory, err := root.OpenRoot(parent)
		if err == nil {
			defer func() { _ = directory.Close() }()
			info, err := directory.Stat(".")
			if err != nil {
				return patchIdentity{}, err
			}
			fold, err := patchCaseInsensitive(directory)
			if err != nil {
				return patchIdentity{}, err
			}
			if fold {
				suffix = strings.ToLower(suffix)
			}
			return patchIdentity{parent: info, suffix: suffix, parentPath: parent, caseInsensitive: fold}, nil
		}
		if !errors.Is(err, os.ErrNotExist) || parent == "." {
			return patchIdentity{}, err
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
}

func readPatchFile(root *os.Root, path string) (patchFile, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return patchFile{}, nil
	}
	if err != nil {
		return patchFile{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxPatchFileBytes {
		return patchFile{}, errors.New("patch requires regular UTF-8 files no larger than 8 MiB")
	}
	file, err := openRootRegular(root, path)
	if err != nil {
		return patchFile{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return patchFile{}, execprotocol.ErrConflict
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPatchFileBytes+1))
	if err != nil {
		return patchFile{}, err
	}
	after, err := file.Stat()
	if err != nil || len(data) > maxPatchFileBytes || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return patchFile{}, execprotocol.ErrConflict
	}
	if !utf8.Valid(data) {
		return patchFile{}, errors.New("patch requires UTF-8 content")
	}
	return patchFile{data: data, info: info}, nil
}

func planNativePatch(ctx context.Context, root *os.Root, directory string, ops []patchOperation) ([]*patchMutation, string, error) {
	var mutations []*patchMutation
	var summary strings.Builder
	fmt.Fprintf(&summary, "applied patch: %d file operations", len(ops))
	var identities []patchIdentity
	var existing []os.FileInfo
	used := 0
	target := func(path string) (*patchMutation, error) {
		rel, err := patchPath(directory, path)
		if err != nil {
			return nil, err
		}
		identity, err := patchPathIdentity(root, rel)
		if err != nil {
			return nil, err
		}
		for _, previous := range identities {
			if previous.suffix == identity.suffix && os.SameFile(previous.parent, identity.parent) {
				return nil, fmt.Errorf("patch has duplicate path %q", rel)
			}
		}
		identities = append(identities, identity)
		before, err := readPatchFile(root, rel)
		if err != nil {
			return nil, err
		}
		if before.info != nil {
			for _, info := range existing {
				if os.SameFile(info, before.info) {
					return nil, fmt.Errorf("patch has duplicate file identity %q", rel)
				}
			}
			existing = append(existing, before.info)
		}
		used += len(before.data)
		if used > maxPatchBytes {
			return nil, execprotocol.ErrQuota
		}
		m := &patchMutation{path: rel, before: before}
		mutations = append(mutations, m)
		return m, nil
	}
	for _, op := range ops {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		m, err := target(op.path)
		if err != nil {
			return nil, "", err
		}
		additions, deletions := 0, 0
		switch op.kind {
		case patchAdd:
			if m.before.info != nil {
				return nil, "", fmt.Errorf("patch add target %q exists", m.path)
			}
			content, lines := patchLinesContent(op.lines)
			m.after.data, additions = []byte(content), lines
		case patchDelete:
			if m.before.info == nil {
				return nil, "", os.ErrNotExist
			}
		case patchUpdate:
			if m.before.info == nil {
				return nil, "", os.ErrNotExist
			}
			content := string(m.before.data)
			if len(op.lines) == 0 && op.moveTo == "" {
				return nil, "", errors.New("patch update has no hunks")
			}
			if len(op.lines) > 0 {
				hunks, err := patchHunks(op.lines)
				if err != nil {
					return nil, "", err
				}
				content, additions, deletions, err = applyPatchHunks(m.path, content, hunks)
				if err != nil {
					return nil, "", err
				}
			}
			destination := m
			if op.moveTo != "" {
				destination, err = target(op.moveTo)
				if err != nil {
					return nil, "", err
				}
				if destination.before.info != nil {
					return nil, "", fmt.Errorf("patch move target %q exists", destination.path)
				}
				// Publish the destination before removing the source.
				mutations[len(mutations)-2], mutations[len(mutations)-1] = destination, m
				fmt.Fprintf(&summary, "\n- move %s -> %s", m.path, destination.path)
			}
			destination.after = patchFile{data: []byte(content), info: m.before.info}
		}
		fmt.Fprintf(&summary, "\n- %s %s (+%d -%d)", op.kind, m.path, additions, deletions)
	}
	for _, m := range mutations {
		used += len(m.after.data)
		if len(m.after.data) > maxPatchFileBytes || used > maxPatchBytes {
			return nil, "", execprotocol.ErrQuota
		}
	}
	return mutations, summary.String(), nil
}

// Probe the actual volume, not the OS name: APFS may be case sensitive and
// Linux may mount a case-insensitive filesystem. The private empty probe is
// removed before any target is changed; failures stop the patch.
func patchCaseInsensitive(root *os.Root) (bool, error) {
	name := ".juex-case-" + rand.Text()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	alternate, altErr := root.Stat(strings.ToUpper(name))
	removeErr := root.Remove(name)
	if err := errors.Join(statErr, closeErr, removeErr); err != nil {
		return false, err
	}
	if errors.Is(altErr, os.ErrNotExist) {
		return false, nil
	}
	if altErr != nil {
		return false, altErr
	}
	return os.SameFile(info, alternate), nil
}

func patchMatches(root *os.Root, path string, expected patchFile) error {
	actual, err := readPatchFile(root, path)
	if err != nil {
		return err
	}
	if expected.info == nil && actual.info == nil {
		return nil
	}
	if expected.info == nil || actual.info == nil || !os.SameFile(expected.info, actual.info) || expected.info.Mode().Perm() != actual.info.Mode().Perm() || !bytes.Equal(expected.data, actual.data) {
		return execprotocol.ErrConflict
	}
	return nil
}

func stagePatchFile(parent *os.Root, value patchFile) (string, os.FileInfo, error) {
	name := ".juex-patch-" + rand.Text()
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, err
	}
	mode := os.FileMode(0644)
	if value.info != nil {
		mode = value.info.Mode().Perm()
	}
	_, writeErr := file.Write(value.data)
	_, statErr := file.Stat()
	err = errors.Join(writeErr, statErr, file.Chmod(mode), file.Sync(), file.Close())
	if err != nil {
		_ = parent.Remove(name)
		return "", nil, err
	}
	// Refresh mode after Chmod for subsequent compare-before-rollback checks.
	info, err := parent.Stat(name)
	return name, info, err
}

func syncPatchParent(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func applyNativePatch(ctx context.Context, root *os.Root, mutations []*patchMutation) (err error) {
	defer func() {
		for _, m := range mutations {
			if m.parent != nil {
				if m.staged != "" {
					_ = m.parent.Remove(m.staged)
				}
				_ = m.parent.Close()
			}
		}
	}()
	// Stage complete contents before the first publication. Keep parent handles
	// open, so replacing a directory symlink cannot redirect later writes.
	for _, m := range mutations {
		if err := ctx.Err(); err != nil {
			return err
		}
		parent := filepath.Dir(m.path)
		if m.after.data != nil {
			if err := root.MkdirAll(parent, 0755); err != nil {
				return err
			}
		}
		m.parent, err = root.OpenRoot(parent)
		if err != nil {
			return err
		}
		m.name = filepath.Base(m.path)
		if err := patchMatches(m.parent, m.name, m.before); err != nil {
			return err
		}
		if m.after.data != nil {
			m.staged, m.after.info, err = stagePatchFile(m.parent, m.after)
			if err != nil {
				return err
			}
		}
	}
	defer func() {
		if err != nil {
			if rollbackErr := rollbackNativePatch(mutations); rollbackErr != nil {
				err = errors.Join(execprotocol.ErrOutcomeUnknown, err, rollbackErr)
			}
		}
	}()
	for _, m := range mutations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := patchMatches(m.parent, m.name, m.before); err != nil {
			return err
		}
		switch {
		case m.after.data == nil:
			err = m.parent.Remove(m.name)
		case m.before.info == nil:
			err = m.parent.Link(m.staged, m.name)
		default:
			err = m.parent.Rename(m.staged, m.name)
		}
		if err != nil {
			return err
		}
		m.changed = true
		if err := syncPatchParent(m.parent); err != nil {
			return err
		}
	}
	return nil
}

func rollbackNativePatch(mutations []*patchMutation) error {
	var failures []error
	for i := len(mutations) - 1; i >= 0; i-- {
		m := mutations[i]
		if !m.changed {
			continue
		}
		if err := patchMatches(m.parent, m.name, m.after); err != nil {
			failures = append(failures, err)
			continue
		}
		var err error
		if m.before.info == nil {
			err = m.parent.Remove(m.name)
		} else {
			var staged string
			staged, _, err = stagePatchFile(m.parent, m.before)
			if err == nil {
				if m.after.info == nil {
					err = m.parent.Link(staged, m.name)
				} else {
					err = m.parent.Rename(staged, m.name)
				}
				_ = m.parent.Remove(staged)
			}
		}
		failures = append(failures, err, syncPatchParent(m.parent))
	}
	return errors.Join(failures...)
}
