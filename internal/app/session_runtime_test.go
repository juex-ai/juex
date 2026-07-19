package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

func TestSwitchToNewPrimarySessionWaitsForLifecycleReaders(t *testing.T) {
	a, _ := newStubApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- a.ReadSession(func(sess *session.Session) error {
			close(entered)
			<-release
			return sess.Append(llm.TextMessage(llm.RoleUser, "reader completed on old session"))
		})
	}()
	<-entered

	switchDone := make(chan error, 1)
	go func() {
		switchDone <- a.SwitchToNewPrimarySession()
	}()
	select {
	case err := <-switchDone:
		t.Fatalf("session switch completed before lifecycle reader: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-switchDone; err != nil {
		t.Fatal(err)
	}
}

func TestSwitchToNewPrimarySessionBusyRestoresHistory(t *testing.T) {
	a, _ := newStubApp(t)
	oldIdentity, ok := a.SessionIdentity()
	if !ok {
		t.Fatal("missing initial session")
	}
	if err := a.Engine.ReserveTurnID("turn-busy"); err != nil {
		t.Fatal(err)
	}

	err := a.SwitchToNewPrimarySession()
	if !errors.Is(err, runtime.ErrSessionRuntimeBusy) {
		t.Fatalf("SwitchToNewPrimarySession() error = %v, want ErrSessionRuntimeBusy", err)
	}
	identity, ok := a.SessionIdentity()
	if !ok || identity.ID != oldIdentity.ID {
		t.Fatalf("active app session = %+v, want %q", identity, oldIdentity.ID)
	}
	history, err := session.LoadHistory(a.cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != oldIdentity.ID {
		t.Fatalf("history active = %+v, want %q", history.Active, oldIdentity.ID)
	}
	if len(history.Sessions) != 1 || history.Sessions[0].ID != oldIdentity.ID {
		t.Fatalf("history sessions = %+v, want only original session", history.Sessions)
	}
}

func TestSwitchToNewPrimarySessionIsAtomicForConcurrentReaders(t *testing.T) {
	a, _ := newStubApp(t)

	const (
		switches = 48
		readers  = 6
	)
	start := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, readers+1)
	var wg sync.WaitGroup

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				select {
				case <-done:
					return
				default:
				}
				if err := assertAtomicAppSessionRead(a); err != nil {
					errs <- err
					return
				}
				status := a.StatusSnapshot(time.Now().UTC())
				if status.SessionID == "" || filepath.Base(status.SessionDir) != status.SessionID {
					errs <- fmt.Errorf("mixed status snapshot: id=%q dir=%q", status.SessionID, status.SessionDir)
					return
				}
				_ = a.ActiveContext()
				_, _ = a.SessionStateStatus()
				_ = a.Engine.PromptSections()
				_ = a.PendingInputStatus()
			}
		}()
	}

	close(start)
	for i := 0; i < switches; i++ {
		if err := a.SwitchToNewPrimarySession(); err != nil {
			errs <- fmt.Errorf("switch %d: %w", i, err)
			break
		}
	}
	close(done)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func assertAtomicAppSessionRead(a *App) error {
	return a.ReadSession(func(sess *session.Session) error {
		runtime := a.Engine.SessionRuntimeSnapshot()
		if runtime.Session != sess {
			return fmt.Errorf("app session %q and engine session %q differ", sess.ID, sessionRuntimeID(runtime.Session))
		}
		if runtime.ScratchpadDir != sess.ScratchpadDir() {
			return fmt.Errorf("session %q scratchpad = %q, want %q", sess.ID, runtime.ScratchpadDir, sess.ScratchpadDir())
		}
		if runtime.Notes == nil {
			return fmt.Errorf("session %q has no notes store", sess.ID)
		}
		if runtime.Notes.SessionDir != sess.Dir {
			return fmt.Errorf("session %q notes belong to %q", sess.ID, runtime.Notes.SessionDir)
		}
		if runtime.GoalState == nil {
			return fmt.Errorf("session %q has no goal state store", sess.ID)
		}
		if runtime.GoalState.SessionDir != sess.Dir {
			return fmt.Errorf("session %q goal state belongs to %q", sess.ID, runtime.GoalState.SessionDir)
		}
		if runtime.HookContext.SessionID != sess.ID {
			return fmt.Errorf("session %q hook session = %q", sess.ID, runtime.HookContext.SessionID)
		}
		return nil
	})
}

func sessionRuntimeID(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID
}
