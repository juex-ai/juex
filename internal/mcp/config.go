package mcp

import (
	"path/filepath"
	"strings"
)

const (
	workDirEnvKey     = "WORKDIR"
	juexWorkDirEnvKey = "JUEX_WORKDIR"
)

// PrepareConfig returns a runtime-ready copy of cfg for a specific Juex work
// directory. It injects workdir env defaults and expands those variables in
// command, args, and env values before MCP subprocesses are launched.
func PrepareConfig(cfg Config, workDir string) Config {
	if len(cfg.MCPServers) == 0 {
		return Config{}
	}
	runtimeEnv := RuntimeEnv(workDir)
	out := Config{MCPServers: make(map[string]ServerSpec, len(cfg.MCPServers))}
	for name, spec := range cfg.MCPServers {
		prepared := ServerSpec{
			Command: expandRuntimeEnvRefs(spec.Command, runtimeEnv),
			Args:    make([]string, len(spec.Args)),
			Env:     make(map[string]string, len(spec.Env)+len(runtimeEnv)),
		}
		for i, arg := range spec.Args {
			prepared.Args[i] = expandRuntimeEnvRefs(arg, runtimeEnv)
		}
		for k, v := range runtimeEnv {
			prepared.Env[k] = v
		}
		for k, v := range spec.Env {
			prepared.Env[k] = expandRuntimeEnvRefs(v, runtimeEnv)
		}
		out.MCPServers[name] = prepared
	}
	return out
}

// RuntimeEnv returns the environment variables Juex injects into MCP servers.
func RuntimeEnv(workDir string) map[string]string {
	absWorkDir := workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		absWorkDir = abs
	}
	return map[string]string{
		workDirEnvKey:     absWorkDir,
		juexWorkDirEnvKey: absWorkDir,
	}
}

func expandRuntimeEnvRefs(s string, env map[string]string) string {
	for _, key := range []string{juexWorkDirEnvKey, workDirEnvKey} {
		value := env[key]
		s = strings.ReplaceAll(s, "${"+key+"}", value)
		s = replaceUnbracedEnvRef(s, key, value)
	}
	return s
}

func replaceUnbracedEnvRef(s, key, value string) string {
	needle := "$" + key
	if !strings.Contains(s, needle) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	start := 0
	for {
		idx := strings.Index(s[start:], needle)
		if idx < 0 {
			b.WriteString(s[start:])
			break
		}
		idx += start
		end := idx + len(needle)
		if end < len(s) && isEnvNameByte(s[end]) {
			b.WriteString(s[start:end])
			start = end
			continue
		}
		b.WriteString(s[start:idx])
		b.WriteString(value)
		start = end
	}
	return b.String()
}

func isEnvNameByte(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
