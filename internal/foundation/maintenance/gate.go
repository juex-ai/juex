// Package maintenance provides the single-host deployment admission barrier.
// Its files are operator infrastructure, never Agent identity or business state.
package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var ErrDraining = errors.New("platform is draining for maintenance")

type Admission func() (func(), error)

func Enter(admit Admission) (func(), error) {
	if admit == nil {
		return func() {}, nil
	}
	return admit()
}

type Gate struct{ directory string }

// Open never creates the lock: replacing that inode would split the barrier.
// Empty configuration is useful for isolated development and embedded tests.
func Open(directory string) (Gate, error) {
	if directory == "" {
		return Gate{}, nil
	}
	if !filepath.IsAbs(directory) {
		return Gate{}, errors.New("maintenance directory must be absolute")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return Gate{}, err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return Gate{}, errors.New("maintenance directory must be operator-owned and not writable by others")
	}
	g := Gate{directory: directory}
	f, err := g.lock()
	if err != nil {
		return Gate{}, err
	}
	return g, f.Close()
}

func (g Gate) lock() (*os.File, error) {
	path := filepath.Join(g.directory, "admission.lock")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("invalid maintenance lock")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("maintenance lock changed")
	}
	return f, nil
}

func (g Gate) Enter() (func(), error) { return g.enter(false) }

// Exclusive is for offline repair commands after the operator stopped services.
// The persistent drain marker is required; omission must never mean permission.
func (g Gate) Exclusive() (func(), error) {
	if g.directory == "" {
		return nil, errors.New("offline maintenance directory is required")
	}
	if _, err := os.Lstat(filepath.Join(g.directory, "draining")); err != nil {
		return nil, err
	}
	f, err := g.lock()
	if err != nil {
		return nil, err
	}
	if err = exclusiveLock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// Control keeps login and explicit cancellation available while draining.
// The final exclusive barrier still prevents these writes during shutdown.
func (g Gate) Control() (func(), error) { return g.enter(true) }

func (g Gate) enter(control bool) (func(), error) {
	if g.directory == "" {
		return func() {}, nil
	}
	f, err := g.lock()
	if err != nil {
		return nil, err
	}
	if err = sharedLock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	if !control {
		_, err = os.Lstat(filepath.Join(g.directory, "draining"))
		if err == nil {
			_ = f.Close()
			return nil, ErrDraining
		}
		if !os.IsNotExist(err) {
			_ = f.Close()
			return nil, err
		}
	}
	var once sync.Once
	return func() { once.Do(func() { _ = f.Close() }) }, nil
}
