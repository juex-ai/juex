package managedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type mediaGatewayFunc func(context.Context, Scope, llm.MediaRef) ([]byte, error)

func (f mediaGatewayFunc) ReadMedia(ctx context.Context, scope Scope, ref llm.MediaRef) ([]byte, error) {
	return f(ctx, scope, ref)
}

func TestRequestMediaPreservesReferencesAndDoesNotPersistBytes(t *testing.T) {
	data := []byte("private image bytes")
	ref := &llm.MediaRef{ArtifactID: uuid.NewString(), SHA256: execprotocol.FileDigest(data), MediaType: "image/png", OriginalBytes: 123456}
	source := []llm.Message{{ID: "one", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockImage, Media: ref}, {Type: llm.BlockImage, Media: ref}}}}
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	scope, calls := Scope{AgentID: "agent", TenantID: "tenant", ActorID: "actor"}, 0
	reader := mediaGatewayFunc(func(_ context.Context, got Scope, value llm.MediaRef) ([]byte, error) {
		calls++
		if !reflect.DeepEqual(got, scope) || value.ArtifactID != ref.ArtifactID {
			t.Fatal("media read lost authority scope")
		}
		return data, nil
	})
	request, err := hydrateMedia(context.Background(), reader, scope, source)
	if err != nil || calls != 1 || !reflect.DeepEqual(request[0].Blocks[0].Media.Data, data) || len(ref.Data) != 0 {
		t.Fatal("request hydration lost data, repeated a read or mutated history", calls, err)
	}
	persisted, err := json.Marshal(request)
	if err != nil || string(before) != string(persisted) {
		t.Fatal("request-only bytes entered persisted model request", err)
	}
	other := *ref
	other.ArtifactID = uuid.NewString()
	source[0].Blocks[1].Media = &other
	calls = 0
	reader = mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) { calls++; return data, nil })
	if _, err := hydrateMedia(context.Background(), reader, scope, source); err != nil || calls != 2 {
		t.Fatal("content hash bypassed distinct Artifact authorization", calls, err)
	}
}

func TestRequestMediaFailureCannotBecomeTextFallback(t *testing.T) {
	data := []byte("image")
	ref := &llm.MediaRef{ArtifactID: uuid.NewString(), SHA256: execprotocol.FileDigest(data), MediaType: "image/png"}
	message := []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockImage, Media: ref}}}}
	for _, reader := range []MediaGateway{
		nil,
		mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) { return nil, ErrDenied }),
		mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) { return []byte("changed"), nil }),
		mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) { return nil, nil }),
	} {
		if _, err := hydrateMedia(context.Background(), reader, Scope{}, message); !errors.Is(err, ErrMediaUnavailable) {
			t.Fatal("missing or unverified media was silently omitted", err)
		}
	}
	ref.ArtifactID, ref.ArtifactPath = "", "old-machine-relative-path"
	if _, err := hydrateMedia(context.Background(), nil, Scope{}, message); !errors.Is(err, ErrMediaUnavailable) {
		t.Fatal("managed Runtime attempted a machine-local media path", err)
	}
}

func TestRequestMediaBoundsRepeatedWireBytesAndHonorsCancellation(t *testing.T) {
	for _, size := range []int{llm.MaxProviderImageArtifactBytes, llm.MaxProviderImageArtifactBytes + 1} {
		data := bytes.Repeat([]byte{1}, size)
		ref := &llm.MediaRef{ArtifactID: uuid.NewString(), SHA256: execprotocol.FileDigest(data), MediaType: "image/png"}
		reader := mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) { return data, nil })
		history := []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockImage, Media: ref}}}}
		_, err := hydrateMedia(context.Background(), reader, Scope{}, history)
		if size > llm.MaxProviderImageArtifactBytes {
			if !errors.Is(err, ErrMediaUnavailable) {
				t.Fatal("oversized image accepted", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for len(history[0].Blocks) <= MaxInputImages {
			history[0].Blocks = append(history[0].Blocks, history[0].Blocks[0])
		}
		if _, err := hydrateMedia(context.Background(), reader, Scope{}, history); !errors.Is(err, ErrMediaUnavailable) {
			t.Fatal("cached image bypassed total wire size limit", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		reader = mediaGatewayFunc(func(context.Context, Scope, llm.MediaRef) ([]byte, error) { cancel(); return nil, ctx.Err() })
		if _, err := hydrateMedia(ctx, reader, Scope{}, history); !errors.Is(err, context.Canceled) {
			t.Fatal("shutdown became a durable media failure", err)
		}
	}
}
