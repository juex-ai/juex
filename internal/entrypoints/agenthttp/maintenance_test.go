package agenthttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/endpoint"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type cancelShutdownResponse struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelShutdownResponse) Write([]byte) (int, error) {
	w.cancel()
	return 0, context.Canceled
}

func TestConditionalShutdownRollsBackCanceledRequest(t *testing.T) {
	for _, when := range []string{"before-reservation", "after-reservation"} {
		t.Run(when, func(t *testing.T) {
			srv := newTestServer(t)
			active, err := srv.openThread(context.Background(), thread.MainID)
			if err != nil {
				t.Fatal(err)
			}
			expected := endpoint.Runtime{AgentID: "abcdef", InstanceID: "cancel-instance", PID: 42, Endpoint: "tcp://127.0.0.1:12345", StartedAt: time.Now().UTC()}
			shutdown := srv.setEndpointControl(expected)
			defer srv.clearEndpointControl(expected)
			body, err := json.Marshal(endpoint.ShutdownRequest{Runtime: expected})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "/api/control/shutdown-idle", strings.NewReader(string(body))).WithContext(ctx)
			var response http.ResponseWriter = httptest.NewRecorder()
			if when == "before-reservation" {
				cancel()
			} else {
				response = cancelShutdownResponse{httptest.NewRecorder(), cancel}
			}
			srv.APIHandler().ServeHTTP(response, request)
			select {
			case <-shutdown:
				t.Error("canceled request stopped the Agent")
			default:
			}
			if srv.isClosed() {
				t.Error("canceled request left the server draining")
			}
			_, err = active.agent.Engine.ReceivePendingInput(context.Background(), runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "still available"), DeferDelivery: true})
			if err != nil {
				t.Fatalf("canceled request left admission reserved: %v", err)
			}
		})
	}
}

func TestConditionalShutdownFencesIdleIngress(t *testing.T) {
	srv := newTestServer(t)
	active, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.APIHandler())
	defer ts.Close()
	expected := endpoint.Runtime{AgentID: "abcdef", InstanceID: "idle-instance", PID: 42, Endpoint: "tcp://" + strings.TrimPrefix(ts.URL, "http://"), StartedAt: time.Now().UTC()}
	shutdown := srv.setEndpointControl(expected)
	defer srv.clearEndpointControl(expected)
	if err := endpoint.RequestShutdownIfIdle(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdown:
	default:
		t.Fatal("shutdown was not requested")
	}
	_, err = active.agent.Engine.ReceivePendingInput(context.Background(), runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "too late")})
	if !errors.Is(err, runtime.ErrMaintenance) {
		t.Fatalf("late admission=%v", err)
	}
}

func TestConditionalShutdownRejectsDurablePendingInput(t *testing.T) {
	srv := newTestServer(t)
	active, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = active.agent.Engine.ReceivePendingInput(context.Background(), runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "durable input"), Options: &runtime.PendingInputOptions{ID: "waiting", TTL: time.Hour}, DeferDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.APIHandler())
	defer ts.Close()
	expected := endpoint.Runtime{AgentID: "abcdef", InstanceID: "busy-instance", PID: 42, Endpoint: "tcp://" + strings.TrimPrefix(ts.URL, "http://"), StartedAt: time.Now().UTC()}
	shutdown := srv.setEndpointControl(expected)
	defer srv.clearEndpointControl(expected)
	if err := endpoint.RequestShutdownIfIdle(context.Background(), expected); !errors.Is(err, endpoint.ErrRuntimeBusy) {
		t.Fatalf("shutdown=%v", err)
	}
	select {
	case <-shutdown:
		t.Fatal("busy process was stopped")
	default:
	}
	if release, err := active.agent.Engine.ReserveIdleMaintenance(); err == nil {
		release()
		t.Fatal("pending input disappeared")
	}
}

func TestConditionalShutdownRequiresMainAndChecksDormantInputs(t *testing.T) {
	t.Run("before-main-warmup", func(t *testing.T) {
		srv := newTestServer(t)
		if release, reserved := srv.reserveIdleShutdown(context.Background()); reserved {
			release()
			t.Fatal("startup was considered idle before Main recovery")
		}
	})
	t.Run("dormant-worker", func(t *testing.T) {
		srv := newTestServer(t)
		if _, err := srv.openThread(context.Background(), thread.MainID); err != nil {
			t.Fatal(err)
		}
		store := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir)
		worker, err := store.CreateWorker(thread.MainID, "dormant", 1)
		if err != nil {
			t.Fatal(err)
		}
		dir := worker.Dir
		if err := worker.Close(); err != nil {
			t.Fatal(err)
		}
		queue := runtime.NewPendingInputQueue(dir, runtime.PendingInputQueueOptions{})
		if _, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "retained dormant work"), runtime.PendingInputOptions{ID: "dormant", TTL: time.Hour}, ""); err != nil {
			t.Fatal(err)
		}
		if release, reserved := srv.reserveIdleShutdown(context.Background()); reserved {
			release()
			t.Fatal("dormant input ignored")
		}
		if records, err := queue.Records(); err != nil || len(records) != 1 {
			t.Fatalf("input changed: %+v %v", records, err)
		}
	})
}
