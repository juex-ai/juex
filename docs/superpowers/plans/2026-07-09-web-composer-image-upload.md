# Web Composer Image Upload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the web composer upload one or more images and send them as durable `MediaRef` image blocks in user turns.

**Architecture:** Add a shared `internal/usermedia` package for storing and validating session-scoped uploaded images. The web API owns multipart upload and reference admission, while `internal/app` only converts already-validated prompt plus `MediaRef` values into `llm.Message` blocks. The frontend reuses the existing prompt attachment state and converts local `File` objects to uploaded `MediaRef` values before starting a turn.

**Tech Stack:** Go HTTP handlers and tests, JueX `llm.MediaRef`, React/TypeScript composer UI, existing frontend test runner.

---

## Architecture Review

Options considered:

- Store upload logic in `internal/web`: smallest today, but blocks CLI `--attach` reuse.
- Add `internal/usermedia`: slightly more files, but keeps validation reusable and avoids coupling app admission to HTTP.
- Reuse provider image-reading internals from `internal/llm`: rejected because those helpers are provider projection details and do not enforce session ownership.

Chosen approach: `internal/usermedia`. It gives a clear boundary: web/CLI produce validated `MediaRef` values, app/runtime consume canonical messages.

## Task 1: User Media Helper

**Files:**
- Create: `internal/usermedia/usermedia.go`
- Test: `internal/usermedia/usermedia_test.go`

- [ ] **Step 1: Write failing tests**

Cover PNG storage, SHA de-duplication, unsupported MIME rejection, 10 MiB limit rejection, cross-session path rejection, path traversal rejection, and SHA mismatch rejection.

Run:

```bash
go test ./internal/usermedia -count=1
```

Expected: FAIL because `internal/usermedia` does not exist.

- [ ] **Step 2: Implement minimal helper**

Expose:

```go
type Limits struct { MaxBytes int64 }
func StoreUpload(workDir, sessionID, filename string, r io.Reader, limits Limits) (llm.MediaRef, error)
func ValidateSessionMediaRefs(workDir, sessionID string, refs []llm.MediaRef, limits Limits) error
```

Use `.juex/artifacts/media/<session-id>/<sha256>.<ext>` as the only accepted storage root.

- [ ] **Step 3: Verify helper**

Run:

```bash
go test ./internal/usermedia -count=1
```

Expected: PASS.

## Task 2: App Admission Blocks

**Files:**
- Modify: `internal/app/turn_admission.go`
- Modify: `internal/app/turn_admission_queue.go`
- Test: `internal/app/turn_admission_test.go`

- [ ] **Step 1: Write failing admission tests**

Add tests for image-only turns, text-plus-image turns, queued image turns, and compact promotion preserving image blocks.

Run:

```bash
go test ./internal/app -run 'TestAdmitTurn.*Image|TestAdmitTurnQueues.*Image' -count=1
```

Expected: FAIL because `TurnAdmissionRequest` has no attachments and queueing uses text only.

- [ ] **Step 2: Implement admission**

Add `Attachments []llm.MediaRef` to `TurnAdmissionRequest`. Build a user message with an optional text block followed by one image block per attachment. Reject an empty prompt with no attachments. Reject slash commands with attachments.

- [ ] **Step 3: Verify admission**

Run:

```bash
go test ./internal/app -run 'TestAdmitTurn.*Image|TestAdmitTurnQueues.*Image|TestAdmitTurn' -count=1
```

Expected: PASS.

## Task 3: Web Upload and Turn Validation

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers.go`
- Test: `internal/web/handlers_test.go`
- Test: `tests/e2e/web_test.go`

- [ ] **Step 1: Write failing web tests**

Add route tests for successful upload, non-image rejection, cross-session turn rejection, image-only turn acceptance, and text-plus-image provider projection.

Run:

```bash
go test ./internal/web -run 'TestPostSessionAttachment|TestPostTurn.*Attachment' -count=1
go test ./tests/e2e -run TestWeb_ComposerImageUpload -count=1
```

Expected: FAIL because `/attachments` and turn attachments are not implemented.

- [ ] **Step 2: Implement web routes**

Add `POST /api/sessions/<id>/attachments` to dispatch, parse one multipart `file` part, call `usermedia.StoreUpload`, and return the `MediaRef`. Extend `turnRequest` and validate attachments with `usermedia.ValidateSessionMediaRefs` before admission.

- [ ] **Step 3: Verify web behavior**

Run:

```bash
go test ./internal/web -run 'TestPostSessionAttachment|TestPostTurn.*Attachment' -count=1
go test ./tests/e2e -run TestWeb_ComposerImageUpload -count=1
```

Expected: PASS.

## Task 4: Frontend Composer Wiring

**Files:**
- Modify: `frontend/src/api.ts`
- Modify: `frontend/src/pages/Session.tsx`
- Modify: `frontend/src/lib/composer-submit.ts`
- Modify: `frontend/src/lib/live-session-projection.ts`
- Modify: `frontend/src/lib/session-read-state.ts`
- Modify: `frontend/src/lib/queued-inputs.ts`
- Modify: `frontend/src/components/QueuedInputStack.tsx`
- Test: `tests/frontend/composer-submit.test.ts`
- Test: `tests/frontend/live-session-projection.test.ts`
- Test: `tests/frontend/session-read-state.test.ts`

- [ ] **Step 1: Write failing frontend tests**

Cover attachment-only submit actions, `startTurn` request bodies with attachments, optimistic image projection, and queued image summaries.

Run:

```bash
pnpm --dir frontend test --run composer-submit live-session-projection session-read-state
```

Expected: FAIL because frontend APIs and projections are text-only.

- [ ] **Step 2: Implement frontend**

Use `PromptInput` with `accept="image/*"`, `multiple`, `maxFiles={8}`, and `maxFileSize={10 * 1024 * 1024}`. Add a picker button, preview strip, upload-before-send flow, and pass uploaded `MediaRef` values to `startTurn` and optimistic projection.

- [ ] **Step 3: Verify frontend**

Run:

```bash
pnpm --dir frontend test --run composer-submit live-session-projection session-read-state
pnpm --dir frontend build
```

Expected: PASS.

## Task 5: Documentation and Final Validation

**Files:**
- Modify: `DESIGN.md`
- Modify: `frontend/README.md` if the composer API notes become stale.

- [ ] **Step 1: Update docs**

Remove or revise the stale image/file attachment non-goal and document the web composer image behavior in the smallest relevant location.

- [ ] **Step 2: Run final checks**

Run:

```bash
go test ./internal/usermedia ./internal/app ./internal/web ./tests/e2e -count=1
make test
make build
git diff --check
```

Expected: PASS.
