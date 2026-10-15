package app

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

func TestAdmitTurnPreservesAttachmentsAndWarnings(t *testing.T) {
	a, _ := newStubApp(t)
	media := turnAdmissionMediaRef()
	result := a.AdmitTurn(context.Background(), agent.TurnAdmissionRequest{Prompt: "describe this", Attachments: []llm.MediaRef{media}})
	if result.Kind != agent.TurnAdmissionStarted || result.Start == nil {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "attachment_vision_unavailable" {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	blocks := result.Start.Message.Blocks
	if len(blocks) != 2 || blocks[0].Text != "describe this" || blocks[1].Type != llm.BlockImage || blocks[1].Media == nil || blocks[1].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestAdmitTurnVisionCapabilitySuppressesAttachmentWarning(t *testing.T) {
	a, _ := newStubApp(t)
	vision := true
	a.cfg.ProviderCapabilities.Vision = &vision
	result := a.AdmitTurn(context.Background(), agent.TurnAdmissionRequest{Attachments: []llm.MediaRef{turnAdmissionMediaRef()}})
	if result.Kind != agent.TurnAdmissionStarted || len(result.Warnings) != 0 || len(result.Start.Message.Blocks) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdmitTurnQueuesAttachmentBlocksBehindActiveTurn(t *testing.T) {
	a, _ := newStubApp(t)
	started := a.AdmitTurn(context.Background(), agent.TurnAdmissionRequest{Prompt: "first"})
	if started.Kind != agent.TurnAdmissionStarted {
		t.Fatalf("started = %+v", started)
	}
	media := turnAdmissionMediaRef()
	queued := a.AdmitTurn(context.Background(), agent.TurnAdmissionRequest{Prompt: "second", Attachments: []llm.MediaRef{media}})
	if queued.Kind != agent.TurnAdmissionQueued || len(queued.Warnings) != 1 {
		t.Fatalf("queued = %+v", queued)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.State == runtime.PendingInputStatePending && (len(record.Message.Blocks) != 2 || record.Message.Blocks[1].Media == nil || record.Message.Blocks[1].Media.ArtifactPath != media.ArtifactPath) {
			t.Fatalf("queued record = %+v", record)
		}
	}
}

func turnAdmissionMediaRef() llm.MediaRef {
	return llm.MediaRef{
		ArtifactPath: "threads/123456/media/image.png", MediaType: "image/png",
		SHA256: strings.Repeat("a", 64), OriginalBytes: 123, Width: 2, Height: 3,
	}
}
