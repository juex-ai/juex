package managementcli

import (
	"errors"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/spf13/cobra"
	"io"
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

func TestModelLimitsValidatedBeforeOpeningServices(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"defaults", nil, true},
		{"positive omitted reserve", []string{"--max-output=1024"}, true},
		{"provider default", []string{"--max-output=0", "--output-reserve=8192"}, true},
		{"independent reserve", []string{"--max-output=1024", "--output-reserve=8192"}, true},
		{"missing reserve", []string{"--max-output=0"}, false},
		{"explicit zero reserve", []string{"--output-reserve=0"}, false},
		{"negative cap", []string{"--max-output=-1", "--output-reserve=8192"}, false},
		{"cap exceeds reserve", []string{"--max-output=2048", "--output-reserve=1024"}, false},
		{"reserve fills context", []string{"--output-reserve=32768"}, false},
		{"Anthropic default fits", []string{"--protocol=anthropic/messages", "--max-output=0", "--output-reserve=4096"}, true},
		{"Anthropic default exceeds reserve", []string{"--protocol=anthropic/messages", "--max-output=0", "--output-reserve=512"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := false
			unavailable := errors.New("test service unavailable")
			cmd := modelCommand(func(*cobra.Command) (*managed.Management, error) { opened = true; return nil, unavailable }, io.Discard)
			cmd.SetArgs(append([]string{"put"}, tc.args...))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if opened != tc.valid || err == nil || tc.valid && !errors.Is(err, unavailable) {
				t.Fatal("unexpected validation or service access", opened, err)
			}
		})
	}
}
