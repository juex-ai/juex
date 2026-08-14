package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

func prepareSandboxScratchDir(agentStateDir string) (string, error) {
	if strings.TrimSpace(agentStateDir) == "" {
		return "", nil
	}
	scratch := filepath.Join(agentStateDir, "tmp")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return "", err
	}
	return scratch, nil
}

func replaceEnvironmentValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		out = append(out, item)
	}
	return append(out, prefix+value)
}

func sandboxScratchEnvironment(env []string, scratch string) []string {
	cache := filepath.Join(scratch, "cache")
	env = replaceEnvironmentValue(env, "TMPDIR", scratch)
	env = replaceEnvironmentValue(env, "XDG_CACHE_HOME", cache)
	env = replaceEnvironmentValue(env, "GOCACHE", filepath.Join(cache, "go-build"))
	return replaceEnvironmentValue(env, "GOMODCACHE", filepath.Join(cache, "go-mod"))
}
