package sandbox

import (
	"path/filepath"
	"strings"
)

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
	return replaceEnvironmentValue(env, "GOCACHE", filepath.Join(cache, "go-build"))
}
