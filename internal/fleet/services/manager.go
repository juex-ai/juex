package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

func (m *Manager) Status(ctx context.Context) []Status {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ids := m.ids()
	result := make([]Status, len(ids))
	var probes sync.WaitGroup
	for index, id := range ids {
		probes.Go(func() {
			result[index] = m.status(ctx, id, m.definitions[id])
		})
	}
	probes.Wait()
	return result
}
func (m *Manager) Get(ctx context.Context, id string) (Status, error) {
	d, err := m.definition(id)
	if err != nil {
		return Status{}, err
	}
	return m.status(ctx, id, d), nil
}

func (m *Manager) ids() []string {
	ids := make([]string, 0, len(m.definitions))
	for id := range m.definitions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (m *Manager) record(id string) (serviceendpoint.Record, error) {
	var intent launchIntent
	if err := serviceendpoint.ReadJSON(serviceendpoint.IntentPath(m.home, id), &intent); err != nil {
		return serviceendpoint.Record{}, err
	}
	expected := intent.Runtime.Identity
	if expected.FleetID != m.fleet || expected.ServiceID != id || expected.InstanceID == "" {
		return serviceendpoint.Record{}, errors.New("launch identity mismatch")
	}
	var record serviceendpoint.Record
	if err := serviceendpoint.ReadJSON(serviceendpoint.CandidatePath(m.home, id, expected.InstanceID), &record); err != nil {
		return record, err
	}
	if record.Identity != expected {
		return serviceendpoint.Record{}, errors.New("candidate identity mismatch")
	}
	return record, record.Validate()
}
func (m *Manager) status(ctx context.Context, id string, d Definition) Status {
	status := Status{ID: id, Mode: d.Mode, Enabled: d.Enabled, Phase: "stopped"}
	desired, err := m.desired(id)
	status.Desired = desired.State
	if err != nil {
		status.Phase = "failed"
		status.Reason = err.Error()
		return status
	}
	if d.Mode == External {
		status.Desired = "external"
		if !d.Enabled {
			status.Phase = "disabled"
			return status
		}
		record := serviceendpoint.Record{Identity: serviceendpoint.Identity{FleetID: m.fleet, ServiceID: id}, Network: d.Network, Address: d.Address}
		identity, err := serviceendpoint.Inspect(ctx, record)
		if err != nil {
			status.Phase = "degraded"
			status.Reason = err.Error()
			return status
		}
		record.Identity = identity
		status.Phase = "ready"
		status.Runtime = &record
		return status
	}
	lock, err := serviceendpoint.StateLock(m.home, id)
	if err == nil {
		_ = lock.Close()
		if !d.Enabled {
			status.Phase = "disabled"
		} else if desired.State == "running" {
			status.Phase = "failed"
			status.Reason = "no service writer is running"
			if desired.LastError != "" {
				status.Reason = desired.LastError
			}
		}
		return status
	}
	if !errors.Is(err, homestore.ErrLockBusy) {
		status.Phase = "failed"
		status.Reason = err.Error()
		return status
	}
	record, err := m.record(id)
	if err != nil {
		status.Phase = "starting"
		status.Reason = "writer active; readiness unavailable: " + err.Error()
		return status
	}
	status.Runtime = &record
	if err := serviceendpoint.Probe(ctx, record); err != nil {
		status.Phase = "degraded"
		status.Reason = err.Error()
		return status
	}
	status.Phase = "ready"
	if !d.Enabled || desired.State == "stopped" {
		status.Phase = "degraded"
		status.Reason = "service shutdown is incomplete"
	}
	return status
}
func (m *Manager) Start(ctx context.Context, id string) (Status, error) {
	return m.start(ctx, id, true)
}
func (m *Manager) start(ctx context.Context, id string, explicit bool) (status Status, returnErr error) {
	if err := ctx.Err(); err != nil {
		return status, err
	}
	d, err := m.definition(id)
	if err != nil {
		return status, err
	}
	if d.Mode != Managed {
		return status, errors.New("external service lifecycle is not managed by Fleet")
	}
	if !d.Enabled {
		return status, errors.New("service is disabled in Home configuration")
	}
	guard, err := serviceendpoint.LifecycleLock(m.home, id)
	if err != nil {
		return status, err
	}
	defer func() { _ = guard.Close() }()
	return m.startLocked(ctx, id, d, explicit)
}

func (m *Manager) startLocked(ctx context.Context, id string, d Definition, explicit bool) (status Status, returnErr error) {
	defer func() {
		if state, err := m.desired(id); err == nil {
			state.LastError = ""
			if returnErr != nil {
				state.LastError = returnErr.Error()
			}
			returnErr = errors.Join(returnErr, serviceendpoint.WriteJSON(desiredPath(m.home, id), state))
		}
		status = m.status(ctx, id, d)
		if returnErr != nil {
			status.Reason = returnErr.Error()
		}
	}()
	desired, err := m.desired(id)
	if err != nil {
		return status, err
	}
	if explicit {
		desired.State = "running"
		desired.Attempts = 0
	}
	if desired.State != "running" {
		return status, nil
	}
	if err := serviceendpoint.WriteJSON(desiredPath(m.home, id), desired); err != nil {
		return status, err
	}
	lock, err := serviceendpoint.StateLock(m.home, id)
	if errors.Is(err, homestore.ErrLockBusy) {
		record, readErr := m.record(id)
		if readErr != nil {
			return status, fmt.Errorf("active writer has no verified endpoint: %w", homestore.ErrLockBusy)
		}
		if err := serviceendpoint.Probe(ctx, record); err != nil {
			return status, err
		}
		return status, serviceendpoint.Publish(m.home, record)
	}
	if err != nil {
		return status, err
	}
	if desired.Attempts >= 3 {
		_ = lock.Close()
		return status, errors.New("automatic service restart budget exhausted; explicitly start to retry")
	}
	desired.Attempts++
	if err := serviceendpoint.WriteJSON(desiredPath(m.home, id), desired); err != nil {
		_ = lock.Close()
		return status, err
	}
	intent := m.newIntent(id, d)
	// Holding the writer lock while replacing intent fences any delayed old child.
	err = serviceendpoint.WriteJSON(serviceendpoint.IntentPath(m.home, id), intent)
	if err == nil {
		err = m.removeRuntime(id)
	}
	_ = lock.Close()
	if err != nil {
		return status, err
	}
	if err := m.spawn(intent); err != nil {
		return status, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, m.startTimeout)
	defer cancel()
	err = poll(waitCtx, func() (bool, error) {
		record, err := m.record(id)
		if err != nil {
			return false, nil
		}
		if err := serviceendpoint.Probe(waitCtx, record); err != nil {
			return false, nil
		}
		return true, serviceendpoint.Publish(m.home, record)
	})
	return status, err
}
func (m *Manager) Stop(ctx context.Context, id string) (Status, error) {
	d, err := m.definition(id)
	if err != nil {
		return Status{}, err
	}
	if d.Mode != Managed {
		return Status{}, errors.New("external service lifecycle is not managed by Fleet")
	}
	guard, err := serviceendpoint.LifecycleLock(m.home, id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = guard.Close() }()
	desired, err := m.desired(id)
	if err != nil {
		return Status{}, err
	}
	desired.State = "stopped"
	if err := serviceendpoint.WriteJSON(desiredPath(m.home, id), desired); err != nil {
		return Status{}, err
	}
	err = m.stopLocked(ctx, id)
	status := m.status(ctx, id, d)
	if err != nil {
		status.Reason = err.Error()
	}
	return status, err
}
func (m *Manager) stopLocked(ctx context.Context, id string) error {
	waitCtx, cancel := context.WithTimeout(ctx, m.stopTimeout)
	defer cancel()
	requested := false
	return poll(waitCtx, func() (bool, error) {
		lock, err := serviceendpoint.StateLock(m.home, id)
		if err == nil {
			defer func() { _ = lock.Close() }()
			// Removing intent under writer lock prevents a launched but delayed child starting.
			var intent launchIntent
			if err := serviceendpoint.ReadJSON(serviceendpoint.IntentPath(m.home, id), &intent); err == nil {
				if intent.Runtime.FleetID != m.fleet || intent.Runtime.ServiceID != id {
					return false, errors.New("cannot remove mismatched service intent")
				}
				_ = os.Remove(serviceendpoint.CandidatePath(m.home, id, intent.Runtime.InstanceID))
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			if err := os.Remove(serviceendpoint.IntentPath(m.home, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			return true, m.removeRuntime(id)
		}
		if !errors.Is(err, homestore.ErrLockBusy) {
			return false, err
		}
		if !requested {
			record, err := m.record(id)
			if err != nil {
				return false, nil
			}
			if err := serviceendpoint.Stop(waitCtx, record); err != nil {
				return false, err
			}
			requested = true
		}
		return false, nil
	})
}
func (m *Manager) Restart(ctx context.Context, id string) (Status, error) {
	d, err := m.definition(id)
	if err != nil {
		return Status{}, err
	}
	if d.Mode != Managed || !d.Enabled {
		return Status{}, errors.New("restart requires an enabled managed service")
	}
	guard, err := serviceendpoint.LifecycleLock(m.home, id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = guard.Close() }()
	if err := serviceendpoint.WriteJSON(desiredPath(m.home, id), desiredState{State: "stopped"}); err != nil {
		return Status{}, err
	}
	if err := m.stopLocked(ctx, id); err != nil {
		return Status{}, err
	}
	return m.startLocked(ctx, id, d, true)
}
func (m *Manager) removeRuntime(id string) error {
	path := serviceendpoint.RuntimePath(m.home, id)
	var record serviceendpoint.Record
	if err := serviceendpoint.ReadJSON(path, &record); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if record.FleetID != m.fleet || record.ServiceID != id {
		return errors.New("cannot remove mismatched service runtime")
	}
	return serviceendpoint.Remove(m.home, record)
}

// Reconcile does not turn an explicit stop back into a start. Each failed launch
// consumes one of three persisted attempts, including across manager restarts.
func (m *Manager) Reconcile(ctx context.Context) []Status {
	result := make([]Status, 0, len(m.definitions))
	for _, id := range m.ids() {
		if ctx.Err() != nil {
			break
		}
		d := m.definitions[id]
		status := m.status(ctx, id, d)
		if d.Mode == External {
			guard, err := serviceendpoint.LifecycleLock(m.home, id)
			if err != nil {
				status.Reason = err.Error()
				result = append(result, status)
				continue
			}
			if d.Enabled && status.Runtime != nil {
				if err := serviceendpoint.Publish(m.home, *status.Runtime); err != nil {
					status.Phase = "failed"
					status.Reason = err.Error()
				}
			} else if !d.Enabled {
				if err := m.removeRuntime(id); err != nil {
					status.Phase = "failed"
					status.Reason = err.Error()
				}
			}
			_ = guard.Close()
		} else if !d.Enabled || status.Desired == "stopped" {
			guard, err := serviceendpoint.LifecycleLock(m.home, id)
			if err == nil {
				err = m.stopLocked(ctx, id)
				_ = guard.Close()
			}
			status = m.status(ctx, id, d)
			if err != nil {
				status.Phase = "degraded"
				status.Reason = err.Error()
			}
		} else {
			var err error
			status, err = m.start(ctx, id, false)
			if err != nil {
				status.Reason = err.Error()
			}
		}
		result = append(result, status)
	}
	return result
}
func (m *Manager) Serve(ctx context.Context) {
	for {
		m.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func (m *Manager) Logs(id string, lines int) ([]byte, error) {
	d, err := m.definition(id)
	if err != nil {
		return nil, err
	}
	if d.Mode != Managed {
		return nil, errors.New("external service logs are managed externally")
	}
	if lines < 1 || lines > 10000 {
		return nil, errors.New("lines must be between 1 and 10000")
	}
	file, err := os.Open(filepath.Join(serviceendpoint.StateDir(m.home, id), "service.log"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := max(int64(0), info.Size()-(1<<20))
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return []byte(strings.Join(parts, "\n") + "\n"), nil
}
func poll(ctx context.Context, fn func() (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := fn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
