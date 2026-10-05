package managementcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateModelOptions(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		mode          os.FileMode
		valid         bool
	}{
		{"private", `{"thinking_effort":"xhigh","headers":{"X-Account":"private-value"},"capabilities":{"Vision":true}}`, 0600, true},
		{"shared", `{}`, 0644, false},
		{"unknown", `{"private-value":"never print this"}`, 0600, false},
		{"null", `null`, 0600, false},
		{"trailing", `{} {}`, 0600, false},
		{"oversize", strings.Repeat(" ", (64<<10)+1), 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "options.json")
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			options, err := readModelOptions(path)
			if (err == nil) != tc.valid {
				t.Fatal("unexpected options validation", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-value") {
				t.Fatal("private file content leaked in error")
			}
			if tc.valid && (options.ThinkingEffort != "xhigh" || options.Headers["X-Account"] != "private-value" || options.Capabilities.Vision == nil || !*options.Capabilities.Vision) {
				t.Fatal("profile option was dropped")
			}
		})
	}
}
