package fleet

import (
	"strings"
	"testing"
)

func TestAgentConfigEnvironmentRedactionAndPlaceholderMerge(t *testing.T) {
	const secret = "fleet-config-secret-sentinel"
	current := AgentConfig{
		Path:   "/work/.juex/juex.yaml",
		Exists: true,
		Content: `model: local:test
environment:
  load_dotenv: true
  variables:
    SECRET_TOKEN: fleet-config-secret-sentinel
    EMPTY: ""
`,
	}
	redacted, err := RedactAgentConfig(current)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted.Content, secret) {
		t.Fatalf("redacted config leaked value:\n%s", redacted.Content)
	}
	if strings.Count(redacted.Content, redactedEnvironmentValue) != 2 {
		t.Fatalf("redacted config =\n%s", redacted.Content)
	}

	merged, err := mergeRedactedEnvironmentValues([]byte(redacted.Content), current)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), secret) || !strings.Contains(string(merged), `EMPTY: ""`) {
		t.Fatalf("merged config did not restore existing values:\n%s", merged)
	}
}

func TestAgentConfigRedactedPlaceholderCannotCreateUnknownValue(t *testing.T) {
	current := AgentConfig{
		Exists:  true,
		Content: "environment:\n  variables:\n    EXISTING: value\n",
	}
	_, err := mergeRedactedEnvironmentValues(
		[]byte("environment:\n  variables:\n    NEW_SECRET: \"[REDACTED_ENV]\"\n"),
		current,
	)
	if err == nil || !strings.Contains(err.Error(), "NEW_SECRET") {
		t.Fatalf("err = %v, want unknown placeholder error", err)
	}
}
