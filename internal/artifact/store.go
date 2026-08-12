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

// ErrIntegrity reports that stored bytes do not match their durable reference.
var ErrIntegrity = errors.New("artifact integrity check failed")

// ErrTooLarge reports that artifact bytes exceed a caller's read limit.
var ErrTooLarge = errors.New("artifact exceeds read limit")

// Ref is a durable path relative to one Agent's Artifact root.
type Ref struct {
	Path   string
	SHA256 string
	Bytes  int
}

// File is one safely enumerated Artifact file.
type File struct {
	Path string
	Data []byte
}

// Store owns artifact path safety, atomic writes, and integrity verification
// beneath one Agent Artifact directory.
type Store struct {
	artifactDir string
	parentDir   string
	rootName    string
}

// NewStore creates a store at artifactDir. The parent directory must already
// exist; the Artifact directory itself is created lazily on first write.
func NewStore(artifactDir string) (Store, error) {
	artifactDir = strings.TrimSpace(artifactDir)
	if artifactDir == "" {
		return Store{}, errors.New("artifact store: missing artifact directory")
	}
	abs, err := filepath.Abs(artifactDir)
	if err != nil {
		return Store{}, fmt.Errorf("artifact store root: %w", err)
	}
	rootName := filepath.Base(filepath.Clean(abs))
	if rootName == "." || rootName == string(filepath.Separator) || rootName == "" {
		return Store{}, fmt.Errorf("artifact store root %q is invalid", artifactDir)
	}
	parentDir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return Store{}, fmt.Errorf("artifact store root: %w", err)
	}
	info, err := os.Stat(parentDir)
	if err != nil {
		return Store{}, fmt.Errorf("artifact store root: %w", err)
	}
	if !info.IsDir() {
		return Store{}, fmt.Errorf("artifact store parent %q is not a directory", parentDir)
	}
	rootPath := filepath.Join(parentDir, rootName)
	if rootInfo, statErr := os.Lstat(rootPath); statErr == nil {
		if rootInfo.Mode()&os.ModeSymlink != 0 {
			return Store{}, fmt.Errorf("artifact store root %q is a symbolic link", rootPath)
		}
		if !rootInfo.IsDir() {
			return Store{}, fmt.Errorf("artifact store root %q is not a directory", rootPath)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Store{}, fmt.Errorf("artifact store root: %w", statErr)
	}
	return Store{artifactDir: rootPath, parentDir: parentDir, rootName: rootName}, nil
}

// Put atomically stores data at a logical path relative to the Artifact root.
func (s Store) Put(relativePath string, data []byte) (Ref, error) {
	relativePath, err := normalizeRelativePath(relativePath)
	if err != nil {
		return Ref{}, err
	}
	ref := refForData(relativePath, data)
	root, err := s.openRoot(true)
	if err != nil {
		return Ref{}, fmt.Errorf("artifact store open: %w", err)
	}
	defer func() { _ = root.Close() }()
	target := filepath.FromSlash(ref.Path)

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

// Read returns verified artifact bytes. Empty SHA256 or Bytes values request
// path-only lookup when a caller does not have integrity metadata.
func (s Store) Read(ref Ref) ([]byte, error) {
	return s.read(ref, 0)
}

// ReadLimit returns verified artifact bytes without reading more than maxBytes.
func (s Store) ReadLimit(ref Ref, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("artifact read limit must be positive")
	}
	if ref.Bytes > 0 && int64(ref.Bytes) > maxBytes {
		return nil, fmt.Errorf("%w: %q bytes=%d limit=%d", ErrTooLarge, ref.Path, ref.Bytes, maxBytes)
	}
	return s.read(ref, maxBytes)
}

func (s Store) read(ref Ref, maxBytes int64) ([]byte, error) {
	target, err := referenceTarget(ref.Path)
	if err != nil {
		return nil, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		return nil, fmt.Errorf("artifact store open: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.FromSlash(target))
	if err != nil {
		return nil, fmt.Errorf("artifact read %q: %w", ref.Path, err)
	}
	defer func() { _ = file.Close() }()
	var reader io.Reader = file
	if maxBytes > 0 {
		reader = io.LimitReader(file, maxBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("artifact read %q: %w", ref.Path, err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %q limit=%d", ErrTooLarge, ref.Path, maxBytes)
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
		Path:   relativePath,
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
	if strings.HasPrefix(strings.SplitN(name, "/", 2)[0], ".") {
		return "", fmt.Errorf("hidden artifact path %q is not supported", name)
	}
	return name, nil
}

func referenceTarget(name string) (string, error) {
	if strings.Contains(name, `\`) || !fs.ValidPath(name) {
		return "", fmt.Errorf("unsafe artifact reference %q", name)
	}
	if _, err := normalizeRelativePath(name); err != nil {
		return "", err
	}
	return name, nil
}

// Open opens one logical Artifact path for read-only adapter access and
// returns its file metadata. The caller owns the returned file.
func (s Store) Open(relativePath string) (*os.File, os.FileInfo, error) {
	target, err := referenceTarget(relativePath)
	if err != nil {
		return nil, nil, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		return nil, nil, fmt.Errorf("artifact store open: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.FromSlash(target))
	if err != nil {
		return nil, nil, fmt.Errorf("artifact open %q: %w", relativePath, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("artifact stat %q: %w", relativePath, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("artifact %q is not a regular file", relativePath)
	}
	return file, info, nil
}

// Files returns every regular Artifact file without following symlinks.
func (s Store) Files() ([]File, error) {
	root, err := s.openRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("artifact store open: %w", err)
	}
	defer func() { _ = root.Close() }()
	var files []File
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact path %q is a symbolic link", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact path %q is not a regular file", name)
		}
		data, err := fs.ReadFile(root.FS(), name)
		if err != nil {
			return err
		}
		logical := strings.TrimPrefix(filepath.ToSlash(name), "./")
		if _, err := referenceTarget(logical); err != nil {
			return err
		}
		files = append(files, File{Path: logical, Data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("artifact enumerate: %w", err)
	}
	return files, nil
}

// HasNamespace reports whether a logical Artifact path exists.
func (s Store) HasNamespace(relativePath string) (bool, error) {
	target, err := referenceTarget(relativePath)
	if err != nil {
		return false, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("artifact store open: %w", err)
	}
	defer func() { _ = root.Close() }()
	_, err = root.Stat(filepath.FromSlash(target))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("artifact inspect %q: %w", relativePath, err)
	}
	return true, nil
}

// RemoveNamespace recursively removes one logical Artifact namespace.
func (s Store) RemoveNamespace(relativePath string) error {
	target, err := referenceTarget(relativePath)
	if err != nil {
		return err
	}
	root, err := s.openRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("artifact store open: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(filepath.FromSlash(target)); err != nil {
		return fmt.Errorf("artifact remove %q: %w", relativePath, err)
	}
	return nil
}

func (s Store) openRoot(create bool) (*os.Root, error) {
	parent, err := os.OpenRoot(s.parentDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	rootInfo, err := os.Lstat(s.artifactDir)
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := parent.Mkdir(s.rootName, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return nil, mkdirErr
		}
		rootInfo, err = os.Lstat(s.artifactDir)
	}
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact store root %q is a symbolic link", s.artifactDir)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("artifact store root %q is not a directory", s.artifactDir)
	}
	root, err := os.OpenRoot(s.artifactDir)
	if err != nil {
		return nil, err
	}
	openedInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if !os.SameFile(rootInfo, openedInfo) {
		_ = root.Close()
		return nil, fmt.Errorf("artifact store root %q changed while opening", s.artifactDir)
	}
	return root, nil
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
	defer func() {
		_ = file.Close()
		_ = root.Remove(temp)
	}()
	written, err := file.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
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
