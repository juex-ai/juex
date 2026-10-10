package legacy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

const captureRevision = "281889e57a7dec0379878bb42336d0a49d5a94de"
const maxCaptureBytes = 1 << 30
const maxCaptureFiles = 100000

// captureEnvelope is private transport, not a report. Original file bytes are
// authoritative; derived Thread state is checked and rebuilt when decoding.
type captureEnvelope struct {
	Version  int               `json:"version"`
	Revision string            `json:"source_revision"`
	Fleet    *Fleet            `json:"fleet"`
	Blobs    map[string][]byte `json:"blobs"`
}

// EncodeCapture freezes an already captured Fleet without filesystem access.
// The returned bytes contain private history/configuration and must be stored
// privately. They are not a full Workspace backup or proof of source quiescence.
func EncodeCapture(source Fleet) ([]byte, string, error) {
	if err := captureEncodingPaths(source); err != nil {
		return nil, "", err
	}
	blobs := map[string][]byte{}
	if err := visitCaptureFiles(&source, func(files []SourceFile, absent []string) error {
		for _, file := range files {
			if !capturePath(file.Path) || int64(len(file.Data)) != file.Size || captureDigest(file.Data) != file.SHA256 {
				return errors.New("capture file bytes differ from their declared digest or size")
			}
			blobs[file.SHA256] = file.Data
		}
		for _, name := range absent {
			if !capturePath(name) {
				return errors.New("captured absence path cannot be encoded losslessly")
			}
		}
		return nil
	}); err != nil {
		return nil, "", err
	}
	for _, agent := range source.Agents {
		for _, thread := range agent.Threads {
			rebuilt, err := rebuildCapturedThread(thread)
			if err != nil || !reflect.DeepEqual(rebuilt.Metadata, thread.Metadata) || !reflect.DeepEqual(rebuilt.Commits, thread.Commits) || !reflect.DeepEqual(rebuilt.Context, thread.Context) || !reflect.DeepEqual(rebuilt.Inputs, thread.Inputs) {
				return nil, "", errors.New("captured Thread differs from original journal or contains request-only media")
			}
		}
	}
	data, err := json.Marshal(captureEnvelope{1, captureRevision, &source, blobs})
	if err != nil {
		return nil, "", errors.New("could not encode private capture")
	}
	digest := captureDigest(data)
	if _, err := DecodeCapture(data, digest); err != nil {
		return nil, "", err
	}
	return data, digest, nil
}

// DecodeCapture requires the digest recorded independently at capture time.
// It never consults source paths, environment, credentials or the current clock.
// Missing private bytes cannot become an empty file or a new live lookup.
func DecodeCapture(data []byte, expectedSHA256 string) (Fleet, error) {
	if len(data) == 0 || len(data) > maxCaptureBytes || !captureHash(expectedSHA256) || captureDigest(data) != expectedSHA256 {
		return Fleet{}, errors.New("private capture size or digest mismatch")
	}
	if err := uniqueCaptureJSON(data); err != nil {
		return Fleet{}, err
	}
	var envelope captureEnvelope
	if err := decode(data, &envelope); err != nil {
		return Fleet{}, errors.New("invalid private capture JSON")
	}
	if envelope.Version != 1 || envelope.Revision != captureRevision || envelope.Fleet == nil || envelope.Blobs == nil || len(envelope.Blobs) > maxCaptureFiles {
		return Fleet{}, errors.New("unsupported or incomplete private capture")
	}
	var total int64
	for digest, content := range envelope.Blobs {
		total += int64(len(content))
		if !captureHash(digest) || captureDigest(content) != digest || len(content) > maxSourceFileBytes || total > maxCaptureBytes/2 {
			return Fleet{}, errors.New("invalid or oversized capture blob")
		}
	}
	used := map[string]bool{}
	count := 0
	err := visitCaptureFiles(envelope.Fleet, func(files []SourceFile, absent []string) error {
		count += len(files) + len(absent)
		if count > maxCaptureFiles {
			return errors.New("too many capture file references")
		}
		seen := map[string]bool{}
		paths := make([]string, 0, len(files))
		for i := range files {
			file := &files[i]
			content, ok := envelope.Blobs[file.SHA256]
			if !capturePath(file.Path) || seen[file.Path] || !ok || file.Mode&^0777 != 0 || file.Size != int64(len(content)) {
				return errors.New("invalid capture file reference")
			}
			seen[file.Path], used[file.SHA256] = true, true
			paths = append(paths, file.Path)
			file.Data = content
		}
		slices.Sort(paths)
		for _, name := range absent {
			if !capturePath(name) || seen[name] {
				return errors.New("invalid or conflicting captured absence")
			}
			i, _ := slices.BinarySearch(paths, name+"/")
			if i < len(paths) && strings.HasPrefix(paths[i], name+"/") {
				return errors.New("captured absence conflicts with a file")
			}
			for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
				if _, present := slices.BinarySearch(paths, ancestor); present {
					return errors.New("captured absence conflicts with a file")
				}
			}
			seen[name] = true
		}
		return nil
	})
	if err != nil {
		return Fleet{}, err
	}
	if len(used) != len(envelope.Blobs) {
		return Fleet{}, errors.New("capture includes an unreferenced blob")
	}
	if err := validateCapturedFleet(envelope.Fleet); err != nil {
		return Fleet{}, err
	}
	return *envelope.Fleet, nil
}

func visitCaptureFiles(f *Fleet, visit func([]SourceFile, []string) error) error {
	if err := visit(f.Files, f.AbsentFiles); err != nil {
		return err
	}
	if err := visit(f.DefaultHome.Files, f.DefaultHome.AbsentFiles); err != nil {
		return err
	}
	for i := range f.Workspaces {
		if err := visit(f.Workspaces[i].Files, f.Workspaces[i].AbsentFiles); err != nil {
			return err
		}
	}
	if f.Memory != nil {
		if err := visit(f.Memory.Files, f.Memory.AbsentFiles); err != nil {
			return err
		}
	}
	for i := range f.Agents {
		a := &f.Agents[i]
		if err := visit(a.Files, a.AbsentFiles); err != nil {
			return err
		}
		for j := range a.Threads {
			if err := visit(a.Threads[j].Files, a.Threads[j].AbsentFiles); err != nil {
				return err
			}
		}
	}
	return nil
}

func captureDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func captureHash(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32 && strings.ToLower(value) == value
}

func capturePath(value string) bool {
	return utf8.ValidString(value) && fs.ValidPath(value) && value != "." && !strings.ContainsAny(value, "\\\x00")
}

func captureDirectory(value string) bool {
	return utf8.ValidString(value) && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0)
}

func captureEncodingPaths(f Fleet) error {
	if !captureDirectory(f.SourceHome) || !captureDirectory(f.DefaultHome.Directory) || !utf8.ValidString(f.ID) {
		return errors.New("capture Home paths or identity cannot be encoded losslessly")
	}
	skipped := append([]SkippedSource(nil), f.Skipped...)
	if f.Memory != nil {
		skipped = append(skipped, f.Memory.Skipped...)
	}
	for _, a := range f.Agents {
		if !captureDirectory(a.Definition.Workspace) || !utf8.ValidString(a.Definition.Name) {
			return errors.New("capture Agent paths or name cannot be encoded losslessly")
		}
		skipped = append(skipped, a.Skipped...)
	}
	for _, w := range f.Workspaces {
		if !captureDirectory(w.Path) || !captureDirectory(w.ResolvedPath) {
			return errors.New("capture Workspace paths cannot be encoded losslessly")
		}
	}
	for _, value := range skipped {
		if !capturePath(value.Path) || !utf8.ValidString(value.Reason) {
			return errors.New("capture skipped-path evidence cannot be encoded losslessly")
		}
	}
	return nil
}

// DisallowUnknownFields does not reject duplicate keys. A private wire object
// must have one unambiguous interpretation before it can authorize any import.
func uniqueCaptureJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return errors.New("capture JSON nesting exceeds limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errors.New("invalid capture JSON container")
		}
		keys := map[string]bool{}
		for d.More() {
			if delim == '{' {
				token, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || keys[key] {
					return errors.New("duplicate capture JSON key")
				}
				keys[key] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return errors.New("invalid, duplicate or excessively nested capture JSON")
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("capture must contain one JSON value")
	}
	return nil
}

func capturedFile(files []SourceFile, name string) (SourceFile, error) {
	for _, file := range files {
		if file.Path == name {
			return file, nil
		}
	}
	return SourceFile{}, fmt.Errorf("capture is missing required file %s", name)
}

func sameCaptureJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

func captureProjection(parent []SourceFile, parentAbsent []string, prefix string, files []SourceFile, absent []string, complete bool) error {
	byPath := make(map[string]SourceFile, len(parent))
	for _, file := range parent {
		byPath[file.Path] = file
	}
	for _, file := range files {
		got, ok := byPath[path.Join(prefix, file.Path)]
		if !ok || got.SHA256 != file.SHA256 || got.Size != file.Size || got.Mode != file.Mode || !got.ModifiedAt.Equal(file.ModifiedAt) {
			return errors.New("capture owner file projections disagree")
		}
	}
	for _, name := range absent {
		found := false
		for _, parentName := range parentAbsent {
			found = found || parentName == path.Join(prefix, name)
		}
		if !found {
			return errors.New("capture owner absence projections disagree")
		}
	}
	if complete {
		fileCount, absentCount := 0, 0
		for _, file := range parent {
			if strings.HasPrefix(file.Path, prefix+"/") {
				fileCount++
			}
		}
		for _, name := range parentAbsent {
			if strings.HasPrefix(name, prefix+"/") {
				absentCount++
			}
		}
		if fileCount != len(files) || absentCount != len(absent) {
			return errors.New("capture omitted owner files or absence records")
		}
	}
	return nil
}
