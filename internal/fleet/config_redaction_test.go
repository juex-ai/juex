package fleet

import (
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestAgentConfigRedactionRejectsDuplicateEnvironmentMappings(t *testing.T) {
	tests := map[string]string{
		"environment": `environment:
  variables:
    FIRST: first-secret
environment:
  variables:
    SECOND: second-secret
`,
		"variables": `environment:
  variables:
    FIRST: first-secret
  variables:
    SECOND: second-secret
`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := RedactAgentConfig(AgentConfig{Exists: true, Content: content})
			if err == nil || !strings.Contains(err.Error(), `duplicate "`+name+`" mapping key`) {
				t.Fatalf("err = %v, want duplicate %s error", err, name)
			}
		})
	}
}

func TestAgentConfigRedactionAcceptsNullEnvironmentVariables(t *testing.T) {
	tests := map[string]string{
		"explicit": "environment:\n  variables: null\n",
		"implicit": "environment:\n  variables:\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			state := AgentConfig{Exists: true, Content: content}
			redacted, err := RedactAgentConfig(state)
			if err != nil {
				t.Fatal(err)
			}
			if redacted.Content != content {
				t.Fatalf("redacted content = %q, want unchanged %q", redacted.Content, content)
			}

			merged, err := mergeRedactedEnvironmentValues([]byte(content), state)
			if err != nil {
				t.Fatal(err)
			}
			if string(merged) != content {
				t.Fatalf("merged content = %q, want unchanged %q", merged, content)
			}
		})
	}
}

func TestAgentConfigEnvironmentMergeRoundTrip(t *testing.T) {
	current := AgentConfig{
		Exists: true,
		Content: `environment:
  variables:
    <<:
      SHARED_TOKEN: shared-secret
      EMPTY: ""
    DIRECT_TOKEN: direct-secret
`,
	}
	redacted, err := RedactAgentConfig(current)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"shared-secret", "direct-secret"} {
		if strings.Contains(redacted.Content, secret) {
			t.Fatalf("redacted merge config leaked %q:\n%s", secret, redacted.Content)
		}
	}
	if !strings.Contains(redacted.Content, "<<:") {
		t.Fatalf("redacted config lost YAML merge mapping:\n%s", redacted.Content)
	}

	merged, err := mergeRedactedEnvironmentValues([]byte(redacted.Content), current)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Environment struct {
			Variables map[string]string `yaml:"variables"`
		} `yaml:"environment"`
	}
	if err := yaml.Unmarshal(merged, &decoded); err != nil {
		t.Fatalf("merged config is invalid YAML: %v\n%s", err, merged)
	}
	want := map[string]string{
		"SHARED_TOKEN": "shared-secret",
		"EMPTY":        "",
		"DIRECT_TOKEN": "direct-secret",
	}
	for key, value := range want {
		if decoded.Environment.Variables[key] != value {
			t.Fatalf("merged variable %s = %q, want %q\n%s", key, decoded.Environment.Variables[key], value, merged)
		}
	}
}

func TestAgentConfigEnvironmentMergeSequenceRoundTrip(t *testing.T) {
	current := AgentConfig{
		Exists: true,
		Content: `environment:
  variables:
    <<:
      - FIRST_TOKEN: first-secret
      - SECOND_TOKEN: second-secret
    DIRECT_TOKEN: direct-secret
`,
	}
	redacted, err := RedactAgentConfig(current)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"first-secret", "second-secret", "direct-secret"} {
		if strings.Contains(redacted.Content, secret) {
			t.Fatalf("redacted merge sequence leaked %q:\n%s", secret, redacted.Content)
		}
	}
	merged, err := mergeRedactedEnvironmentValues([]byte(redacted.Content), current)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Environment struct {
			Variables map[string]string `yaml:"variables"`
		} `yaml:"environment"`
	}
	if err := yaml.Unmarshal(merged, &decoded); err != nil {
		t.Fatalf("merged config is invalid YAML: %v\n%s", err, merged)
	}
	want := map[string]string{
		"FIRST_TOKEN":  "first-secret",
		"SECOND_TOKEN": "second-secret",
		"DIRECT_TOKEN": "direct-secret",
	}
	for key, value := range want {
		if decoded.Environment.Variables[key] != value {
			t.Fatalf("merged variable %s = %q, want %q\n%s", key, decoded.Environment.Variables[key], value, merged)
		}
	}
}

func TestAgentConfigMultiDocumentRoundTripPreservesEveryDocument(t *testing.T) {
	current := AgentConfig{
		Exists: true,
		Content: `environment:
  variables:
    SECRET_TOKEN: first-document-secret
---
observables:
  second-document-marker: preserved
---
third-document-marker:
  nested: preserved
`,
	}
	redacted, err := RedactAgentConfig(current)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted.Content, "first-document-secret") {
		t.Fatalf("redacted multi-document config leaked secret:\n%s", redacted.Content)
	}
	for _, marker := range []string{"second-document-marker", "third-document-marker"} {
		if !strings.Contains(redacted.Content, marker) {
			t.Fatalf("redacted multi-document config lost %q:\n%s", marker, redacted.Content)
		}
	}
	if got := decodeYAMLDocumentCount(t, []byte(redacted.Content)); got != 3 {
		t.Fatalf("redacted YAML document count = %d, want 3\n%s", got, redacted.Content)
	}

	merged, err := mergeRedactedEnvironmentValues([]byte(redacted.Content), current)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "first-document-secret") {
		t.Fatalf("merged multi-document config did not restore secret:\n%s", merged)
	}
	for _, marker := range []string{"second-document-marker", "third-document-marker"} {
		if !strings.Contains(string(merged), marker) {
			t.Fatalf("merged multi-document config lost %q:\n%s", marker, merged)
		}
	}
	if got := decodeYAMLDocumentCount(t, merged); got != 3 {
		t.Fatalf("merged YAML document count = %d, want 3\n%s", got, merged)
	}
}

func decodeYAMLDocumentCount(t *testing.T, data []byte) int {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	count := 0
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err != nil {
			if err == io.EOF {
				return count
			}
			t.Fatalf("decode YAML document %d: %v", count+1, err)
		}
		count++
	}
}
