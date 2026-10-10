package managedruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type inputAuthority struct {
	Authority
	scope     Scope
	snapshots int
}

func (a *inputAuthority) Authorize(context.Context, string, string, string, bool) (Scope, error) {
	return a.scope, nil
}
func (a *inputAuthority) Snapshot(context.Context, Scope) (TurnConfig, error) {
	a.snapshots++
	return TurnConfig{Models: []ModelConfig{{ModelID: "vision"}}}, nil
}
func (a *inputAuthority) Provider(context.Context, Scope, ModelConfig, ModelRequirements) (llm.Provider, error) {
	return nil, nil
}

type inputStore struct {
	ConversationStore
	accepted *InputReceipt
	existing int
	writes   int
}

func (s *inputStore) EnsureAgent(context.Context, Scope) (Thread, error) { return Thread{}, nil }
func (s *inputStore) ExistingInput(context.Context, Scope, InputRequest) (InputReceipt, bool, error) {
	s.existing++
	if s.accepted != nil {
		return *s.accepted, true, nil
	}
	return InputReceipt{}, false, nil
}
func (s *inputStore) AcceptInput(context.Context, Scope, InputRequest) (InputReceipt, error) {
	s.writes++
	return InputReceipt{ID: "new"}, nil
}

func TestImageInputAdmissionValidatesContentAndKeepsConcurrentReceipt(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	input := InputRequest{RequestID: "image-only", Images: []InputImage{{ArtifactID: uuid.NewString(), SHA256: execprotocol.FileDigest(data), MediaType: "image/png", Size: int64(len(data))}}}
	for _, scenario := range []string{"ready", "forged-type", "changed-hash", "changed-size", "missing", "concurrent-acceptance", "authority-changed"} {
		t.Run(scenario, func(t *testing.T) {
			store := &inputStore{}
			authority := &inputAuthority{}
			service := Service{Store: store, Authority: authority, Media: mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) {
				switch scenario {
				case "forged-type":
					return []byte("not an image"), nil
				case "changed-hash":
					return append([]byte(nil), append(data[:len(data)-1:len(data)-1], byte(1))...), nil
				case "changed-size":
					return data[:10], nil
				case "missing":
					return nil, ErrDenied
				case "concurrent-acceptance":
					store.accepted = &InputReceipt{ID: "concurrent"}
					return nil, ErrDenied
				case "authority-changed":
					authority.scope.AgentExecutionEpoch++
				}
				return data, nil
			})}
			receipt, err := service.Submit(context.Background(), "actor", "tenant", "agent", input)
			switch scenario {
			case "ready":
				if err != nil || receipt.ID != "new" || store.writes != 1 {
					t.Fatal(receipt, err, store.writes)
				}
			case "concurrent-acceptance":
				if err != nil || receipt.ID != "concurrent" || store.existing != 2 || store.writes != 0 {
					t.Fatal("receipt was lost after concurrent admission", receipt, err)
				}
			case "authority-changed":
				if !errors.Is(err, ErrDenied) || store.writes != 0 {
					t.Fatal("stale authority admitted input", err)
				}
			default:
				if !errors.Is(err, ErrMediaUnavailable) || store.writes != 0 {
					t.Fatal("unverified media admitted", err)
				}
			}
		})
	}
}

func TestExistingImageReceiptDoesNotRevalidateExpiredMediaOrModel(t *testing.T) {
	store := &inputStore{accepted: &InputReceipt{ID: "durable"}}
	authority := &inputAuthority{}
	service := Service{Store: store, Authority: authority}
	input := InputRequest{RequestID: "retry", Images: []InputImage{{ArtifactID: uuid.NewString(), SHA256: execprotocol.FileDigest([]byte("old")), MediaType: "image/png", Size: 3}}}
	if result, err := service.Submit(context.Background(), "actor", "tenant", "agent", input); err != nil || result.ID != "durable" || authority.snapshots != 0 || store.writes != 0 {
		t.Fatal(result, err)
	}
}

func TestImageInputLimitsAndIdentity(t *testing.T) {
	image := InputImage{ArtifactID: uuid.NewString(), SHA256: execprotocol.FileDigest([]byte("image")), MediaType: "image/webp", Size: llm.MaxProviderImageArtifactBytes}
	input := InputRequest{RequestID: "eight"}
	for range MaxInputImages {
		input.Images = append(input.Images, image)
	}
	if err := input.Validate(); err != nil {
		t.Fatal("full eight-image input rejected", err)
	}
	input.Images = append(input.Images, image)
	if !errors.Is(input.Validate(), ErrInvalid) {
		t.Fatal("ninth image accepted")
	}
	input.Images = input.Images[:1]
	input.Images[0].Size++
	if !errors.Is(input.Validate(), ErrInvalid) {
		t.Fatal("oversized image accepted")
	}
	input.Images[0] = image
	input.Images[0].MediaType = "image/svg+xml"
	if !errors.Is(input.Validate(), ErrInvalid) {
		t.Fatal("active image format accepted")
	}
	input.Images = nil
	if !errors.Is(input.Validate(), ErrInvalid) {
		t.Fatal("empty input accepted")
	}
}
