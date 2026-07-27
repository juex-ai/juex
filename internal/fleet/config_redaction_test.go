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

func TestAgentConfigLiteralPlaceholderTagWritesExactValue(t *testing.T) {
	current := AgentConfig{
		Exists:  true,
		Content: "environment:\n  variables:\n    EXISTING: old-value\n",
	}
	merged, err := mergeRedactedEnvironmentValues(
		[]byte(`environment:
  variables:
    EXISTING: "[REDACTED_ENV]"
    LITERAL: !juex/literal "[REDACTED_ENV]"
`),
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	body := string(merged)
	if !strings.Contains(body, "EXISTING: old-value") {
		t.Fatalf("existing placeholder was not merged:\n%s", body)
	}
	if !strings.Contains(body, `LITERAL: "[REDACTED_ENV]"`) {
		t.Fatalf("literal placeholder was not written:\n%s", body)
	}
	if strings.Contains(body, literalEnvironmentValueYAMLTag) {
		t.Fatalf("literal control tag was persisted:\n%s", body)
	}
}

func TestAgentConfigLiteralPlaceholderTagRejectsOtherValues(t *testing.T) {
	_, err := mergeRedactedEnvironmentValues(
		[]byte("environment:\n  variables:\n    BAD: !juex/literal other-value\n"),
		AgentConfig{},
	)
	if err == nil || !strings.Contains(err.Error(), literalEnvironmentValueYAMLTag) {
		t.Fatalf("err = %v, want invalid literal-tag error", err)
	}
}
