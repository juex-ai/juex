package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

type testTurnIDs struct {
	next map[string]int
}

func (g *testTurnIDs) NextTurnID(prefix string) string {
	if g.next == nil {
		g.next = map[string]int{}
	}
	g.next[prefix]++
	return fmt.Sprintf("%s-%d", prefix, g.next[prefix])
}

func TestAdmitTurnStartsWhenIdle(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "hello",
		IDs:    ids,
	})

	if result.Kind != TurnAdmissionStarted {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionStarted, result.Error)
	}
	if result.Start == nil || result.Start.TurnID != "turn-1" || result.Start.Message.FirstText() != "hello" {
		t.Fatalf("start = %+v", result.Start)
	}
	if status := a.Engine.PendingInputStatus(); status.TurnID != "turn-1" {
		t.Fatalf("runtime active turn = %+v", status)
	}
}

func TestAdmitTurnStartsWithImageAttachments(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}
	media := turnAdmissionMediaRef()

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt:      "describe this",
		Attachments: []llm.MediaRef{media},
		IDs:         ids,
	})

	if result.Kind != TurnAdmissionStarted {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionStarted, result.Error)
	}
	if result.Start == nil || result.Start.TurnID != "turn-1" {
		t.Fatalf("start = %+v", result.Start)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "attachment_vision_unavailable" ||
		!strings.Contains(result.Warnings[0].Message, "openai:m") ||
		!strings.Contains(result.Warnings[0].Suggestion, "providers[].models[].capabilities.vision") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	blocks := result.Start.Message.Blocks
	if len(blocks) != 2 || blocks[0].Type != llm.BlockText || blocks[0].Text != "describe this" ||
		blocks[1].Type != llm.BlockImage || blocks[1].Media == nil || blocks[1].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("message blocks = %+v", blocks)
	}
}

func TestAdmitTurnVisionCapabilitySuppressesAttachmentWarning(t *testing.T) {
	a, _ := newStubApp(t)
	vision := true
	a.cfg.ProviderCapabilities.Vision = &vision

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt:      "describe this",
		Attachments: []llm.MediaRef{turnAdmissionMediaRef()},
		IDs:         &testTurnIDs{},
	})

	if result.Kind != TurnAdmissionStarted || len(result.Warnings) != 0 {
		t.Fatalf("result = %+v, want started without warnings", result)
	}
}

func TestAdmitTurnStartsWithImageOnlyInput(t *testing.T) {
	a, _ := newStubApp(t)
	media := turnAdmissionMediaRef()

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Attachments: []llm.MediaRef{media},
		IDs:         &testTurnIDs{},
	})

	if result.Kind != TurnAdmissionStarted {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionStarted, result.Error)
	}
	blocks := result.Start.Message.Blocks
	if len(blocks) != 1 || blocks[0].Type != llm.BlockImage || blocks[0].Media == nil || blocks[0].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("message blocks = %+v", blocks)
	}
}

func TestAdmitTurnRejectsSlashCommandWithAttachments(t *testing.T) {
	a, _ := newStubApp(t)

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt:      "/status",
		Attachments: []llm.MediaRef{turnAdmissionMediaRef()},
		IDs:         &testTurnIDs{},
	})

	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCompleteAdmittedTurnAllowsNextTurn(t *testing.T) {
	a, _ := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "done"),
		StopReason: llm.StopEndTurn,
	})
	ids := &testTurnIDs{}

	first := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first", IDs: ids})
	if first.Kind != TurnAdmissionStarted || first.TurnID != "turn-1" {
		t.Fatalf("first = %+v", first)
	}

	if _, err := a.Engine.TurnMessageWithID(context.Background(), first.Start.Message, first.TurnID); err != nil {
		t.Fatal(err)
	}
	a.CompleteAdmittedTurn("turn-1")
	second := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "second", IDs: ids})
	if second.Kind != TurnAdmissionStarted || second.TurnID != "turn-2" {
		t.Fatalf("second = %+v", second)
	}
	if status := a.Engine.PendingInputStatus(); status.TurnID != "turn-2" || status.PendingCount != 0 {
		t.Fatalf("runtime active turn = %+v", status)
	}
}

func TestAdmitTurnQueuesWhileRunning(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}
	start := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first", IDs: ids})
	if start.Kind != TurnAdmissionStarted {
		t.Fatalf("start = %+v", start)
	}

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "second",
		IDs:    ids,
	})

	if result.Kind != TurnAdmissionQueued {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionQueued, result.Error)
	}
	if result.TurnID != "turn-1" || !result.Queued || result.PendingCount != 1 {
		t.Fatalf("queued result = %+v", result)
	}
}

func TestAdmitTurnQueuesImageBlocksWhileRunning(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}
	start := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first", IDs: ids})
	if start.Kind != TurnAdmissionStarted {
		t.Fatalf("start = %+v", start)
	}
	media := turnAdmissionMediaRef()

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt:      "second",
		Attachments: []llm.MediaRef{media},
		IDs:         ids,
	})

	if result.Kind != TurnAdmissionQueued {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionQueued, result.Error)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "attachment_vision_unavailable" {
		t.Fatalf("queued warnings = %+v", result.Warnings)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records = %d, want 1", len(records))
	}
	for _, record := range records {
		blocks := record.Message.Blocks
		if len(blocks) != 2 || blocks[0].Type != llm.BlockText || blocks[0].Text != "second" ||
			blocks[1].Type != llm.BlockImage || blocks[1].Media == nil || blocks[1].Media.ArtifactPath != media.ArtifactPath {
			t.Fatalf("queued message blocks = %+v", blocks)
		}
	}
}

func TestAdmitTurnQueuesDuringCompactAndPromotesPendingInput(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}
	compactID := ids.NextTurnID("compact")
	if err := a.beginCompactAdmission(compactID); err != nil {
		t.Fatal(err)
	}

	queued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "after compact",
		IDs:    ids,
	})
	if queued.Kind != TurnAdmissionQueued || queued.TurnID != compactID {
		t.Fatalf("queued = %+v", queued)
	}

	promoted := a.finishCompactAdmission(compactID, ids)
	if promoted == nil || promoted.TurnID != "turn-1" || promoted.Message.FirstText() != "after compact" {
		t.Fatalf("promoted = %+v", promoted)
	}
	if status := a.Engine.PendingInputStatus(); status.TurnID != "turn-1" || status.PendingCount != 0 {
		t.Fatalf("runtime pending status = %+v", status)
	}
}

func TestAdmitTurnQueuesImageBlocksDuringCompactAndPromotesPendingInput(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}
	compactID := ids.NextTurnID("compact")
	if err := a.beginCompactAdmission(compactID); err != nil {
		t.Fatal(err)
	}
	media := turnAdmissionMediaRef()

	queued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Attachments: []llm.MediaRef{media},
		IDs:         ids,
	})
	if queued.Kind != TurnAdmissionQueued || queued.TurnID != compactID {
		t.Fatalf("queued = %+v", queued)
	}

	promoted := a.finishCompactAdmission(compactID, ids)
	if promoted == nil || promoted.TurnID != "turn-1" {
		t.Fatalf("promoted = %+v", promoted)
	}
	blocks := promoted.Message.Blocks
	if len(blocks) != 1 || blocks[0].Type != llm.BlockImage || blocks[0].Media == nil || blocks[0].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("promoted message blocks = %+v", blocks)
	}
}

func TestAdmitTurnStatusSlashAllowedWhileRunning(t *testing.T) {
	a, prov := newStubApp(t)
	ids := &testTurnIDs{}
	start := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first", IDs: ids})
	if start.Kind != TurnAdmissionStarted {
		t.Fatalf("start = %+v", start)
	}

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "/status",
		IDs:    ids,
	})

	if result.Kind != TurnAdmissionCommandCompleted {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionCommandCompleted, result.Error)
	}
	if result.Command == nil || result.Command.Name != SlashStatus ||
		!strings.Contains(result.Command.Text, "observables: 0/0 running, 0 errors") ||
		strings.Contains(result.Command.Text, "Juex status") {
		t.Fatalf("command = %+v", result.Command)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
}

func turnAdmissionMediaRef() llm.MediaRef {
	return llm.MediaRef{
		ArtifactPath:  ".juex/artifacts/media/session-1/image.png",
		MediaType:     "image/png",
		SHA256:        strings.Repeat("a", 64),
		OriginalBytes: 123,
		Width:         2,
		Height:        3,
	}
}

func TestAdmitTurnNewSlashRejectsWhileBusy(t *testing.T) {
	a, _ := newStubApp(t)
	ids := &testTurnIDs{}
	start := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first", IDs: ids})
	if start.Kind != TurnAdmissionStarted {
		t.Fatalf("start = %+v", start)
	}

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "/new",
		IDs:    ids,
	})

	if result.Kind != TurnAdmissionConflict || result.Error.Message != "session busy" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdmitTurnNewSlashStartsGreetingTurnWhenIdle(t *testing.T) {
	a, _ := newStubApp(t)
	oldID := a.Session.ID

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "/new",
		IDs:    &testTurnIDs{},
	})

	if result.Kind != TurnAdmissionCommandCompleted {
		t.Fatalf("kind = %s, want %s; error=%+v", result.Kind, TurnAdmissionCommandCompleted, result.Error)
	}
	if result.SessionChanged == nil || result.SessionChanged.OldID != oldID || result.SessionChanged.NewID != a.Session.ID {
		t.Fatalf("session change = %+v, old=%s new=%s", result.SessionChanged, oldID, a.Session.ID)
	}
	if result.Command == nil || result.Command.Name != SlashNew {
		t.Fatalf("command = %+v", result.Command)
	}
	if result.TurnID != "turn-1" || result.Start == nil {
		t.Fatalf("started turn = %+v, turn_id=%q", result.Start, result.TurnID)
	}
	if result.Start.Message.FirstText() != NewSessionGreetingPrompt() {
		t.Fatalf("start message = %q, want greeting prompt", result.Start.Message.FirstText())
	}
	if status := a.Engine.PendingInputStatus(); status.TurnID != "turn-1" {
		t.Fatalf("runtime active turn = %+v, want turn-1", status)
	}
}

func TestAdmitTurnMapsQueueFailures(t *testing.T) {
	t.Run("queue full", func(t *testing.T) {
		a, _ := newStubApp(t)
		a.Engine.MaxPendingInputs = 1
		ids := &testTurnIDs{}
		start := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first", IDs: ids})
		if start.Kind != TurnAdmissionStarted {
			t.Fatalf("start = %+v", start)
		}
		if firstQueued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "second", IDs: ids}); firstQueued.Kind != TurnAdmissionQueued {
			t.Fatalf("first queued = %+v", firstQueued)
		}

		result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
			Prompt: "third",
			IDs:    ids,
		})

		if result.Kind != TurnAdmissionRejected || result.Error.Kind != "pending_input_full" || !result.Error.Retryable {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("no active turn", func(t *testing.T) {
		a, _ := newStubApp(t)
		a.turnAdmission.mu.Lock()
		a.turnAdmission.phase = turnAdmissionRunning
		a.turnAdmission.turnID = "turn-missing"
		a.turnAdmission.mu.Unlock()

		result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
			Prompt: "queued",
			IDs:    &testTurnIDs{},
		})

		if result.Kind != TurnAdmissionConflict || !errors.Is(result.Err, runtime.ErrNoActiveTurn) {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestAdmitTurnMalformedSlashReturnsBadRequest(t *testing.T) {
	a, _ := newStubApp(t)
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "/status verbose",
		IDs:    &testTurnIDs{},
	})

	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" || result.Error.Retryable {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Error.Suggestion, AvailableSlashCommandsText()) {
		t.Fatalf("suggestion = %q", result.Error.Suggestion)
	}
}

func TestAdmitTurnRequiresInitializedApp(t *testing.T) {
	t.Run("nil app", func(t *testing.T) {
		var app *App
		result := app.AdmitTurn(context.Background(), TurnAdmissionRequest{
			Prompt: "hello",
			IDs:    &testTurnIDs{},
		})
		assertUninitializedAdmission(t, result)
	})

	t.Run("nil engine", func(t *testing.T) {
		app, _ := newStubApp(t)
		app.Engine = nil
		result := app.AdmitTurn(context.Background(), TurnAdmissionRequest{
			Prompt: "hello",
			IDs:    &testTurnIDs{},
		})
		assertUninitializedAdmission(t, result)
	})

	t.Run("nil session", func(t *testing.T) {
		app, _ := newStubApp(t)
		app.Session = nil
		result := app.AdmitTurn(context.Background(), TurnAdmissionRequest{
			Prompt: "hello",
			IDs:    &testTurnIDs{},
		})
		assertUninitializedAdmission(t, result)
	})
}

func assertUninitializedAdmission(t *testing.T, result TurnAdmissionResult) {
	t.Helper()
	if result.Kind != TurnAdmissionError || result.Error.Kind != "general_error" {
		t.Fatalf("result = %+v", result)
	}
	if result.Error.Message != "turn admission: app, engine, or session is not initialized" {
		t.Fatalf("message = %q", result.Error.Message)
	}
}
