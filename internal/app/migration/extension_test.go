package migration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func stdioSnapshot(manifest, servers string) legacy.ExtensionSnapshot {
	s := legacy.ExtensionSnapshot{Directory: "/source/wire", Selection: legacy.ExtensionResources{MCP: true, Hooks: true, Observables: true, Skills: true}, AbsentFiles: []string{"hooks.yaml", "observables.json", "skills"}}
	for name, data := range map[string]string{"juex.extension.json": manifest, "mcp.json": servers} {
		s.Files = append(s.Files, legacy.SourceFile{Path: name, Data: []byte(data), Size: int64(len(data)), SHA256: configDigest([]byte(data))})
	}
	return s
}

const stdioManifest = `{"manifest_version":1,"name":"wire","version":"1.2.3","description":"Selected wire"}`
const stdioServers = `{"mcpServers":{"wire":{"command":"wire","args":["mcp","two words","","$HOME","$(touch marker)","${WORKDIR}/a","$WORKDIR_X","$JUEX_WORKDIR/b"],"env":{"STATE":"$JUEX_EXT_DATA_DIR/state","ROOT":"${JUEX_EXT_DIR}","CWD":"$WORKDIR"}}}}`

func TestConvertMCPExtension(t *testing.T) {
	snapshot := stdioSnapshot(stdioManifest, stdioServers)
	before, _ := json.Marshal(snapshot)
	bindings := map[string]MCPProcessBinding{"wire": {Executable: "/target/bin/wire", WorkingDirectory: "/target/process cwd", RuntimeWorkDir: "/target/workspace"}}
	manifest, err := ConvertMCPExtension(snapshot, bindings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestVersion != 2 || manifest.Name != "wire" || manifest.Version != "1.2.3" || manifest.Description != "Selected wire" || manifest.Validate() != nil {
		t.Fatal(manifest)
	}
	if len(manifest.MCP) != 1 {
		t.Fatal(manifest)
	}
	c := manifest.MCP[0]
	if c.ID != "wire" || !reflect.DeepEqual(c.Command[5:], []string{"/target/workspace", "/target/process cwd", "/target/bin/wire", "mcp", "two words", "", "$HOME", "$(touch marker)", "/target/workspace/a", "$WORKDIR_X", "/target/workspace/b"}) {
		t.Fatal(c.Command)
	}
	if !reflect.DeepEqual(c.Environment, map[string]string{"STATE": "${JUEX_EXT_DATA_DIR}/state", "ROOT": "${JUEX_EXT_DIR}", "CWD": "/target/workspace"}) {
		t.Fatal(c.Environment)
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("mutated capture")
	}
	c.Environment["STATE"] = "changed"
	again, err := ConvertMCPExtension(snapshot, bindings, nil)
	if err != nil || again.MCP[0].Environment["STATE"] != "${JUEX_EXT_DATA_DIR}/state" {
		t.Fatal(again, err)
	}
}

func TestConvertMCPExtensionRejectsUnprovedConversion(t *testing.T) {
	tests := map[string]func(*legacy.ExtensionSnapshot, map[string]MCPProcessBinding){
		"disabled MCP":              func(s *legacy.ExtensionSnapshot, _ map[string]MCPProcessBinding) { s.Selection.MCP = false },
		"hash":                      func(s *legacy.ExtensionSnapshot, _ map[string]MCPProcessBinding) { s.Files[0].SHA256 = "bad" },
		"missing resource evidence": func(s *legacy.ExtensionSnapshot, _ map[string]MCPProcessBinding) { s.AbsentFiles = nil },
		"missing binding":           func(_ *legacy.ExtensionSnapshot, b map[string]MCPProcessBinding) { delete(b, "wire") },
		"extra binding":             func(_ *legacy.ExtensionSnapshot, b map[string]MCPProcessBinding) { b["other"] = b["wire"] },
		"relative executable": func(_ *legacy.ExtensionSnapshot, b map[string]MCPProcessBinding) {
			v := b["wire"]
			v.Executable = "wire"
			b["wire"] = v
		},
		"missing cwd": func(_ *legacy.ExtensionSnapshot, b map[string]MCPProcessBinding) {
			v := b["wire"]
			v.WorkingDirectory = ""
			b["wire"] = v
		},
		"missing runtime workdir": func(_ *legacy.ExtensionSnapshot, b map[string]MCPProcessBinding) {
			v := b["wire"]
			v.RuntimeWorkDir = ""
			b["wire"] = v
		},
		"invalid utf8 binding": func(_ *legacy.ExtensionSnapshot, b map[string]MCPProcessBinding) {
			v := b["wire"]
			v.Executable = "/x\xff"
			b["wire"] = v
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s := stdioSnapshot(stdioManifest, stdioServers)
			b := map[string]MCPProcessBinding{"wire": {Executable: "/bin/wire", WorkingDirectory: "/cwd", RuntimeWorkDir: "/work"}}
			change(&s, b)
			if _, err := ConvertMCPExtension(s, b, nil); err == nil {
				t.Fatal("accepted unproved conversion")
			}
		})
	}
}

func TestConvertMCPExtensionSourceValidation(t *testing.T) {
	for name, manifest := range map[string]string{
		"version":          strings.Replace(stdioManifest, "1.2.3", "1.2", 1),
		"name":             strings.Replace(stdioManifest, `"wire"`, `"other"`, 1),
		"duplicate":        `{"manifest_version":1,"name":"wrong","name":"wire","version":"1.2.3"}`,
		"null description": `{"manifest_version":1,"name":"wire","version":"1.2.3","description":null}`,
		"agent defaults":   `{"manifest_version":1,"name":"wire","version":"1.2.3","agent":{"environment":{"variables":{"PATH":"/old"}}}}`,
		"null agent":       `{"manifest_version":1,"name":"wire","version":"1.2.3","agent":null}`,
		"requirements":     `{"manifest_version":1,"name":"wire","version":"1.2.3","requirements":[{"name":"missing fields"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := stdioSnapshot(manifest, stdioServers)
			if _, err := ConvertMCPExtension(s, stdioBindings(), nil); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	for name, server := range map[string]string{
		"http":                  `{"type":"http","url":"https://example.invalid"}`,
		"null type":             `{"type":null,"command":"wire"}`,
		"blank type":            `{"type":"","command":"wire"}`,
		"extra field":           `{"command":"wire","cwd":"/old"}`,
		"headers presence":      `{"command":"wire","headers":null}`,
		"url presence":          `{"command":"wire","url":null}`,
		"argv data dir":         `{"command":"wire","args":["$JUEX_EXT_DATA_DIR/state"]}`,
		"argv install dir":      `{"command":"wire","args":["${JUEX_EXT_DIR}/app"]}`,
		"command placeholder":   `{"command":"${WORKDIR}/wire"}`,
		"unproved reserved env": `{"command":"wire","env":{"JUEX_OTHER":"secret"}}`,
		"nul":                   `{"command":"wire","args":["\u0000"]}`,
		"too many args":         `{"command":"wire","args":[` + strings.Repeat(`"x",`, 56) + `"x"]}`,
		"duplicate command":     `{"command":"ignored","command":"wire"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := stdioSnapshot(stdioManifest, `{"mcpServers":{"wire":`+server+`}}`)
			if _, err := ConvertMCPExtension(s, stdioBindings(), nil); err == nil {
				t.Fatal("accepted unproved server")
			}
		})
	}
}

func stdioBindings() map[string]MCPProcessBinding {
	return map[string]MCPProcessBinding{"wire": {Executable: "/bin/wire", WorkingDirectory: "/cwd", RuntimeWorkDir: "/work"}}
}

func TestConvertMCPExtensionResourceSelection(t *testing.T) {
	s := stdioSnapshot(stdioManifest, stdioServers)
	s.Selection = legacy.ExtensionResources{MCP: true}
	s.AbsentFiles = nil
	if _, err := ConvertMCPExtension(s, stdioBindings(), nil); err != nil {
		t.Fatal(err)
	}
	s.Selection.Skills = true
	s.AbsentFiles = []string{"skills/aux/SKILL.md"}
	if _, err := ConvertMCPExtension(s, stdioBindings(), nil); err == nil {
		t.Fatal("accepted unproved skill directory")
	}
	s.Files = append(s.Files, legacy.SourceFile{Path: "skills/active/SKILL.md", Data: []byte("skill"), Size: 5, SHA256: configDigest([]byte("skill"))})
	if _, err := ConvertMCPExtension(s, stdioBindings(), nil); err == nil {
		t.Fatal("discarded enabled skill")
	}
}

func TestConvertMCPExtensionDeterministicBindingsAndLimits(t *testing.T) {
	servers := `{"mcpServers":{"z":{"command":"wire","type":"stdio"},"a":{"command":"wire","env":{"WORKDIR":"ignored","JUEX_WORKDIR":"ignored","JUEX_EXT_DIR":"ignored","juex_ext_data_dir":"ignored","LITERAL":"$OTHER/$WORKDIR_SUFFIX/$$JUEX_EXT_DIR/${WORKDIR}"}}}}`
	b := stdioBindings()["wire"]
	manifest, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, servers), map[string]MCPProcessBinding{"z": b, "a": b}, nil)
	if err != nil || len(manifest.MCP) != 2 || manifest.MCP[0].ID != "a" || manifest.MCP[1].ID != "z" {
		t.Fatal(manifest, err)
	}
	if !reflect.DeepEqual(manifest.MCP[0].Environment, map[string]string{"LITERAL": "$OTHER/$WORKDIR_SUFFIX/$${JUEX_EXT_DIR}//work"}) {
		t.Fatal(manifest.MCP[0].Environment)
	}
	environment := map[string]string{}
	for i := range 16 {
		environment["VALUE_"+string(rune('A'+i))] = strings.Repeat("x", 4096)
	}
	encoded, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"wire": map[string]any{"command": "wire", "env": environment}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, string(encoded)), stdioBindings(), nil); err == nil {
		t.Fatal("accepted manifest larger than Native inspection limit")
	}
}

func TestConvertMCPExtensionRejectsShellOwnedEnvironment(t *testing.T) {
	for _, key := range []string{"IFS", "PWD", "OLDPWD", "SHLVL", "SHELLOPTS", "BASHOPTS", "BASH_VERSION", "BASHPID", "UID", "EUID", "PPID", "RANDOM", "SECONDS", "LINENO", "OPTIND", "OPTERR", "HISTCMD", "POSIXLY_CORRECT", "BASH", "BASH_VERSINFO", "BASH_COMMAND", "BASH_EXECUTION_STRING", "_", "PS1"} {
		t.Run(key, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"wire": map[string]any{"command": "wire", "env": map[string]string{key: "explicit-source-value"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ConvertMCPExtension(stdioSnapshot(stdioManifest, string(data)), stdioBindings(), nil); err == nil {
				t.Fatal("silently accepted shell-rewritten environment")
			}
		})
	}
}
