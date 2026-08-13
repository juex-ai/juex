//go:build windows

package session

import (
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestCachedInfoUsesContentValidatedCheckpointSummary(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "canonical"), "m1")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	id := s.ID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := fingerprintFromPath(filepath.Join(dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}

	info, scanned, err := cachedOrScannedInfo(dir, id, map[string]Info{
		id: {ID: id, Turns: 99, Preview: "stale", transcript: fingerprint},
	}, loadInfoSummary)
	if err != nil {
		t.Fatal(err)
	}
	if scanned {
		t.Fatal("content-valid checkpoint forced a strict transcript scan")
	}
	if info.Turns != 1 || info.Preview != "canonical" {
		t.Fatalf("summary = %d/%q, want checkpoint 1/canonical", info.Turns, info.Preview)
	}
}
