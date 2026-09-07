package state

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/homestore"
)

type activeComposition struct {
	owners     []Owner
	references int
	lock       *homestore.Lock
}

var compositions = struct {
	sync.Mutex
	active map[string]*activeComposition
}{active: map[string]*activeComposition{}}

// Lease prevents configuration retirement while any App or deferred Worker
// writer still uses the composition. The process-level advisory lock also
// excludes another Agent process, including before its endpoint is published.
type Lease struct {
	dir  string
	once sync.Once
	err  error
}

func Acquire(agentDir string, enabled []Owner, directories func() ([]string, error)) (*Lease, error) {
	dir, err := filepath.Abs(agentDir)
	if err != nil {
		return nil, err
	}
	owners := slices.Clone(enabled)
	for _, owner := range owners {
		if err := validateOwner(owner); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(owners, func(a, b Owner) int {
		if a.Module < b.Module {
			return -1
		}
		if a.Module > b.Module {
			return 1
		}
		return 0
	})
	owners = slices.Compact(owners)
	compositions.Lock()
	defer compositions.Unlock()
	if current := compositions.active[dir]; current != nil {
		if !slices.Equal(current.owners, owners) {
			return nil, errors.New("module state: stop the previous Agent composition before applying module changes")
		}
		current.references++
		return &Lease{dir: dir}, nil
	}
	lock, err := homestore.AcquireLock(filepath.Join(dir, ".module-resources.lock"), homestore.LockTry)
	if err != nil {
		return nil, fmt.Errorf("module state: acquire Agent lifecycle: %w", err)
	}
	dirs, err := directories()
	if err == nil {
		err = Reconcile(dir, dirs, owners)
	}
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	compositions.active[dir] = &activeComposition{owners: owners, references: 1, lock: lock}
	return &Lease{dir: dir}, nil
}
func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		compositions.Lock()
		defer compositions.Unlock()
		current := compositions.active[l.dir]
		current.references--
		if current.references == 0 {
			l.err = current.lock.Close()
			delete(compositions.active, l.dir)
		}
	})
	return l.err
}
