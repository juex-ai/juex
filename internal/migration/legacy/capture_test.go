package legacy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func captureFixture(t *testing.T) Fleet {
	t.Helper()
	dir, _ := legacyFleetFixture(t)
	source, err := ReadFleet(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestPrivateCaptureRoundTripWithoutSource(t *testing.T) {
	dir, workspace := legacyFleetFixture(t)
	// Distinct Agent owners can share the spelling of Main and source messages.
	second := filepath.Join(dir, "agents/abc235")
	if err := os.CopyFS(second, os.DirFS(filepath.Join(dir, "agents/abc234"))); err != nil {
		t.Fatal(err)
	}
	mutateJSONFile(t, filepath.Join(second, "agent.json"), func(v map[string]any) { v["id"] = "abc235" })
	source, err := ReadFleet(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	before := fixtureHashes(t, dir)
	data, digest, err := EncodeCapture(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
		t.Fatal("encoding changed the source")
	}
	if err := os.Rename(dir, dir+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(dir+"-offline", dir) })
	if err := os.Rename(workspace, workspace+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(workspace+"-offline", workspace) })
	got, err := DecodeCapture(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, source) {
		t.Fatal("capture lost original bytes, ownership, context, coordinates, inputs or Memory")
	}
	again, sameDigest, err := EncodeCapture(got)
	if err != nil || sameDigest != digest || !bytes.Equal(data, again) {
		t.Fatalf("encoding is not stable: %v", err)
	}
	for _, agent := range got.Agents {
		thread := agent.Threads[0]
		if thread.Metadata.ThreadID != "0" || thread.Commits[5].GenerationID != "g000002" || thread.Commits[5].EndOffset >= thread.Commits[4].EndOffset {
			t.Fatal("generation-local coordinates or independent Main identity were lost")
		}
	}
}

func TestPrivateCaptureRejectsTamperingAndIncompleteProjections(t *testing.T) {
	source := captureFixture(t)
	data, digest, err := EncodeCapture(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCapture(append(append([]byte{}, data...), ' '), digest); err == nil {
		t.Fatal("digest did not bind every byte")
	}
	for _, test := range []struct {
		name string
		edit func(*captureEnvelope)
	}{
		{"version", func(v *captureEnvelope) { v.Version++ }},
		{"source revision", func(v *captureEnvelope) { v.Revision = "281889e5" }},
		{"missing state", func(v *captureEnvelope) { v.Fleet = nil }},
		{"missing bytes", func(v *captureEnvelope) { delete(v.Blobs, v.Fleet.Files[0].SHA256) }},
		{"changed bytes", func(v *captureEnvelope) { v.Blobs[v.Fleet.Files[0].SHA256] = []byte("different") }},
		{"unused bytes", func(v *captureEnvelope) { v.Blobs[captureDigest([]byte("extra"))] = []byte("extra") }},
		{"duplicate file", func(v *captureEnvelope) { v.Fleet.Files = append(v.Fleet.Files, v.Fleet.Files[0]) }},
		{"present and absent", func(v *captureEnvelope) { v.Fleet.AbsentFiles = append(v.Fleet.AbsentFiles, v.Fleet.Files[0].Path) }},
		{"unsafe path", func(v *captureEnvelope) { v.Fleet.Files[0].Path = "../outside" }},
		{"wrong mode", func(v *captureEnvelope) { v.Fleet.Agents[0].Files[0].Mode ^= 0100 }},
		{"lost private Agent file", func(v *captureEnvelope) {
			for i, file := range v.Fleet.Agents[0].Files {
				if file.Path == "extensions/calendar/calendar.json" {
					v.Fleet.Agents[0].Files = append(v.Fleet.Agents[0].Files[:i], v.Fleet.Agents[0].Files[i+1:]...)
					return
				}
			}
		}},
		{"missing Workspace absence", func(v *captureEnvelope) { v.Fleet.Workspaces[0].AbsentFiles = nil }},
		{"conflicting shared Home", func(v *captureEnvelope) {
			v.Fleet.DefaultHome.Files = nil
			v.Fleet.DefaultHome.AbsentFiles = []string{"juex.yaml"}
		}},
		{"omitted Agent", func(v *captureEnvelope) { v.Fleet.Agents = nil; v.Fleet.Workspaces = nil }},
		{"omitted Thread", func(v *captureEnvelope) { v.Fleet.Agents[0].Threads = nil }},
		{"omitted Memory", func(v *captureEnvelope) { v.Fleet.Memory = nil }},
		{"wrong Workspace", func(v *captureEnvelope) { v.Fleet.Workspaces[0].AgentID = "abc235" }},
		{"wrong Thread metadata", func(v *captureEnvelope) { v.Fleet.Agents[0].Threads[0].Metadata.Revision++ }},
		{"wrong context", func(v *captureEnvelope) { v.Fleet.Agents[0].Threads[0].Context = nil }},
		{"wrong context scope", func(v *captureEnvelope) { v.Fleet.Agents[0].Threads[0].ContextScopeID = "g000002" }},
		{"wrong commit", func(v *captureEnvelope) { v.Fleet.Agents[0].Threads[0].Commits[1].Seq++ }},
		{"lost Input", func(v *captureEnvelope) { v.Fleet.Agents[0].Threads[0].Inputs = nil }},
		{"wrong Memory", func(v *captureEnvelope) { v.Fleet.Memory.Entries[0].Body += "changed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var envelope captureEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			test.edit(&envelope)
			changed, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeCapture(changed, captureDigest(changed)); err == nil {
				t.Fatal("invalid capture accepted even with a recalculated outer digest")
			}
		})
	}
	for _, changed := range [][]byte{
		append([]byte(`{"version":1,`), data[1:]...),
		append(append([]byte{}, data...), []byte(`{}`)...),
		bytes.Replace(data, []byte(`"version":1`), []byte(`"unknown":1,"version":1`), 1),
		[]byte(strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130)),
	} {
		if _, err := DecodeCapture(changed, captureDigest(changed)); err == nil {
			t.Fatal("ambiguous, unknown or excessively nested JSON accepted")
		}
	}
}

func TestPrivateCaptureRejectsLossyEncoding(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Thread)
	}{
		{"generation", func(v *Thread) { v.Commits[0].GenerationID = "other" }},
		{"offset", func(v *Thread) { v.Commits[0].EndOffset++ }},
		{"request media", func(v *Thread) {
			v.Context[0].Blocks = append(v.Context[0].Blocks, llm.Block{Type: llm.BlockImage, Media: &llm.MediaRef{Data: []byte("request private bytes")}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := captureFixture(t)
			test.edit(&source.Agents[0].Threads[0])
			if _, _, err := EncodeCapture(source); err == nil {
				t.Fatal("unsupported in-memory state was silently discarded")
			}
		})
	}
}

func TestPrivateCaptureRejectsNonUTF8SourcePaths(t *testing.T) {
	source := captureFixture(t)
	// This is a valid native Linux pathname even when the test host's filesystem
	// cannot create it. Keep both ownership projections exactly in agreement.
	name := "modules/" + string([]byte{'x', 0xff}) + ".bin"
	for i := range source.Files {
		if source.Files[i].Path == "agents/abc234/modules/memory-client/participation.json" {
			source.Files[i].Path = "agents/abc234/" + name
		}
	}
	for i := range source.Agents[0].Files {
		if source.Agents[0].Files[i].Path == "modules/memory-client/participation.json" {
			source.Agents[0].Files[i].Path = name
		}
	}
	if _, _, err := EncodeCapture(source); err == nil {
		t.Fatal("capture silently replaced an original non-UTF8 pathname")
	}
	for _, edit := range []func(*Fleet){
		func(f *Fleet) { f.SourceHome += string([]byte{0xff}) },
		func(f *Fleet) { f.DefaultHome.Directory += string([]byte{0xff}) },
		func(f *Fleet) { f.Workspaces[0].ResolvedPath += string([]byte{0xff}) },
		func(f *Fleet) { f.Workspaces[0].AbsentFiles[0] += string([]byte{0xff}) },
		func(f *Fleet) { f.Skipped[0].Path += string([]byte{0xff}) },
	} {
		value := captureFixture(t)
		edit(&value)
		if _, _, err := EncodeCapture(value); err == nil {
			t.Fatal("capture silently replaced path provenance")
		}
	}
}

func TestPrivateCaptureRejectsNonUTF8FilesystemPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native non-UTF8 filename coverage requires a Linux filesystem; portable capture-path regression runs everywhere")
	}
	dir, _ := legacyFleetFixture(t)
	name := filepath.Join(dir, "agents/abc234/modules", string([]byte{'x', 0xff})+".bin")
	if err := os.WriteFile(name, []byte("private bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := ReadFleet(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := EncodeCapture(source); err == nil {
		t.Fatal("capture silently replaced an original non-UTF8 pathname")
	}
}
