package goal

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("界界界", 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate returned invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "界...(truncated") {
		t.Fatalf("truncate returned %q", got)
	}
}
