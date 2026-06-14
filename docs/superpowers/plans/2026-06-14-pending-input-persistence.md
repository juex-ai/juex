# Pending Input Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist pending input accepted during an active turn so restart recovery can safely replay unfinished user steer and MCP event messages.

**Architecture:** `internal/runtime` owns queue semantics and a session-local append-only JSONL store. `internal/config` exposes TTL policy through `RuntimeLimits`; `internal/app` passes runtime limits and deterministic MCP event ids into the engine.

**Tech Stack:** Go, standard library JSON/time/file APIs, existing `llm.Message`, `session.Session`, and runtime tests.

---

### Task 1: Runtime Pending Queue Store

**Files:**
- Create: `internal/runtime/pending_queue.go`
- Test: `internal/runtime/pending_queue_test.go`

- [ ] **Step 1: Write failing tests**

Add tests for append/load latest record, duplicate id coalescing, TTL expiration, and stable message id generation:

```go
func TestPendingInputQueue_DeduplicatesByID(t *testing.T) {
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{Now: fixedNow})
	first, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "one"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "two"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Message.FirstText() != "one" {
		t.Fatalf("duplicate enqueue replaced record: first=%+v second=%+v", first, second)
	}
	records, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "event-1" {
		t.Fatalf("records = %+v", records)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/runtime -run PendingInputQueue -count=1`
Expected: compile failure because `NewPendingInputQueue` and related types do not exist.

- [ ] **Step 3: Implement queue store**

Create a small append-only store with these production types:

```go
type PendingInputState string

const (
	PendingInputStatePending   PendingInputState = "pending"
	PendingInputStateAdmitted  PendingInputState = "admitted"
	PendingInputStateProcessed PendingInputState = "processed"
	PendingInputStateExpired   PendingInputState = "expired"
	PendingInputStateDropped   PendingInputState = "dropped"
)

type PendingInputOptions struct {
	ID  string
	TTL time.Duration
}
```

The store should load latest records by id from `pending_input.jsonl`, generate ids when omitted, assign stable message ids, mark expired records during replay lookup, and append state snapshots atomically enough for local JSONL use.

- [ ] **Step 4: Verify GREEN**

Run: `mise exec -- go test ./internal/runtime -run PendingInputQueue -count=1`
Expected: PASS.

### Task 2: Engine Replay and Drain Semantics

**Files:**
- Modify: `internal/runtime/loop.go`
- Test: `internal/runtime/loop_test.go`

- [ ] **Step 1: Write failing tests**

Add runtime tests for restart replay, no replay after success, expired discard, duplicate id, and admitted-with-existing-message idempotency. Use `session.Load` for restart simulation and assert provider histories:

```go
func TestTurn_ReplaysPersistedPendingInputAfterRestart(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	eng := newEngineForSession(t, sess, &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "unused"), StopReason: llm.StopEndTurn}}})
	if err := eng.ReserveTurnID("turn-active"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "replay me"), PendingInputOptions{ID: "event-1", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()

	reloaded, err := session.Load(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	prov := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}}}
	restarted := newEngineForSession(t, reloaded, prov)
	if _, err := restarted.Turn(context.Background(), "after restart"); err != nil {
		t.Fatal(err)
	}
	if got := prov.histories[0][len(prov.histories[0])-1].FirstText(); got != "replay me" {
		t.Fatalf("last provider message = %q", got)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/runtime -run 'PendingInput|Replay' -count=1`
Expected: tests fail because engine does not wire the store yet.

- [ ] **Step 3: Implement engine integration**

Add `PendingInputQueue`, `PendingInputTTL`, and `ExternalEventTTL` fields to `Engine`. Persist on enqueue, restore replayable records when a turn is reserved/started, mark admitted before append, mark processed after session append or existing stable message id detection, and mark dropped when active turn failure discards queue items.

- [ ] **Step 4: Verify GREEN**

Run: `mise exec -- go test ./internal/runtime -run 'PendingInput|Replay' -count=1`
Expected: PASS.

### Task 3: Config and App Wiring

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/values.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/app/app.go`
- Modify: `juex.yaml.example`

- [ ] **Step 1: Write failing config and app tests**

Add config tests for:

```yaml
runtime:
  pending_input_ttl: 30m
  external_event_ttl: 48h
```

Expected resolved values: `30*time.Minute` and `48*time.Hour`.

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/config ./internal/app -run 'PendingInputTTL|MCPNotification' -count=1`
Expected: config tests fail before fields exist.

- [ ] **Step 3: Implement config/app wiring**

Parse Go duration strings in `runtimeConfig`, expose values through `RuntimeLimits`, set engine TTL fields in `app.New`, and pass deterministic MCP ids/TTL from `HandleMCPNotification`.

- [ ] **Step 4: Verify GREEN**

Run: `mise exec -- go test ./internal/config ./internal/app -run 'PendingInputTTL|MCPNotification' -count=1`
Expected: PASS.

### Task 4: Full Verification and Taskline Artifacts

**Files:**
- Modify: `ARCHITECTURE.md`
- Modify: `README.md`

- [ ] **Step 1: Update concise docs**

Document `pending_input.jsonl` under runtime files and note the runtime TTL keys.

- [ ] **Step 2: Run focused and full checks**

Run:

```bash
mise exec -- go test ./internal/runtime ./internal/config ./internal/app -count=1
mise exec -- go test ./...
mise exec -- make build
mise exec -- make development-eval
```

- [ ] **Step 3: Create Taskline docs and PR**

Create `Dev Notes`, `Test Report`, commit, push, create PR, link it to Taskline, wait for CI/review, address comments, create `Review Report`, mark done, merge, return to main, and pull.
