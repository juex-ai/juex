package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

func sampleInfos() []session.Info {
	return []session.Info{
		{ID: "20260506T103500-aaaa1111", Preview: "summarise README", LastActiveAt: time.Now()},
		{ID: "20260505T194212-bbbb2222", Preview: "refactor session loader", LastActiveAt: time.Now().Add(-time.Hour)},
	}
}

func TestPickSession_ValidNumberReturnsID(t *testing.T) {
	in := strings.NewReader("1\n")
	var out bytes.Buffer
	id, err := pickSession(in, &out, sampleInfos())
	if err != nil {
		t.Fatal(err)
	}
	if id != "20260506T103500-aaaa1111" {
		t.Errorf("id = %s", id)
	}
	if !strings.Contains(out.String(), "summarise README") {
		t.Errorf("expected preview in prompt, got: %s", out.String())
	}
}

func TestPickSession_QuitReturnsCancelled(t *testing.T) {
	in := strings.NewReader("q\n")
	var out bytes.Buffer
	_, err := pickSession(in, &out, sampleInfos())
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestPickSession_RepromptsThenCancels(t *testing.T) {
	in := strings.NewReader("99\nabc\n0\n")
	var out bytes.Buffer
	_, err := pickSession(in, &out, sampleInfos())
	if err == nil {
		t.Fatal("expected cancellation after retries")
	}
	if strings.Count(out.String(), "Enter ") < 2 {
		t.Errorf("expected at least 2 reprompts, got: %s", out.String())
	}
}

func TestPickSession_EmptyListErrors(t *testing.T) {
	in := strings.NewReader("1\n")
	var out bytes.Buffer
	_, err := pickSession(in, &out, nil)
	if err == nil {
		t.Fatal("expected error for empty list")
	}
}
