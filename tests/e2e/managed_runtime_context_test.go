//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedRuntimeContextReferencesAreThreadScoped(t *testing.T) {
	_, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	original := strings.Repeat("原文内容", 2000) + "MIDDLE-PROOF" + strings.Repeat("后文内容", 2000)
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "large", Text: original})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "context-reader", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig()); err != nil {
		t.Fatal(err)
	}
	ref := managedruntime.ContextReference(input.ID, 0, "text")
	var recovered strings.Builder
	for offset := 0; offset < len(original); {
		page, err := store.ReadContext(ctx, scope, main.ID, ref, offset, 257)
		if err != nil || !utf8.ValidString(page.Text) || page.Offset != offset || page.NextOffset <= offset || page.TotalBytes != len(original) {
			t.Fatal(page, err)
		}
		recovered.WriteString(page.Text)
		offset = page.NextOffset
	}
	if recovered.String() != original {
		t.Fatal("pagination lost original bytes")
	}
	worker, err := store.CreateWorker(ctx, scope, main.ID, "worker", "Worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadContext(ctx, scope, worker.ID, ref, 0, 100); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-thread reference", err)
	}
	for _, mutate := range []func(*managedruntime.Scope){func(s *managedruntime.Scope) { s.UserID = uuid.NewString() }, func(s *managedruntime.Scope) { s.TenantID = uuid.NewString() }, func(s *managedruntime.Scope) { s.FleetID = uuid.NewString() }, func(s *managedruntime.Scope) { s.AgentID = uuid.NewString() }} {
		foreign := scope
		mutate(&foreign)
		if _, err := store.ReadContext(ctx, foreign, main.ID, ref, 0, 100); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("cross-owner reference", err)
		}
	}
	for _, offset := range []int{-1, 1, len(original) + 1} {
		if _, err := store.ReadContext(ctx, scope, main.ID, ref, offset, 100); !errors.Is(err, managedruntime.ErrInvalid) {
			t.Fatal("invalid byte boundary", err)
		}
	}
	if _, err := store.ReadContext(ctx, scope, main.ID, ref, 0, 4097); !errors.Is(err, managedruntime.ErrInvalid) {
		t.Fatal("unbounded original read", err)
	}
}

func TestManagedRuntimeReadsOriginalContextWithoutExecutionGateway(t *testing.T) {
	var calls atomic.Int32
	original := strings.Repeat("开头内容", 2000) + "MIDDLE-PROOF" + strings.Repeat("后文内容", 2000)
	offset := len(strings.Repeat("开头内容", 2000))
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(request["messages"])
		if calls.Add(1) == 1 {
			if strings.Contains(string(encoded), "MIDDLE-PROOF") {
				t.Error("large original was not projected")
			}
			ref := regexp.MustCompile(`ctx_[A-Za-z0-9_-]+`).FindString(string(encoded))
			if ref == "" {
				t.Error("no original content reference")
			}
			streamManagedTool(w, "read_context", map[string]any{"reference": ref, "offset": offset, "limit": 96})
		} else {
			if !strings.Contains(string(encoded), "MIDDLE-PROOF") {
				t.Error("original middle was not readable")
			}
			streamManagedReply(w, "Read the durable original")
		}
	})
	input := f.submit(t, "read-original", f.main.ID, original)
	f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 2 })
	var stored string
	if err := f.pool.QueryRow(context.Background(), `SELECT text FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&stored); err != nil || stored != original {
		t.Fatal("original changed", err)
	}
	var tools int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.tools WHERE state='ready' AND environment_id=''`).Scan(&tools); err != nil || tools != 1 {
		t.Fatal("local tool required execution environment", tools, err)
	}
}
