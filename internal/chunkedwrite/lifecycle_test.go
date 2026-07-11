package chunkedwrite

import "testing"

func TestBuildStatesDerivesCommittedLifecycle(t *testing.T) {
	states := BuildStates([]Event{
		{Kind: EventBegin, WriteID: "w_demo", Path: "reports/long.md", Mode: "create", FileMode: 0o644},
		{Kind: EventChunk, WriteID: "w_demo", Index: 0, Bytes: 6, Chars: 6, SHA256: "chunk-a", Chunks: 1},
		{Kind: EventChunk, WriteID: "w_demo", Index: 1, Bytes: 5, Chars: 5, SHA256: "chunk-b", Chunks: 2},
		{Kind: EventCommit, WriteID: "w_demo", Path: "reports/long.md", Bytes: 11, Chars: 11, SHA256: "full", Chunks: 2},
	})

	state, ok := states["w_demo"]
	if !ok {
		t.Fatal("missing state for w_demo")
	}
	if state.Status != StatusCommitted {
		t.Fatalf("status = %q, want committed", state.Status)
	}
	if state.Path != "reports/long.md" || state.Mode != "create" || state.FileMode != 0o644 {
		t.Fatalf("state metadata = %+v", state)
	}
	if len(state.Chunks) != 2 || state.Chunks[1].Index != 1 || state.Chunks[1].Bytes != 5 {
		t.Fatalf("chunks = %+v", state.Chunks)
	}
	if state.Commit == nil || state.Commit.SHA256 != "full" || state.Commit.Chunks != 2 {
		t.Fatalf("commit = %+v", state.Commit)
	}
}

func TestBuildStatesLeavesUnresolvedActive(t *testing.T) {
	states := BuildStates([]Event{
		{Kind: EventBegin, WriteID: "w_live", Path: "drafts/live.md", Mode: "overwrite"},
		{Kind: EventChunk, WriteID: "w_live", Index: 0, Bytes: 5, Chars: 5, SHA256: "chunk-a", Chunks: 1},
	})

	state := states["w_live"]
	if state.Status != StatusActive {
		t.Fatalf("status = %q, want active", state.Status)
	}
	if len(state.Chunks) != 1 || state.Chunks[0].Index != 0 {
		t.Fatalf("chunks = %+v", state.Chunks)
	}
}
