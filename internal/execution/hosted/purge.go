package hosted

import "context"

// Purge runs under Execution's durable environment lifecycle lock. Admission is
// fenced, and container/network removal precedes all volume removal.
func (d *Docker) Purge(ctx context.Context, spec Spec) error {
	if err := d.RemoveContainer(ctx, spec); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.policy(spec); err != nil {
		return err
	}
	return purgeStorage(ctx, d.config, spec)
}
