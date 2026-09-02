package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/statusapi"
)

func TestFleetRestartContinuesFailedTurnOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary failed Turn restart is slow")
	}
	binary := buildJuex(t)
	for _, mode := range []string{"single", "bulk"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			requests := make(chan []map[string]any, 4)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode provider request: %v", err)
					http.Error(w, "invalid body", http.StatusBadRequest)
					return
				}
				select {
				case requests <- body.Messages:
				default:
					t.Error("unexpected extra provider request")
				}
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"error":{"message":"test provider failure","type":"authentication_error"}}`)
					return
				}
				_, _ = io.WriteString(w, chatCompletionResponse("continued failed work"))
			}))
			t.Cleanup(provider.Close)

			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			workspace := t.TempDir()
			agentID := "aaaaaa"
			address := writeFleetE2EAgent(t, home, workspace, agentID)
			writeFleetProviderConfig(t, workspace, provider.URL)
			environment := fleetE2EEnvironmentForProvider(home, "local-chat", "openai/chat", provider.URL, "chat-test")
			t.Cleanup(func() { shutdownFleetAgent(t, address) })
			if stdout, stderr, err := runFleetE2E(binary, environment, "", "start", agentID); err != nil {
				t.Fatalf("start: %v\n%s\n%s", err, stdout, stderr)
			}
			originalRuntime := waitFleetRuntime(t, address)
			threadID, originalTurnID := startFleetBlockingTurn(t, originalRuntime)
			failed := waitFleetTurnState(t, originalRuntime, threadID, originalTurnID, statusapi.TurnErrored)
			if failed.State != statusapi.ActivityIdle || failed.SelectedStatus.Thread.State != statusapi.ThreadFailed {
				t.Fatalf("failed activity = %+v", failed)
			}
			select {
			case <-requests: // The original request has durably failed before restart.
			default:
				t.Fatal("original failure did not reach provider")
			}

			restart := func(wantResume bool) {
				t.Helper()
				if mode == "single" {
					stdout, stderr, err := runFleetE2E(binary, environment, "", "restart", agentID)
					if err != nil {
						t.Fatalf("restart: %v\n%s\n%s", err, stdout, stderr)
					}
					if got := strings.Contains(stdout, "resume=sent"); got != wantResume {
						t.Fatalf("restart resume=%v, want %v:\n%s", got, wantResume, stdout)
					}
					return
				}
				manager, err := fleet.New(fleet.Options{HomeDir: home, Executable: binary})
				if err != nil {
					t.Fatal(err)
				}
				result, err := manager.RestartRunningAgents(context.Background())
				if err != nil || result.Restarted != 1 || len(result.Items) != 1 {
					t.Fatalf("bulk restart: %+v, %v", result, err)
				}
				if result.Items[0].Resume.Sent != wantResume || result.Items[0].Resume.Error != "" {
					t.Fatalf("bulk continuation = %+v, want sent=%v", result.Items[0].Resume, wantResume)
				}
			}
			restart(true)
			replacement := waitFleetRuntimeVersion(t, address, originalRuntime.InstanceID, originalRuntime.BinaryVersion)
			completed := waitFleetTurnState(t, replacement, threadID, "", statusapi.TurnCompleted)
			if completed.SelectedStatus.Turn.ID == originalTurnID {
				t.Fatal("continuation overwrote the failed Turn identity")
			}
			select {
			case history := <-requests:
				originalInputs, notices := 0, 0
				for _, message := range history {
					if message["role"] != "user" {
						continue
					}
					content, _ := json.Marshal(message["content"])
					if strings.Contains(string(content), "work until restarted") {
						originalInputs++
					}
					if strings.Contains(string(content), "System notice") && strings.Contains(string(content), "failed") {
						notices++
					}
				}
				if originalInputs != 1 || notices != 1 {
					t.Fatalf("continuation history has original=%d notices=%d: %+v", originalInputs, notices, history)
				}
			default:
				t.Fatal("completed continuation did not reach provider")
			}

			// A later explicit restart must not repeat the now-completed work.
			restart(false)
			finalRuntime := waitFleetRuntimeVersion(t, address, replacement.InstanceID, replacement.BinaryVersion)
			waitFleetTurnState(t, finalRuntime, threadID, completed.SelectedStatus.Turn.ID, statusapi.TurnCompleted)
			if got := calls.Load(); got != 2 {
				t.Fatalf("provider requests = %d, want original failure and one continuation", got)
			}
		})
	}
}

func waitFleetTurnState(t *testing.T, state endpoint.Runtime, threadID, turnID string, want statusapi.TurnState) statusapi.AgentActivity {
	t.Helper()
	target, err := endpoint.Parse(state.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := target.NewClient()
	var activity statusapi.AgentActivity
	for ctx.Err() == nil {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL("/api/status"), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("read Agent activity: %v", err)
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&activity)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil {
			t.Fatalf("activity response: status=%d error=%v", response.StatusCode, decodeErr)
		}
		if selected := activity.SelectedStatus; selected != nil && selected.Thread.ID == threadID &&
			selected.Turn != nil && selected.Turn.State == want && (turnID == "" || selected.Turn.ID == turnID) {
			return activity
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Agent did not reach thread=%s turn=%s state=%s; last activity: %+v", threadID, turnID, want, activity)
	return activity
}
