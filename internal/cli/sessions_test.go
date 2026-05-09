package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedSession writes a session dir under <work>/.agents/sessions/<id>/.
func seedSession(t *testing.T, work, id string, jsonlBody string) string {
	t.Helper()
	dir := filepath.Join(work, ".agents", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(jsonlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSessionsList_JSONShape(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Turns   int    `json:"turns"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("len = %d", len(parsed.Sessions))
	}
	if parsed.Sessions[0].ID != "20260506T103500-aaaa1111" {
		t.Errorf("id = %s", parsed.Sessions[0].ID)
	}
	if parsed.Sessions[0].Turns != 1 {
		t.Errorf("turns = %d", parsed.Sessions[0].Turns)
	}
	if parsed.Sessions[0].Preview != "hi" {
		t.Errorf("preview = %q", parsed.Sessions[0].Preview)
	}
}

func TestSessionsList_EmptyReturnsEmptyArray(t *testing.T) {
	work := t.TempDir()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"sessions":`) {
		t.Errorf("missing sessions key: %s", out.String())
	}
}

func TestSessionsList_TableFormat(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list", "--format", "table"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"ID", "TURNS", "PREVIEW", "20260506T103500-aaaa1111"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in table:\n%s", want, body2)
		}
	}
}

func TestSessionsList_LimitTruncates(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	for _, id := range []string{
		"20260506T103500-aaaa1111",
		"20260505T103500-bbbb2222",
		"20260504T103500-cccc3333",
	} {
		seedSession(t, work, id, body)
	}
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list", "--limit", "2"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 2 {
		t.Errorf("limit ignored: %d sessions", len(parsed.Sessions))
	}
}

func TestSessionsShow_JSONIncludesMessages(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0001", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0001"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ID       string `json:"id"`
		Turns    int    `json:"turns"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if parsed.ID != "20260506T103500-show0001" {
		t.Errorf("id = %s", parsed.ID)
	}
	if len(parsed.Messages) != 2 {
		t.Errorf("messages len = %d", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" || parsed.Messages[1].Role != "assistant" {
		t.Errorf("roles wrong: %+v", parsed.Messages)
	}
}

func TestSessionsShow_TextRendersTranscript(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0002", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0002", "--format", "text"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"20260506T103500-show0002", "user>", "hi", "assistant>", "hello"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in:\n%s", want, body2)
		}
	}
}

func TestSessionsShow_TextRendersReasoning(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"assistant","blocks":[{"type":"reasoning","text":"step one"},{"type":"text","text":"answer"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"reasoning","content":"x","redacted":true}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0003", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0003", "--format", "text"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"thinking> step one", "thinking> [redacted]", "assistant> answer"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in:\n%s", want, body2)
		}
	}
}

func TestSessionsShow_NotFound(t *testing.T) {
	work := t.TempDir()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "missing-id"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("expected *notFoundError, got %T: %v", err, err)
	}
}
