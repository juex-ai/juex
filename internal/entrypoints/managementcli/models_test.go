package managementcli

import (
	"errors"
	"io"
	"testing"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/spf13/cobra"
)

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
