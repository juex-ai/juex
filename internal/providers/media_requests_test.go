package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestProviderRequestIncludesAuthorizedUserAndToolImageBytes(t *testing.T) {
	encoded := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg=="
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []llm.Protocol{llm.ProtocolOpenAIChat, llm.ProtocolOpenAIResponses, llm.ProtocolOpenAICodexResponses, llm.ProtocolAnthropicMessages} {
		t.Run(string(protocol), func(t *testing.T) {
			observed := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				observed <- body
				writeHeaderTestResponse(w, protocol, true)
			}))
			defer server.Close()
			provider, err := NewProvider(llm.ProviderProfile{ID: "managed", Protocol: protocol, APIKey: "fixture", Model: "model", BaseURL: server.URL, Capabilities: llm.ProviderCapabilities{Vision: true, Streaming: true, Tools: true}})
			if err != nil {
				t.Fatal(err)
			}
			ref := &llm.MediaRef{ArtifactID: uuid.NewString(), MediaType: "image/png", SHA256: execprotocol.FileDigest(data), OriginalBytes: 1000000, Data: data}
			history := []llm.Message{
				{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "look"}, {Type: llm.BlockImage, Media: ref}}},
				{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "image.png"}}}},
				{Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "read", Content: "image", Media: ref}}},
			}
			response, err := llm.CompleteWithOptions(context.Background(), provider, "", history, []llm.ToolSpec{{Name: "read", Schema: map[string]any{"type": "object"}}}, llm.CompleteOptions{SingleAttempt: true})
			if err != nil || response.Message.FirstText() != "ok" {
				t.Fatal("image request failed", err)
			}
			body := <-observed
			images := 0
			var visit func(any)
			visit = func(value any) {
				switch value := value.(type) {
				case []any:
					for _, child := range value {
						visit(child)
					}
				case map[string]any:
					if value["type"] == "image" {
						source, _ := value["source"].(map[string]any)
						if source["type"] == "base64" && source["media_type"] == "image/png" && source["data"] == encoded {
							images++
						}
					}
					if value["type"] == "input_image" && value["image_url"] == "data:image/png;base64,"+encoded {
						images++
					}
					if value["type"] == "image_url" {
						if image, _ := value["image_url"].(map[string]any); image["url"] == "data:image/png;base64,"+encoded {
							images++
						}
					}
					for _, child := range value {
						visit(child)
					}
				}
			}
			visit(body)
			wire, _ := json.Marshal(body)
			if images != 2 || strings.Contains(string(wire), "cannot view image content") {
				t.Fatal("user/tool image bytes lost in protocol projection", images)
			}
		})
	}
}
