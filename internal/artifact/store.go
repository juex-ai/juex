package artifact

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

const artifactPrefix = ".juex/artifacts"

// ErrIntegrity reports that stored bytes do not match their durable reference.
var ErrIntegrity = errors.New("artifact integrity check failed")

// Ref is a durable workspace-relative reference to stored artifact bytes.
type Ref struct {
	Path   string
	SHA256 string
	Bytes  int
}

// Store owns artifact path safety, atomic writes, and integrity verification
// beneath one workspace.
type Store struct {
	workDir string
}

// NewStore creates an artifact store rooted in workDir.
func NewStore(workDir string) (Store, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Store{}, fmt.Errorf("artifact store cwd: %w", err)
		}
		workDir = cwd
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return Store{}, fmt.Errorf("artifact store root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Store{}, fmt.Errorf("artifact store root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Store{}, fmt.Errorf("artifact store root: %w", err)
	}
	if !info.IsDir() {
		return Store{}, fmt.Errorf("artifact store root %q is not a directory", resolved)
	}
	return Store{workDir: resolved}, nil
}

// Put atomically stores data at a logical path relative to `.juex/artifacts`.
func (s Store) Put(relativePath string, data []byte) (Ref, error) {
	relativePath, err := normalizeRelativePath(relativePath)
	if err != nil {
		return Ref{}, err
	}
	ref := refForData(relativePath, data)
	target := filepath.FromSlash(ref.Path)
	root, err := os.OpenRoot(s.workDir)
	if err != nil {
		return Ref{}, fmt.Errorf("artifact store open: %w", err)
	}
	defer root.Close()

	if existing, readErr := root.ReadFile(target); readErr == nil {
		if bytes.Equal(existing, data) {
			return ref, nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Ref{}, fmt.Errorf("artifact inspect %q: %w", ref.Path, readErr)
	}
	if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Ref{}, fmt.Errorf("artifact mkdir %q: %w", ref.Path, err)
	}
	// A concurrent writer may have completed while the parent was created.
	if existing, readErr := root.ReadFile(target); readErr == nil && bytes.Equal(existing, data) {
		return ref, nil
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Ref{}, fmt.Errorf("artifact inspect %q: %w", ref.Path, readErr)
	}
	if err := writeAtomic(root, target, data); err != nil {
		return Ref{}, fmt.Errorf("artifact write %q: %w", ref.Path, err)
	}
	return ref, nil
}

// PutContentAddressed stores data under namespace using its SHA-256 digest.
func (s Store) PutContentAddressed(namespace, extension string, data []byte) (Ref, error) {
	namespace, err := normalizeRelativePath(namespace)
	if err != nil {
		return Ref{}, fmt.Errorf("artifact namespace: %w", err)
	}
	if err := validateExtension(extension); err != nil {
		return Ref{}, err
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + extension
	return s.Put(path.Join(namespace, name), data)
}

// Read returns verified artifact bytes. Empty SHA256 or Bytes values are
// treated as unspecified for compatibility with older references.
func (s Store) Read(ref Ref) ([]byte, error) {
	target, err := referenceTarget(ref.Path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.workDir)
	if err != nil {
		return nil, fmt.Errorf("artifact store open: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.FromSlash(target))
	if err != nil {
		return nil, fmt.Errorf("artifact read %q: %w", ref.Path, err)
	}
	if ref.Bytes > 0 && len(data) != ref.Bytes {
		return nil, fmt.Errorf("%w: %q bytes=%d want=%d", ErrIntegrity, ref.Path, len(data), ref.Bytes)
	}
	if ref.SHA256 != "" {
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, ref.SHA256) {
			return nil, fmt.Errorf("%w: %q sha256=%s want=%s", ErrIntegrity, ref.Path, got, ref.SHA256)
		}
	}
	return data, nil
}

func refForData(relativePath string, data []byte) Ref {
	sum := sha256.Sum256(data)
	return Ref{
		Path:   path.Join(artifactPrefix, relativePath),
		SHA256: hex.EncodeToString(sum[:]),
		Bytes:  len(data),
	}
}

func normalizeRelativePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || strings.ContainsAny(name, `\:`) || !fs.ValidPath(name) {
		return "", fmt.Errorf("unsafe artifact path %q", name)
	}
	name = path.Clean(name)
	if name == ".juex" || strings.HasPrefix(name, ".juex/") {
		return "", fmt.Errorf("artifact path %q must be relative to %s", name, artifactPrefix)
	}
	return name, nil
}

func referenceTarget(name string) (string, error) {
	if strings.Contains(name, `\`) || !fs.ValidPath(name) || !strings.HasPrefix(name, artifactPrefix+"/") {
		return "", fmt.Errorf("unsafe artifact reference %q", name)
	}
	relative := strings.TrimPrefix(name, artifactPrefix+"/")
	if _, err := normalizeRelativePath(relative); err != nil {
		return "", err
	}
	return path.Join(artifactPrefix, relative), nil
}

func validateExtension(extension string) error {
	if len(extension) < 2 || extension[0] != '.' || strings.Contains(extension, "..") {
		return fmt.Errorf("unsafe artifact extension %q", extension)
	}
	for _, r := range extension[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			return fmt.Errorf("unsafe artifact extension %q", extension)
		}
	}
	return nil
}

func writeAtomic(root *os.Root, target string, data []byte) error {
	temp, err := tempName(target)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return err
	}
	if written != len(data) {
		_ = file.Close()
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceArtifact(root, temp, target, data); err != nil {
		return err
	}
	return nil
}

func tempName(target string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	dir := filepath.Dir(target)
	name := fmt.Sprintf(".%s.%x.tmp", filepath.Base(target), suffix[:])
	return filepath.Join(dir, name), nil
}
