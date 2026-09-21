package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestLiveBinary_ReadDownsampledImageReachesProviderAfterRestart(t *testing.T) {
	bin := buildJuex(t)
	var requestCount atomic.Int32
	requests := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		select {
		case requests <- body:
		default:
			t.Error("unexpected extra provider request")
		}
		w.Header().Set("Content-Type", "application/json")
		output := []any{map[string]any{
			"type": "message", "id": "msg_image", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "image-received", "annotations": []any{}}},
		}}
		if requestCount.Add(1) == 1 {
			output = []any{map[string]any{
				"type": "function_call", "id": "fc_read", "call_id": "call_read", "name": "read",
				"arguments": `{"path":"wide.png"}`, "status": "completed",
			}}
		}
		writeJSON(t, w, map[string]any{
			"id": "resp_image", "object": "response", "model": "vision-test", "status": "completed", "output": output,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	work, home := t.TempDir(), t.TempDir()
	configBody := fmt.Sprintf(`models: [local:vision-test]
providers:
  - id: local
    protocol: openai/responses
    base_url: %s
    api_key: test-key
    capabilities:
      vision: true
      streaming: false
    models:
      - id: vision-test
`, srv.URL)
	if err := writeText(filepath.Join(work, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}
	source := image.NewRGBA(image.Rect(0, 0, 2101, 3))
	for x := range source.Bounds().Dx() {
		source.SetRGBA(x, 0, color.RGBA{R: uint8(x % 251), A: 255})
	}
	var original bytes.Buffer
	if err := png.Encode(&original, source); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(work, "wide.png")
	if err := os.WriteFile(sourcePath, original.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	first := sendAndWait(t, bin, home, work, "read wide.png")
	var media *llm.MediaRef
	for _, message := range readThreadMessages(t, first.ThreadDir) {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult && block.ToolName == "read" {
				media = block.Media
			}
		}
	}
	if media == nil || media.Width != 2000 || media.OriginalBytes != original.Len() {
		t.Fatalf("downsampled read reference = %+v", media)
	}
	stored, err := os.ReadFile(filepath.Join(home, "agents", first.AgentID, "media", filepath.FromSlash(media.ArtifactPath)))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) == original.Len() {
		t.Fatal("fixture must have different source and stored sizes")
	}
	unchanged, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(unchanged, original.Bytes()) {
		t.Fatalf("read changed source image: %v", err)
	}
	stopLiveAgent(t, bin, home, work)
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	second := sendAndWait(t, bin, home, work, "describe the previously read image")
	if second.ThreadDir != first.ThreadDir || requestCount.Load() != 3 {
		t.Fatalf("replay changed Thread or request count: first=%+v second=%+v requests=%d", first, second, requestCount.Load())
	}
	<-requests // Initial prompt before read has no image.
	for _, phase := range []string{"read continuation", "restart replay"} {
		body := string(<-requests)
		var request struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(stored)
		if !strings.Contains(body, want) || !strings.Contains(body, `"type":"input_image"`) || !strings.Contains(body, "downsampled from") {
			t.Errorf("%s omitted the stored tool image: %s", phase, request.Input)
		}
		if strings.Contains(body, "cannot view image content") {
			t.Errorf("%s included unavailable-image guidance", phase)
		}
	}
}
