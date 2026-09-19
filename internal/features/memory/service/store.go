// Package service owns Fleet Memory knowledge, durable work and commit recovery.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

type work struct {
	Receipt      mc.Receipt  `json:"receipt"`
	Proposal     mc.Proposal `json:"proposal"`
	Caller       mc.Caller   `json:"caller"`
	Fingerprint  string      `json:"fingerprint"`
	DecisionHash string      `json:"decision_hash,omitempty"`
	Executor     string      `json:"executor,omitempty"`
	Token        string      `json:"token,omitempty"`
	Expires      time.Time   `json:"expires,omitempty"`
	RetryAt      time.Time   `json:"retry_at,omitempty"`
	Fence        uint64      `json:"fence"`
	Automatic    bool        `json:"automatic"`
	SourceKey    string      `json:"source_key,omitempty"`
	Through      uint64      `json:"through,omitempty"`
}

type sourceState struct {
	mc.SourceState
	Caller           mc.Caller     `json:"caller"`
	Evidence         []mc.Evidence `json:"evidence"`
	EndedGenerations []string      `json:"ended_generations"`
	IdleSince        time.Time     `json:"idle_since"`
	FirstPending     time.Time     `json:"first_pending"`
	Pending          int           `json:"pending"`
	Job              string        `json:"job,omitempty"`
	LiveUntil        time.Time     `json:"live_until"`
}

type state struct {
	Fleet      string                  `json:"fleet"`
	Strategy   string                  `json:"strategy"`
	Fence      uint64                  `json:"fence"`
	Clock      uint64                  `json:"clock"`
	Access     map[string]uint64       `json:"access"`
	Uses       map[string]uint64       `json:"uses"`
	Requests   map[string]*work        `json:"requests"`
	Keys       map[string]string       `json:"keys"`
	Sources    map[string]*sourceState `json:"sources"`
	Suppressed []mc.Source             `json:"suppressed"`
	Deleted    map[string]bool         `json:"deleted"`
}

type intent struct {
	Entries []mc.Entry `json:"entries"`
	Deletes []string   `json:"deletes"`
	State   state      `json:"state"`
}

// Store requires the service process's exclusive writer lease. Its mutex also
// excludes readers until an interrupted intent has been fully reconciled.
type Store struct {
	mu          sync.Mutex
	dir, fleet  string
	state       state
	entries     map[string]mc.Entry
	indexReady  bool
	reloadState bool
	now         func() time.Time
	beforeWrite func(string) error
}

func Open(dir, fleet, strategy string) (*Store, error) {
	if fleet == "" || (strategy != mc.Basic && strategy != mc.Advanced) {
		return nil, errors.New("memory requires Fleet identity and basic/advanced strategy")
	}
	s := &Store{dir: dir, fleet: fleet, now: func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }}
	for _, p := range []string{dir, filepath.Join(dir, "state"), filepath.Join(dir, "memory")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
		if err := regularPath(p, true); err != nil {
			return nil, err
		}
	}
	s.state = state{Fleet: fleet, Strategy: strategy, Access: map[string]uint64{}, Uses: map[string]uint64{}, Requests: map[string]*work{}, Keys: map[string]string{}, Sources: map[string]*sourceState{}, Deleted: map[string]bool{}}
	if err := s.readJSON(s.statePath(), &s.state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.state.Fleet != fleet {
		return nil, errors.New("memory store belongs to another Fleet")
	}
	if err := s.recover(); err != nil {
		return nil, err
	}
	if err := s.loadEntries(); err != nil {
		return nil, err
	}
	if s.state.Strategy != strategy {
		s.state.Strategy = strategy
		s.state.Fence++
		for _, w := range s.state.Requests {
			if w.Automatic && !terminal(w.Receipt.State) {
				s.settle(w, "rejected", "strategy changed")
				s.releaseSource(w)
			}
		}
	}
	if err := s.persist(); err != nil {
		return nil, err
	}
	_ = s.rebuild()
	return s, nil
}

func (s *Store) statePath() string  { return filepath.Join(s.dir, "state", "state.json") }
func (s *Store) intentPath() string { return filepath.Join(s.dir, "state", "intent.json") }
func regularPath(path string, dir bool) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if i.Mode()&os.ModeSymlink != 0 || dir != i.IsDir() || (!dir && !i.Mode().IsRegular()) {
		return fmt.Errorf("memory unsafe path: %s", path)
	}
	return nil
}
func (s *Store) write(path string, data []byte) error {
	if err := regularPath(filepath.Dir(path), true); err != nil {
		return err
	}
	if err := regularPath(path, false); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.beforeWrite != nil {
		if err := s.beforeWrite(path); err != nil {
			return err
		}
	}
	return homestore.WriteFileAtomic(path, data, 0o600, 0o700)
}
func (s *Store) writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return s.write(path, b)
}
func (s *Store) readJSON(path string, v any) error {
	if err := regularPath(path, false); err != nil {
		return err
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Store) persist() error {
	err := s.writeJSON(s.statePath(), s.state)
	// Atomic publication can succeed before directory durability reports an
	// error. Reconcile that uncertainty before accepting the next operation.
	s.reloadState = err != nil
	return err
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }

func (s *Store) loadEntries() error {
	files, err := os.ReadDir(filepath.Join(s.dir, "memory"))
	if err != nil {
		return err
	}
	entries := map[string]mc.Entry{}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		p := filepath.Join(s.dir, "memory", f.Name())
		if err := regularPath(p, false); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		parts := strings.SplitN(string(b), "\n---\n", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "---\n") {
			return fmt.Errorf("invalid memory entry %s", f.Name())
		}
		var e mc.Entry
		if err := json.Unmarshal([]byte(strings.TrimPrefix(parts[0], "---\n")), &e); err != nil {
			return err
		}
		e.Body = parts[1]
		if err := mc.ValidateEntryID(e.ID); err != nil {
			return err
		}
		if f.Name() != e.ID+".md" || e.Revision == 0 {
			return errors.New("memory entry identity/revision mismatch")
		}
		entries[e.ID] = e
	}
	s.entries = entries
	return nil
}

func (s *Store) publish(in intent) error {
	if len(in.Deletes) > 0 {
		if err := os.Remove(filepath.Join(s.dir, "MEMORY.md")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.indexReady = false
		if err := homestore.SyncDir(s.dir); err != nil {
			return err
		}
	}
	for _, e := range in.Entries {
		body := e.Body
		e.Body = ""
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if err := s.write(filepath.Join(s.dir, "memory", e.ID+".md"), []byte("---\n"+string(b)+"\n---\n"+body)); err != nil {
			return err
		}
	}
	for _, id := range in.Deletes {
		p := filepath.Join(s.dir, "memory", id+".md")
		if err := regularPath(p, false); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(in.Deletes) > 0 {
		if err := homestore.SyncDir(filepath.Join(s.dir, "memory")); err != nil {
			return err
		}
	}
	if err := s.writeJSON(s.statePath(), in.State); err != nil {
		return err
	}
	if err := os.Remove(s.intentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := homestore.SyncDir(filepath.Dir(s.intentPath())); err != nil {
		s.reloadState = true
		return err
	}
	s.state = in.State
	return s.loadEntries()
}

func (s *Store) recover() error {
	var in intent
	if err := s.readJSON(s.intentPath(), &in); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if in.State.Fleet != s.fleet {
		return errors.New("memory intent Fleet mismatch")
	}
	for _, e := range in.Entries {
		if err := mc.ValidateEntryID(e.ID); err != nil {
			return err
		}
	}
	for _, id := range in.Deletes {
		if err := mc.ValidateEntryID(id); err != nil {
			return err
		}
	}
	if err := s.publish(in); err != nil {
		return fmt.Errorf("memory commit recovery required: %w", err)
	}
	_ = s.rebuild()
	return nil
}

func (s *Store) commit(before state, entries []mc.Entry, deletes []string) error {
	in := intent{Entries: entries, Deletes: deletes, State: clone(s.state)}
	// Memory state changes become visible only with their durable intent.
	s.state = before
	if err := s.writeJSON(s.intentPath(), in); err != nil {
		return err
	}
	return s.publish(in)
}

func (s *Store) begin(ctx context.Context, c mc.Caller) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.recover(); err != nil {
		return err
	}
	if s.reloadState {
		if err := s.readJSON(s.statePath(), &s.state); err != nil {
			return err
		}
		if err := s.loadEntries(); err != nil {
			return err
		}
		s.reloadState = false
	}
	if c.FleetID != s.fleet || c.AgentID == "" {
		return errors.New("memory caller Fleet/Agent mismatch")
	}
	if c.Profile != mc.ProfileAgent && c.Profile != mc.ProfileSupervisor && c.Profile != mc.ProfileUser {
		return errors.New("memory invalid profile")
	}
	return nil
}
func (s *Store) rebuild() error {
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.state.Access[ids[i]], s.state.Access[ids[j]]
		if a == b {
			return ids[i] < ids[j]
		}
		return a > b
	})
	if len(ids) > mc.MaxEntries {
		ids = ids[:mc.MaxEntries]
	}
	var b strings.Builder
	b.WriteString("# Memory\n\nGenerated hot index; search includes cold entries.\n\n")
	for _, id := range ids {
		e := s.entries[id]
		fmt.Fprintf(&b, "- [%s](memory/%s.md): %s\n", e.Name, e.ID, e.Summary)
	}
	err := s.write(filepath.Join(s.dir, "MEMORY.md"), []byte(b.String()))
	s.indexReady = err == nil
	return err
}

func visible(scope, caller mc.Scope) bool {
	return (scope.Workspace == "" || scope.Workspace == caller.Workspace) && (scope.Project == "" || scope.Project == caller.Project)
}
func (s *Store) effectiveScope(c mc.Caller) mc.Scope {
	if w := s.state.Requests[c.AssignmentID]; w != nil && c.Profile == mc.ProfileSupervisor && w.Token == c.Token && w.Executor == c.AgentID {
		return w.Caller.Scope
	}
	return c.Scope
}
func (s *Store) Status(ctx context.Context, c mc.Caller) (mc.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Status{}, err
	}
	r := mc.Status{Strategy: s.state.Strategy, Fence: s.state.Fence, Entries: len(s.entries), IndexReady: s.indexReady}
	for _, w := range s.state.Requests {
		if w.Receipt.State == "pending" {
			r.Pending++
		}
		if w.Receipt.State == "running" {
			r.Running++
		}
	}
	return r, nil
}

func (s *Store) search(c mc.Caller, q mc.Query, body bool) (mc.Page, error) {
	if q.Offset < 0 || q.Limit < 0 || q.Limit > 50 || len(q.Text) > 4096 {
		return mc.Page{}, errors.New("memory invalid query budget")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	terms := strings.Fields(strings.ToLower(q.Text))
	scope := s.effectiveScope(c)
	type scored struct {
		e     mc.Entry
		score int
	}
	var matches []scored
	for _, e := range s.entries {
		if c.Profile != mc.ProfileUser && !visible(e.Scope, scope) {
			continue
		}
		text := strings.ToLower(e.Name + " " + e.Summary + " " + e.Body)
		score := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
			}
		}
		if len(terms) > 0 && score == 0 {
			continue
		}
		if q.Subject != "" || q.Predicate != "" || q.At != nil {
			found := false
			for _, f := range e.Facts {
				at := s.now()
				if q.At != nil {
					at = *q.At
				}
				if (q.Subject == "" || f.Subject == q.Subject) && (q.Predicate == "" || f.Predicate == q.Predicate) && f.Status != "disputed" && (q.At != nil || f.Status == "valid") && (f.ValidFrom == nil || !at.Before(*f.ValidFrom)) && (f.ValidUntil == nil || at.Before(*f.ValidUntil)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		matches = append(matches, scored{e, score})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].e.ID < matches[j].e.ID
	})
	r := mc.Page{Entries: []mc.Entry{}, Next: -1, Fence: s.state.Fence}
	for i := q.Offset; i < len(matches) && len(r.Entries) < q.Limit; i++ {
		e := clone(matches[i].e)
		if !body {
			e.Body = ""
			e.Sources = nil
			e.Entities = nil
			e.Facts = nil
		}
		r.Entries = append(r.Entries, e)
	}
	if q.Offset+len(r.Entries) < len(matches) {
		r.Next = q.Offset + len(r.Entries)
	}
	return r, nil
}
func (s *Store) Search(ctx context.Context, c mc.Caller, q mc.Query) (mc.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Page{}, err
	}
	return s.search(c, q, false)
}
func (s *Store) Read(ctx context.Context, c mc.Caller, q mc.ReadRequest) (mc.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Entry{}, err
	}
	if err := mc.ValidateEntryID(q.ID); err != nil {
		return mc.Entry{}, err
	}
	e, ok := s.entries[q.ID]
	if !ok || (c.Profile != mc.ProfileUser && !visible(e.Scope, s.effectiveScope(c))) {
		return mc.Entry{}, errors.New("memory entry unavailable in caller scope")
	}
	if c.Purpose != "maintenance" {
		before := clone(s.state)
		s.state.Clock++
		s.state.Access[e.ID] = s.state.Clock
		if err := s.persist(); err != nil {
			s.state = before
			return mc.Entry{}, err
		}
		_ = s.rebuild()
	}
	return clone(e), nil
}
func (s *Store) Recall(ctx context.Context, c mc.Caller, q mc.Query) (mc.Recall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Recall{}, err
	}
	r := mc.Recall{Strategy: s.state.Strategy, Fence: s.state.Fence, Entries: []mc.Entry{}}
	if s.state.Strategy != mc.Advanced {
		return r, nil
	}
	q.Limit = 8
	q.Offset = 0
	p, err := s.search(c, q, true)
	if err != nil {
		return r, err
	}
	remaining := 4094
	before := clone(s.state)
	for _, e := range p.Entries {
		b, _ := json.Marshal(e)
		if len(b)+1 > remaining {
			continue
		}
		remaining -= len(b) + 1
		r.Entries = append(r.Entries, e)
		s.state.Uses[e.ID]++
	}
	if err := s.persist(); err != nil {
		s.state = before
		return mc.Recall{}, err
	}
	return r, nil
}
