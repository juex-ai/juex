//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestExecutionPresenceRenewalPreservesFreshRowsAndConnectionFences(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	journal := rand.Text()
	connected, err := f.executionStore.Connect(ctx, device.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	version := func() [2]string {
		t.Helper()
		var value [2]string
		if err := f.pool.QueryRow(ctx, `SELECT xmin::text,ctid::text FROM execution.environments WHERE id=$1`, device.ID).Scan(&value[0], &value[1]); err != nil {
			t.Fatal(err)
		}
		return value
	}
	t.Run("frequent polling preserves a fresh durable heartbeat", func(t *testing.T) {
		before := version()
		for range 4 {
			if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, true); err != nil {
				t.Fatal(err)
			}
		}
		if after := version(); after != before {
			t.Fatal("fresh heartbeat rewrote its durable row", before, after)
		}
	})
	t.Run("expired presence can be renewed", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE execution.environments SET online_until=clock_timestamp()-interval '1 second' WHERE id=$1`, device.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, true); err != nil {
			t.Fatal(err)
		}
		current, err := f.executionStore.Device(ctx, device.ID)
		if err != nil || !current.Online {
			t.Fatal("expired presence was not renewed", current, err)
		}
	})
	t.Run("renew before expiry and clear presence immediately on disconnect", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE execution.environments SET online_until=clock_timestamp()+interval '10 seconds',last_seen=clock_timestamp()-interval '20 seconds' WHERE id=$1`, device.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, true); err != nil {
			t.Fatal(err)
		}
		var renewed bool
		if err := f.pool.QueryRow(ctx, `SELECT online_until>clock_timestamp()+interval '20 seconds' AND last_seen>clock_timestamp()-interval '5 seconds' FROM execution.environments WHERE id=$1`, device.ID).Scan(&renewed); err != nil || !renewed {
			t.Fatal("due heartbeat was not renewed", renewed, err)
		}
		if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, false); err != nil {
			t.Fatal(err)
		}
		current, err := f.executionStore.Device(ctx, device.ID)
		if err != nil || current.Online {
			t.Fatal("disconnect retained online presence", current, err)
		}
		if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, true); err != nil {
			t.Fatal(err)
		}
		current, err = f.executionStore.Device(ctx, device.ID)
		if err != nil || !current.Online {
			t.Fatal("offline presence was not renewed", current, err)
		}
	})
	t.Run("a renewal waiting for a replacement rejects its old epoch", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE execution.environments SET online_until=clock_timestamp()-interval '1 second' WHERE id=$1`, device.ID); err != nil {
			t.Fatal(err)
		}
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var writer int32
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writer); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE execution.environments SET connection_epoch=connection_epoch+1,online_until=clock_timestamp()+interval '30 seconds' WHERE id=$1`, device.ID); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- f.executionStore.Touch(bounded, device.ID, connected.ConnectionEpoch, true) }()
		runtimeEventually(t, func() bool {
			var waiting bool
			err := f.pool.QueryRow(bounded, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, writer).Scan(&waiting)
			return err == nil && waiting
		})
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		before := version()
		if err := <-result; !errors.Is(err, execprotocol.ErrConflict) {
			t.Fatal("blocked renewal validated the replaced epoch", err)
		}
		if after := version(); after != before {
			t.Fatal("blocked renewal changed the replacement", before, after)
		}
		connected, err = f.executionStore.Device(ctx, device.ID)
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing connection remains a conflict", func(t *testing.T) {
		if err := f.executionStore.Touch(ctx, uuid.NewString(), connected.ConnectionEpoch, true); !errors.Is(err, execprotocol.ErrConflict) {
			t.Fatal("missing connection accepted a heartbeat", err)
		}
	})
	t.Run("replaced connections cannot refresh or clear current presence", func(t *testing.T) {
		current, err := f.executionStore.Connect(ctx, device.ID, journal)
		if err != nil {
			t.Fatal(err)
		}
		before := version()
		for _, online := range []bool{true, false} {
			if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, online); !errors.Is(err, execprotocol.ErrConflict) {
				t.Fatal("stale heartbeat bypassed the connection fence", online, err)
			}
		}
		if after := version(); after != before {
			t.Fatal("stale heartbeat changed current presence", before, after)
		}
		connected = current
	})
	t.Run("fresh heartbeat does not acquire a row lock", func(t *testing.T) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `UPDATE execution.environments SET last_seen=clock_timestamp() WHERE id=$1`, device.ID); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := f.executionStore.Touch(bounded, device.ID, connected.ConnectionEpoch, true); err != nil {
			t.Fatal("fresh heartbeat waited behind a writer", err)
		}
	})
}
