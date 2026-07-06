package errorclass

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/juex-ai/juex/internal/cancellation"
)

func TestClassifyTimeoutErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "sentinel", err: context.DeadlineExceeded},
		{name: "wrapped", err: fmt.Errorf("openai codex responses: codex SSE read: %w", context.DeadlineExceeded)},
		{name: "provider text", err: errors.New("provider returned deadline_exceeded")},
		{name: "handshake timeout", err: errors.New("net/http: TLS handshake timeout")},
		{name: "read deadline", err: errors.New("net/http: read deadline exceeded")},
		{name: "write deadline", err: errors.New("net/http: write deadline exceeded")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err)
			if got.Kind != KindTimeout || !got.TimedOut {
				t.Fatalf("Classify(%v) = %+v, want timeout", tt.err, got)
			}
			if got.RawCause == "" {
				t.Fatal("RawCause = empty, want original error text")
			}
			public := PublicMessage(tt.err, MessageOptions{})
			if !strings.Contains(public, "timed out") {
				t.Fatalf("PublicMessage = %q, want timed out", public)
			}
			for _, forbidden := range []string{"context deadline exceeded", "deadline_exceeded"} {
				if strings.Contains(public, forbidden) {
					t.Fatalf("PublicMessage = %q, should not expose %q", public, forbidden)
				}
			}
		})
	}
}

func TestPublicMessageTimeoutWithSubjectAndSeconds(t *testing.T) {
	err := fmt.Errorf("tools: slow: %w", context.DeadlineExceeded)
	got := PublicMessage(err, MessageOptions{Subject: "tools: slow", TimeoutSeconds: 2})
	if got != "tools: slow timed out after 2s" {
		t.Fatalf("PublicMessage = %q, want tools: slow timed out after 2s", got)
	}
}

func TestPublicMessagePreservesExistingToolTimeout(t *testing.T) {
	err := errors.New("tools: slow timed out after 1s")
	got := PublicMessage(err, MessageOptions{})
	if got != err.Error() {
		t.Fatalf("PublicMessage = %q, want existing timeout text", got)
	}
}

func TestClassifyCancellationIsNotTimeout(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", context.Canceled)
	got := Classify(err)
	if got.Kind != KindCancelled || got.TimedOut {
		t.Fatalf("Classify(context.Canceled) = %+v, want cancelled", got)
	}
	if msg := PublicMessage(err, MessageOptions{}); msg != cancellation.ErrUserCancelled.Error() {
		t.Fatalf("PublicMessage = %q, want normalized cancellation", msg)
	}
}

func TestClassifySignalCancellation(t *testing.T) {
	err := cancellation.NewSignalError(syscall.SIGTERM)
	got := Classify(err)
	if got.Kind != KindTerminated || got.TimedOut {
		t.Fatalf("Classify(signal) = %+v, want terminated", got)
	}
	if msg := PublicMessage(err, MessageOptions{}); msg != "run terminated by signal SIGTERM (15)" {
		t.Fatalf("PublicMessage = %q", msg)
	}
	if strings.Contains(PublicMessage(err, MessageOptions{}), "by user") {
		t.Fatalf("signal cancellation should not blame user: %q", PublicMessage(err, MessageOptions{}))
	}

	interruptErr := cancellation.NewSignalError(syscall.SIGINT)
	if got := Classify(interruptErr); got.Kind != KindInterrupted {
		t.Fatalf("Classify(SIGINT) = %+v, want interrupted", got)
	}
}

func TestClassifyPermissionAndAuth(t *testing.T) {
	if got := ClassifyText("open /root/secret: permission denied"); got.Kind != KindPermission {
		t.Fatalf("permission kind = %q", got.Kind)
	}
	if got := ClassifyText("provider unauthorized"); got.Kind != KindAuth {
		t.Fatalf("auth kind = %q", got.Kind)
	}
}
