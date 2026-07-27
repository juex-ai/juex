package environment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotenvParsesDataWithoutShellEvaluation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := strings.Join([]string{
		"# comment",
		"PLAIN=value",
		"EMPTY=",
		"SPACED = trimmed value",
		`SINGLE='literal $HOME # value'`,
		`DOUBLE="line\nvalue"`,
		`NO_EXEC=$(touch /tmp/should-not-run)`,
		"export EXPORTED=value",
		"exportX=ordinary-key",
		"INLINE=value # comment",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := LoadDotenv(path, LoadDotenvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Loaded || result.Path != path {
		t.Fatalf("result = %+v", result)
	}
	want := map[string]string{
		"PLAIN":    "value",
		"EMPTY":    "",
		"SPACED":   "trimmed value",
		"SINGLE":   "literal $HOME # value",
		"DOUBLE":   "line\nvalue",
		"NO_EXEC":  "$(touch /tmp/should-not-run)",
		"EXPORTED": "value",
		"exportX":  "ordinary-key",
		"INLINE":   "value",
	}
	if got := result.Values; !reflectStringMapEqual(got, want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
}

func TestLoadDotenvMissingIsAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	result, err := LoadDotenv(path, LoadDotenvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Loaded || result.Path != path || len(result.Values) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestLoadDotenvRejectsMalformedDuplicateReservedAndNUL(t *testing.T) {
	tests := []struct {
		name string
		body string
		opts LoadDotenvOptions
		want string
	}{
		{name: "malformed", body: "NOT_AN_ASSIGNMENT\n", want: "line 1"},
		{name: "duplicate", body: "KEY=one\nKEY=two\n", want: "duplicate"},
		{name: "windows case conflict", body: "Key=one\nKEY=two\n", opts: LoadDotenvOptions{GOOS: "windows"}, want: "case-conflicting"},
		{name: "reserved", body: "WORKDIR=/other\n", want: "reserved"},
		{name: "nul", body: "KEY=bad\x00value\n", want: "NUL"},
		{name: "unterminated quote", body: `KEY="bad` + "\n", want: "unterminated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadDotenv(path, tc.opts)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want path and %q", err, tc.want)
			}
		})
	}
}

func reflectStringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, want := range b {
		if got, ok := a[key]; !ok || got != want {
			return false
		}
	}
	return true
}
