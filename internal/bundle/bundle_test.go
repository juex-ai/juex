package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
)

func TestCreateIncludesSessionFilesManifestAndRedacts(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-debug001"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":        `{"alias":"debug","kind":"primary","api_key":"sk-session-secret"}`,
		"conversation.jsonl":  `{"role":"user","blocks":[{"type":"text","text":"use Bearer abc123 and api_key=sk-live-secret"}]}` + "\n",
		"events.jsonl":        `{"type":"llm.responded","payload":{"token_usage":{"input_tokens":3,"output_tokens":1},"auth_token":"credential-token"}}` + "\n",
		"diagnostic.jsonl":    `{"event":"tool.completed","authorization":"Bearer diagnostic-secret"}` + "\n",
		"notes.md":            "- [ ] password=raw-secret\n",
		"logs/juex.log":       "cookie=session-cookie\n",
		"logs/debug.log":      "OPENAI_API_KEY=sk-debug-secret\n",
		"pending_input.jsonl": `{"text":"continue"}` + "\n",
	})
	out := filepath.Join(work, "debug.tar.gz")

	got, err := Create(Options{
		WorkDir:   work,
		SessionID: sessionID,
		OutPath:   out,
		Redact:    true,
		Now:       fixedBundleTime,
		Config: config.Config{
			ProviderID:       "openai",
			ProviderProtocol: "openai/responses",
			BaseURL:          "https://api.example",
			APIKey:           "sk-config-secret",
			Model:            "gpt-test",
			WorkDir:          work,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != out || got.SessionID != sessionID || got.Files == 0 || got.Bytes == 0 || !got.Redacted {
		t.Fatalf("result = %+v", got)
	}

	files := readBundleArchive(t, out)
	for _, want := range []string{
		"juex-debug-bundle/manifest.json",
		"juex-debug-bundle/runtime.json",
		"juex-debug-bundle/session/session.json",
		"juex-debug-bundle/session/conversation.jsonl",
		"juex-debug-bundle/session/events.jsonl",
		"juex-debug-bundle/session/notes.md",
		"juex-debug-bundle/session/logs/juex.log",
		"juex-debug-bundle/session/logs/debug.log",
		"juex-debug-bundle/session/pending_input.jsonl",
	} {
		if _, ok := files[want]; !ok {
			t.Fatalf("archive missing %s; files=%v", want, sortedBundleKeys(files))
		}
	}
	if _, ok := files["juex-debug-bundle/session/diagnostic.jsonl"]; ok {
		t.Fatalf("archive should exclude files outside the session bundle contract: files=%v", sortedBundleKeys(files))
	}

	all := string(joinBundleFiles(files))
	for _, leaked := range []string{
		"sk-session-secret",
		"sk-live-secret",
		"abc123",
		"credential-token",
		"diagnostic-secret",
		"raw-secret",
		"session-cookie",
		"sk-debug-secret",
		"sk-config-secret",
	} {
		if strings.Contains(all, leaked) {
			t.Fatalf("bundle leaked %q:\n%s", leaked, all)
		}
	}
	if !strings.Contains(all, "[REDACTED]") {
		t.Fatalf("redaction marker missing:\n%s", all)
	}
	if !strings.Contains(string(files["juex-debug-bundle/session/events.jsonl"]), "input_tokens") {
		t.Fatalf("token counters should not be redacted:\n%s", files["juex-debug-bundle/session/events.jsonl"])
	}

	var manifest Manifest
	if err := json.Unmarshal(files["juex-debug-bundle/manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.SessionID != sessionID || !manifest.Redacted {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, entry := range manifest.Entries {
		body, ok := files[entry.Path]
		if !ok {
			t.Fatalf("manifest entry missing from archive: %+v", entry)
		}
		sum := sha256.Sum256(body)
		if entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash mismatch for %s: %s", entry.Path, entry.SHA256)
		}
		if entry.Size != int64(len(body)) {
			t.Fatalf("size mismatch for %s: %d != %d", entry.Path, entry.Size, len(body))
		}
	}
}

func TestCreateEnvironmentMetadataNeverIncludesValuesRegardlessOfRedaction(t *testing.T) {
	const secret = "bundle-environment-secret-sentinel"
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(work, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("BUNDLE_ENV_SECRET="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    work,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentStateDir = filepath.Join(work, ".juex")
	sessionID := "20260727T120000-environment"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": "{}\n",
		"events.jsonl":       "{}\n",
		"notes.md":           "captured " + secret + "\n",
	})

	for _, redact := range []bool{false, true} {
		t.Run(strconv.FormatBool(redact), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "debug.tar.gz")
			result, err := Create(Options{
				WorkDir:   work,
				SessionID: sessionID,
				OutPath:   out,
				Redact:    redact,
				Config:    cfg,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Redacted {
				t.Fatal("mandatory environment-value redaction was not reported")
			}
			files := readBundleArchive(t, out)
			all := string(joinBundleFiles(files))
			if strings.Contains(all, secret) {
				t.Fatalf("bundle leaked environment value with redact=%t:\n%s", redact, all)
			}
			var snapshot RuntimeSnapshot
			runtimeBody := files["juex-debug-bundle/runtime.json"]
			if err := json.Unmarshal(runtimeBody, &snapshot); err != nil {
				t.Fatalf("decode runtime metadata: %v\n%s", err, runtimeBody)
			}
			if len(snapshot.Environment) != 1 ||
				snapshot.Environment[0].Key != "BUNDLE_ENV_SECRET" ||
				snapshot.Environment[0].Source != "dotenv" {
				t.Fatalf("runtime environment metadata = %+v", snapshot.Environment)
			}
		})
	}
}

func TestCreateRedactsResolvedExtensionEnvironmentDefaults(t *testing.T) {
	const secret = "bundle-extension-default-secret"
	work := t.TempDir()
	sessionID := "20260802T120000-extension"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"text":"` + secret + `"}` + "\n",
		"events.jsonl":       "{}\n",
	})
	base, err := environment.Resolve(environment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := base.WithDefaults([]environment.DefaultDeclaration{{
		Key: "EXTENSION_BUNDLE_SECRET", Value: secret, Source: "ext:demo", Path: "/manifest",
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "debug.tar.gz")
	result, err := Create(Options{
		WorkDir: work, SessionID: sessionID, OutPath: out,
		Config: config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")}, Environment: resolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Redacted {
		t.Fatal("Extension environment redaction was not reported")
	}
	all := string(joinBundleFiles(readBundleArchive(t, out)))
	if strings.Contains(all, secret) || !strings.Contains(all, "[REDACTED_ENV]") {
		t.Fatalf("bundle Extension environment redaction = %q", all)
	}
}

func TestCreateEnvironmentRedactionPreservesJSONLAndManifestIntegrity(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(work, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	envBody := "SHORT_ENV=a\nNUMBER_ENV=1234\nBOOL_ENV=true\nPROJECT_DIR=" + work + "\n"
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    work,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentStateDir = filepath.Join(work, ".juex")
	sessionID := "20260727T120000-jsonl"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": "{\"value\":\"a\",\"number\":1234,\"flag\":true,\"stable\":\"first\"}\n{\"value\":\"second\"}\n",
		"events.jsonl":       "{\"type\":\"stable\"}\n",
	})
	out := filepath.Join(t.TempDir(), "debug.tar.gz")
	result, err := Create(Options{
		WorkDir:   work,
		SessionID: sessionID,
		OutPath:   out,
		Config:    cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Redacted {
		t.Fatal("configured value redaction was not reported")
	}

	files := readBundleArchive(t, out)
	conversationPath, _ := cfg.EnvironmentSnapshot().RedactConfiguredValues(
		[]byte(pathInBundle("session/conversation.jsonl")),
	)
	conversation := files[string(conversationPath)]
	lines := strings.Split(strings.TrimSuffix(string(conversation), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL record count = %d, body:\n%s", len(lines), conversation)
	}
	for index, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("JSONL record %d is invalid: %q", index, line)
		}
	}
	var first map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["v[REDACTED_ENV]lue"] != "[REDACTED_ENV]" {
		t.Fatalf("first JSONL record = %#v", first)
	}
	for _, key := range []string{"number", "fl[REDACTED_ENV]g"} {
		if first[key] != "[REDACTED_ENV]" {
			t.Fatalf("first JSONL scalar %s = %#v", key, first)
		}
	}
	if first["st[REDACTED_ENV]ble"] != "first" {
		t.Fatalf("first JSONL redacted key content = %#v", first)
	}

	manifestPath, _ := cfg.EnvironmentSnapshot().RedactConfiguredValues(
		[]byte(pathInBundle("manifest.json")),
	)
	manifestBody := files[string(manifestPath)]
	var manifest Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.WorkDir != "[REDACTED_ENV]" {
		t.Fatalf("manifest work_dir = %q, want redacted marker", manifest.WorkDir)
	}
	for _, entry := range manifest.Entries {
		body, ok := files[entry.Path]
		if !ok {
			t.Fatalf("manifest entry missing from archive: %+v", entry)
		}
		if strings.Contains(entry.SourcePath, work) {
			t.Fatalf("manifest source_path leaked configured work dir: %+v", entry)
		}
		sum := sha256.Sum256(body)
		if entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash mismatch for %s: %s", entry.Path, entry.SHA256)
		}
	}
}

func TestRedactConfiguredArchiveJSONCoversKeysScalarsAndCollisions(t *testing.T) {
	snapshot, err := environment.Resolve(environment.Options{Layers: []environment.Layer{{
		Source: environment.SourceDotenv,
		Path:   "/work/.env",
		Values: map[string]string{
			"KEY_SECRET":    "secret",
			"NUMBER_SECRET": "1234",
			"BOOL_SECRET":   "true",
			"NULL_SECRET":   "null",
		},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := redactConfiguredArchiveJSON(
		snapshot,
		[]byte(`{"secret":"key-value","[REDACTED_ENV]":"existing","number":1234,"flag":true,"none":null,"safe":false}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("strict archive JSON redaction did not report a change")
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("redacted archive JSON invalid: %v\n%s", err, got)
	}
	if decoded["[REDACTED_ENV]"] != "existing" ||
		decoded["[REDACTED_ENV]~[REDACTED_ENV]"] != "key-value" {
		t.Fatalf("redacted key collision lost content: %#v", decoded)
	}
	for _, key := range []string{"number", "flag", "none"} {
		if decoded[key] != "[REDACTED_ENV]" {
			t.Fatalf("strict scalar %s = %#v, want redacted marker", key, decoded[key])
		}
	}
	if decoded["safe"] != false {
		t.Fatalf("unmatched scalar type changed: %#v", decoded["safe"])
	}
}

func TestCreateRedactsConfiguredValuesFromArchivePathsAndResolvesCollisions(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(work, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	envBody := "ARCHIVE_ROOT=juex\nARTIFACT_ONE=alpha-private\nARTIFACT_TWO=beta-private\nCOLLISION_SUFFIX=2\n"
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    work,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentStateDir = filepath.Join(work, ".juex")
	sessionID := "20260727T120000-path-redaction"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": "{}\n",
		"events.jsonl":       "{}\n",
	})
	writeBundleFile(t, filepath.Join(cfg.ArtifactDir(), "run", "alpha-private.txt"), "alpha body")
	writeBundleFile(t, filepath.Join(cfg.ArtifactDir(), "run", "beta-private.txt"), "beta body")

	out := filepath.Join(t.TempDir(), "debug.tar.gz")
	result, err := Create(Options{
		WorkDir:          work,
		SessionID:        sessionID,
		OutPath:          out,
		IncludeArtifacts: true,
		Config:           cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Redacted {
		t.Fatal("archive path redaction was not reported")
	}

	files := readBundleArchive(t, out)
	keys := sortedBundleKeys(files)
	keyList := strings.Join(keys, "\n")
	for _, leaked := range []string{"juex", "alpha-private", "beta-private"} {
		if strings.Contains(keyList, leaked) {
			t.Fatalf("archive paths leaked %q:\n%s", leaked, keyList)
		}
	}
	root := "[REDACTED_ENV]-debug-bundle"
	artifactPaths := []string{
		root + "/artifacts/run/[REDACTED_ENV].txt",
		root + "/artifacts/run/[REDACTED_ENV]~[REDACTED_ENV].txt",
	}
	payloads := map[string]bool{}
	for _, archivePath := range artifactPaths {
		body, ok := files[archivePath]
		if !ok {
			t.Fatalf("archive missing remapped path %q; files=%v", archivePath, keys)
		}
		payloads[string(body)] = true
	}
	if !payloads["alpha body"] || !payloads["beta body"] {
		t.Fatalf("remapped artifact payloads = %#v", payloads)
	}

	var manifest Manifest
	if err := json.Unmarshal(files[root+"/manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for _, entry := range manifest.Entries {
		if _, duplicate := seen[entry.Path]; duplicate {
			t.Fatalf("duplicate manifest path after redaction: %q", entry.Path)
		}
		seen[entry.Path] = struct{}{}
		body, ok := files[entry.Path]
		if !ok {
			t.Fatalf("manifest entry missing from archive: %+v", entry)
		}
		sum := sha256.Sum256(body)
		if entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash mismatch for %s: %s", entry.Path, entry.SHA256)
		}
	}
}

func TestCreateFailsForMissingSessionAndRequiredFiles(t *testing.T) {
	work := t.TempDir()
	out := filepath.Join(work, "missing.tar.gz")
	_, err := Create(Options{WorkDir: work, SessionID: "missing", OutPath: out, Redact: true})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial bundle exists: %v", statErr)
	}

	sessionID := "20260614T120000-bad0001"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json": `{"kind":"primary"}`,
		"events.jsonl": `{"type":"x"}` + "\n",
	})
	_, err = Create(Options{WorkDir: work, SessionID: sessionID, OutPath: out, Redact: true})
	if !errors.Is(err, ErrRequiredFileMissing) {
		t.Fatalf("err = %v, want ErrRequiredFileMissing", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial bundle exists: %v", statErr)
	}
}

func TestCreateRejectsBlankOutputPath(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-blankout"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n",
		"events.jsonl":       `{"type":"x"}` + "\n",
	})

	_, err := Create(Options{WorkDir: work, SessionID: sessionID, OutPath: "   ", Redact: true})
	if err == nil || !strings.Contains(err.Error(), "output path required") {
		t.Fatalf("err = %v, want output path required", err)
	}
}

func TestCreateRejectsTraversalSessionID(t *testing.T) {
	work := t.TempDir()
	escapedSessionDir := filepath.Join(work, ".juex", "evil")
	for name, body := range map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n",
		"events.jsonl":       `{"type":"x"}` + "\n",
	} {
		writeBundleFile(t, filepath.Join(escapedSessionDir, name), body)
	}

	out := filepath.Join(work, "debug.tar.gz")
	_, err := Create(Options{WorkDir: work, SessionID: "../evil", OutPath: out, Redact: true})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial bundle exists: %v", statErr)
	}
}

func TestCreateFailsWhenOutputExistsUnlessForced(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-force001"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n",
		"events.jsonl":       `{"type":"x"}` + "\n",
	})
	out := filepath.Join(work, "debug.tar.gz")
	if err := os.WriteFile(out, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Create(Options{WorkDir: work, SessionID: sessionID, OutPath: out, Redact: true})
	if !errors.Is(err, ErrOutputExists) {
		t.Fatalf("err = %v, want ErrOutputExists", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "keep" {
		t.Fatalf("existing output was overwritten: %q", body)
	}

	if _, err := Create(Options{WorkDir: work, SessionID: sessionID, OutPath: out, Redact: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if files := readBundleArchive(t, out); files["juex-debug-bundle/manifest.json"] == nil {
		t.Fatalf("forced archive missing manifest")
	}
}

func TestCreateRejectsDirectoryOutputPath(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-dirout"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n",
		"events.jsonl":       `{"type":"x"}` + "\n",
	})
	outDir := filepath.Join(work, "debug-out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Create(Options{WorkDir: work, SessionID: sessionID, OutPath: outDir, Redact: true, Force: true})
	if err == nil || !strings.Contains(err.Error(), "output path is a directory") {
		t.Fatalf("err = %v, want output path is a directory", err)
	}
	if st, statErr := os.Stat(outDir); statErr != nil || !st.IsDir() {
		t.Fatalf("output directory was replaced: stat=%v err=%v", st, statErr)
	}
}

func TestCreateIncludesExtraFilesAndArtifactsWhenRequested(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-extra001"
	seedBundleSession(t, work, sessionID, map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n",
		"events.jsonl":       `{"type":"x"}` + "\n",
	})
	stateDir := filepath.Join(work, ".juex")
	writeBundleFile(t, filepath.Join(stateDir, "artifacts", "run", "output.txt"), "artifact output")
	out := filepath.Join(work, "debug.tar.gz")

	_, err := Create(Options{
		WorkDir:                work,
		SessionID:              sessionID,
		OutPath:                out,
		Redact:                 true,
		IncludeArtifacts:       true,
		Config:                 config.Config{WorkDir: work, AgentStateDir: stateDir},
		ExtraFiles:             []ExtraFile{{ArchivePath: "verifier/log.txt", Bytes: []byte("token=verifier-secret\n"), Redact: true}},
		IncludeWorktreeSummary: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	files := readBundleArchive(t, out)
	if string(files["juex-debug-bundle/artifacts/run/output.txt"]) != "artifact output" {
		t.Fatalf("artifact missing: %v", sortedBundleKeys(files))
	}
	if !strings.Contains(string(files["juex-debug-bundle/verifier/log.txt"]), "[REDACTED]") ||
		strings.Contains(string(files["juex-debug-bundle/verifier/log.txt"]), "verifier-secret") {
		t.Fatalf("extra file not redacted: %s", files["juex-debug-bundle/verifier/log.txt"])
	}
	if _, ok := files["juex-debug-bundle/worktree/summary.json"]; !ok {
		t.Fatalf("worktree summary missing: %v", sortedBundleKeys(files))
	}
}

func TestRedactionHandlesQuotedSecretsAndPreservesJSONLBlankLines(t *testing.T) {
	input := []byte(`{"text":"password=\"my secret value\" and token='single secret value'"}` + "\n\n" + `{"text":"ok"}` + "\n")

	got := string(redactBytes(input))
	for _, leaked := range []string{"my secret value", "single secret value"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("redaction leaked %q:\n%s", leaked, got)
		}
	}
	if strings.Count(got, "\n") != strings.Count(string(input), "\n") {
		t.Fatalf("JSONL newline count changed: got %q", got)
	}
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("JSONL blank line not preserved: %q", got)
	}
}

func TestSafeExtraArchivePathRejectsAbsoluteAndTraversalPaths(t *testing.T) {
	for _, path := range []string{
		"/tmp/debug.txt",
		`\tmp\debug.txt`,
		`C:\debug\log.txt`,
		`\\server\share\log.txt`,
		"../debug.txt",
	} {
		if got, err := safeExtraArchivePath(path); err == nil {
			t.Fatalf("safeExtraArchivePath(%q) = %q, want error", path, got)
		}
	}
	if got, err := safeExtraArchivePath("verifier/log.txt"); err != nil || got != "verifier/log.txt" {
		t.Fatalf("safeExtraArchivePath(valid) = %q, %v", got, err)
	}
}

func fixedBundleTime() time.Time {
	return time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
}

func seedBundleSession(t *testing.T, work, id string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(work, ".juex", "sessions", id)
	for name, body := range files {
		writeBundleFile(t, filepath.Join(dir, name), body)
	}
	return dir
}

func writeBundleFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readBundleArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.FileInfo().IsDir() {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = body
	}
	return out
}

func joinBundleFiles(files map[string][]byte) []byte {
	var b strings.Builder
	for _, key := range sortedBundleKeys(files) {
		b.Write(files[key])
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func sortedBundleKeys(files map[string][]byte) []string {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
