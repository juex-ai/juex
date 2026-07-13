package workmem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNotesStoreUpdatesSnapshotsAndIgnoresLegacyWorkingState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "working_state.json"), []byte(`{invalid`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewNotesStore(dir)
	if snapshot, err := store.StatusSnapshot(); err != nil || snapshot != nil {
		t.Fatalf("initial snapshot = %+v, err = %v", snapshot, err)
	}

	content := "- [x] inspect code\n- [ ] run tests\napi_key=secret"
	snapshot, err := store.Update(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot.Content, "- [ ] run tests") || strings.Contains(snapshot.Content, "secret") || !strings.Contains(snapshot.Content, "[REDACTED]") {
		t.Fatalf("snapshot content = %q", snapshot.Content)
	}
	if snapshot.UpdatedAt.IsZero() {
		t.Fatal("updated_at is zero")
	}

	data, err := os.ReadFile(filepath.Join(dir, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != snapshot.Content {
		t.Fatalf("notes.md = %q, want %q", data, snapshot.Content)
	}
	loaded, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Content != snapshot.Content || loaded.UpdatedAt.IsZero() {
		t.Fatalf("loaded snapshot = %+v", loaded)
	}
	status, err := store.StatusSnapshot()
	if err != nil || status == nil || status.Content != snapshot.Content {
		t.Fatalf("status snapshot = %+v, err = %v", status, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".notes.md-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files remain: %v", temps)
	}
}

func TestNotesStoreRejectsOversizedContentWithoutReplacingExisting(t *testing.T) {
	dir := t.TempDir()
	store := NewNotesStore(dir)
	valid := strings.Repeat("界", MaxNotesCharacters)
	if utf8.RuneCountInString(valid) != MaxNotesCharacters {
		t.Fatal("test input has wrong character count")
	}
	if _, err := store.Update(valid); err != nil {
		t.Fatalf("maximum-sized notes rejected: %v", err)
	}

	tooLong := valid + "界"
	if _, err := store.Update(tooLong); err == nil || !strings.Contains(err.Error(), "maximum is 2048") || !strings.Contains(err.Error(), "scratchpad") {
		t.Fatalf("oversize error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != valid {
		t.Fatalf("rejected update replaced notes: chars=%d", utf8.RuneCount(data))
	}
}

func TestNotesStoreRejectsInvalidUTF8(t *testing.T) {
	store := NewNotesStore(t.TempDir())
	if _, err := store.Update(string([]byte{0xff})); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
}

func TestNotesStoreRedactsBearerAssignmentValues(t *testing.T) {
	dir := t.TempDir()
	store := NewNotesStore(dir)
	snapshot, err := store.Update("authorization: Bearer abc123\ntoken: Bearer xyz789")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"abc123", "xyz789"} {
		if strings.Contains(snapshot.Content, secret) {
			t.Fatalf("snapshot leaked %q: %q", secret, snapshot.Content)
		}
	}
	persisted, err := os.ReadFile(filepath.Join(dir, NotesFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"abc123", "xyz789"} {
		if strings.Contains(string(persisted), secret) {
			t.Fatalf("notes.md leaked %q: %q", secret, persisted)
		}
	}
}

func TestNotesSnapshotRendersProviderContext(t *testing.T) {
	if rendered, ok := (NotesSnapshot{}).RenderProviderContext(); ok || rendered != "" {
		t.Fatalf("empty notes rendered as %q", rendered)
	}
	snapshot := NotesSnapshot{Content: "- [x] inspect\n- [ ] verify"}
	rendered, ok := snapshot.RenderProviderContext()
	if !ok || !strings.Contains(rendered, "Current working notes") || !strings.Contains(rendered, "rewrite with update_notes") || !strings.Contains(rendered, "- [ ] verify") {
		t.Fatalf("provider context = %q", rendered)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("界界界", 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate returned invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "界...(truncated") {
		t.Fatalf("truncate returned %q", got)
	}
}
