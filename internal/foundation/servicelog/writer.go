// Package servicelog bounds ordinary process logs independently from durable
// business receipts and audit records.
package servicelog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Writer struct {
	mu        sync.Mutex
	directory string
	maxBytes  int64
	maxFiles  int
	age       time.Duration
	now       func() time.Time
	file      *os.File
	size      int64
	created   time.Time
}

func New(directory string) (*Writer, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	w := &Writer{directory: directory, maxBytes: 10 << 20, maxFiles: 7, age: 7 * 24 * time.Hour, now: time.Now}
	return w, w.Prune()
}
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for len(p) > 0 {
		now := w.now()
		if w.file == nil || w.size >= w.maxBytes || now.Sub(w.created) >= 24*time.Hour {
			if w.file != nil {
				if err := w.file.Close(); err != nil {
					return written, err
				}
				w.file = nil
			}
			path := filepath.Join(w.directory, fmt.Sprintf("juex-%s.log", now.UTC().Format("20060102T150405.000000000")))
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return written, err
			}
			w.file, w.size, w.created = f, 0, now
			if err := w.prune(); err != nil {
				return written, err
			}
		}
		count := min(int64(len(p)), w.maxBytes-w.size)
		n, err := w.file.Write(p[:count])
		written += n
		w.size += int64(n)
		p = p[n:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
func (w *Writer) Prune() error { w.mu.Lock(); defer w.mu.Unlock(); return w.prune() }
func (w *Writer) prune() error {
	paths, err := filepath.Glob(filepath.Join(w.directory, "juex-*.log"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for i, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		created, err := time.Parse("juex-20060102T150405.000000000.log", filepath.Base(path))
		if err != nil {
			continue
		}
		expired := !created.After(w.now().Add(-w.age))
		if i < len(paths)-w.maxFiles || expired {
			if w.file != nil && w.file.Name() == path {
				if err := w.file.Close(); err != nil {
					return err
				}
				w.file = nil
				w.size = 0
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
