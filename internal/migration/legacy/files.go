package legacy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
)

// The reader rejects oversized files explicitly instead of partially importing
// them. Legacy commits are bounded at 16 MiB; a generation can contain many.
const maxSourceFileBytes = 256 << 20

type sourceReader struct {
	root   *os.Root
	files  []SourceFile
	stats  map[string]os.FileInfo
	absent []string
	byPath map[string][]byte
}

func (r *sourceReader) optionalRead(path string) ([]byte, error) {
	if _, err := r.root.Lstat(path); errors.Is(err, os.ErrNotExist) {
		r.stats[path] = nil
		r.absent = append(r.absent, path)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return r.read(path)
}

func (r *sourceReader) read(path string) ([]byte, error) {
	before, err := r.root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxSourceFileBytes {
		return nil, fmt.Errorf("%s: expected a regular source file of at most %d bytes", path, maxSourceFileBytes)
	}
	if original, ok := r.stats[path]; ok {
		if !sameFile(original, before) {
			return nil, fmt.Errorf("%s: source changed during capture", path)
		}
		return r.byPath[path], nil
	}
	f, err := r.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameFile(before, opened) {
		return nil, fmt.Errorf("%s: source changed while opening", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSourceFileBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !sameFile(before, after) || int64(len(data)) != before.Size() {
		return nil, fmt.Errorf("%s: source changed while reading", path)
	}
	sum := sha256.Sum256(data)
	r.files = append(r.files, SourceFile{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Mode: uint32(before.Mode().Perm()), ModifiedAt: before.ModTime().UTC(), Data: data})
	r.stats[path] = before
	if r.byPath == nil {
		r.byPath = make(map[string][]byte)
	}
	r.byPath[path] = data
	return data, nil
}

func (r *sourceReader) list(name string, optional bool) ([]os.DirEntry, error) {
	before, err := r.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && optional {
		if _, ok := r.stats[name]; ok {
			return nil, fmt.Errorf("%s: directory disappeared during capture", name)
		}
		r.stats[name] = nil
		r.absent = append(r.absent, name)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: expected a regular directory", name)
	}
	if original, ok := r.stats[name]; ok && !sameFile(original, before) {
		return nil, fmt.Errorf("%s: directory changed during capture", name)
	}
	dir, err := r.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !sameFile(before, opened) {
		return nil, fmt.Errorf("%s: directory changed while opening", name)
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	r.stats[name] = before
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (r *sourceReader) adopt(prefix string, child *sourceReader) error {
	if r.byPath == nil {
		r.byPath = make(map[string][]byte)
	}
	for name, info := range child.stats {
		name = path.Join(prefix, name)
		if before, ok := r.stats[name]; ok && !sameFile(before, info) {
			return fmt.Errorf("%s: source changed during capture", name)
		}
		r.stats[name] = info
	}
	for _, file := range child.files {
		file.Path = path.Join(prefix, file.Path)
		r.files = append(r.files, file)
		r.byPath[file.Path] = file.Data
	}
	for _, name := range child.absent {
		r.absent = append(r.absent, path.Join(prefix, name))
	}
	return nil
}

func (r *sourceReader) unchanged() error {
	for path, before := range r.stats {
		after, err := r.root.Lstat(path)
		if before == nil && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !sameFile(before, after) {
			return fmt.Errorf("%s: source changed during capture", path)
		}
	}
	return nil
}

func sameFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}
