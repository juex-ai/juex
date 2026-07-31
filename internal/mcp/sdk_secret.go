package mcp

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

func headerCredentialValues(headers map[string]Credential) []string {
	var values []string
	for _, name := range sortedCredentialNames(headers) {
		value := headers[name].Value()
		values = appendSecret(values, value)
		if strings.EqualFold(name, "Authorization") {
			scheme, token, ok := strings.Cut(value, " ")
			if ok && strings.EqualFold(scheme, "Bearer") {
				values = appendSecret(values, strings.TrimSpace(token))
			}
		}
	}
	return values
}

func appendSecret(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func secretRedactionValues(values []string) []string {
	secrets := make([]string, 0, len(values)*2)
	for _, value := range values {
		secrets = appendSecret(secrets, value)
		encoded, err := json.Marshal(value)
		if err == nil && len(encoded) >= 2 {
			secrets = appendSecret(secrets, string(encoded[1:len(encoded)-1]))
		}
		secrets = appendSecret(secrets, url.QueryEscape(value))
	}
	sort.Slice(secrets, func(i, j int) bool {
		if len(secrets[i]) == len(secrets[j]) {
			return secrets[i] < secrets[j]
		}
		return len(secrets[i]) > len(secrets[j])
	})
	return secrets
}

func redactSecretValues(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
