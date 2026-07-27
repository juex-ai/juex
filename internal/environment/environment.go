// Package environment resolves one immutable runtime environment and derives
// deterministic child-process environments without exposing their values.
package environment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type Source string

const (
	SourceUserConfig      Source = "user_config"
	SourceDotenv          Source = "dotenv"
	SourceWorkspaceConfig Source = "workspace_config"
	SourceExplicitConfig  Source = "explicit_config"
	SourceInherited       Source = "inherited"
	SourceChild           Source = "child"
	SourceRuntime         Source = "runtime"
)

var reservedNames = map[string]struct{}{
	"JUEX_HOME":    {},
	"HOME":         {},
	"USERPROFILE":  {},
	"WORKDIR":      {},
	"JUEX_WORKDIR": {},
	"JUEX_EXT_DIR": {},
}

// Layer is one ordered source of environment declarations. Strict layers are
// validated as user-authored config; inherited process environments are not.
type Layer struct {
	Source Source
	Path   string
	Values map[string]string
	Strict bool
}

type Options struct {
	Layers    []Layer
	Inherited []string
	GOOS      string
}

// Metadata deliberately has no value field. It is safe for diagnostics and
// debug bundles that must describe environment provenance without disclosing
// configured values.
type Metadata struct {
	Key    string `json:"key"`
	Source Source `json:"source"`
	Path   string `json:"path,omitempty"`
}

type entry struct {
	key    string
	value  string
	source Source
	path   string
}

// Snapshot owns an immutable effective environment. Its maps are never
// returned directly; every public projection is a defensive copy.
type Snapshot struct {
	entries          map[string]entry
	configured       map[string]Metadata
	configuredValues []string
	caseInsensitive  bool
	resolved         bool
}

type previousValue struct {
	key   string
	value string
	set   bool
}

func Resolve(opts Options) (Snapshot, error) {
	goos := strings.TrimSpace(opts.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	caseInsensitive := goos == "windows"
	snapshot := Snapshot{
		entries:          make(map[string]entry),
		configured:       make(map[string]Metadata),
		configuredValues: make([]string, 0),
		caseInsensitive:  caseInsensitive,
		resolved:         true,
	}
	for _, layer := range opts.Layers {
		if layer.Strict {
			if err := validateStrictLayer(layer, caseInsensitive); err != nil {
				return Snapshot{}, err
			}
		}
		for _, key := range sortedMapKeys(layer.Values, caseInsensitive) {
			value := layer.Values[key]
			canonical := canonicalKey(key, caseInsensitive)
			snapshot.entries[canonical] = entry{
				key:    key,
				value:  value,
				source: layer.Source,
				path:   layer.Path,
			}
			if layer.Source != SourceInherited {
				snapshot.configured[canonical] = Metadata{
					Key:    key,
					Source: layer.Source,
					Path:   layer.Path,
				}
				snapshot.configuredValues = append(snapshot.configuredValues, value)
			}
		}
	}
	for _, item := range opts.Inherited {
		key, value, ok := splitInheritedEntry(item, caseInsensitive)
		if !ok {
			continue
		}
		canonical := canonicalKey(key, caseInsensitive)
		snapshot.entries[canonical] = entry{
			key:    key,
			value:  value,
			source: SourceInherited,
		}
	}
	return snapshot, nil
}

func splitInheritedEntry(item string, windows bool) (string, string, bool) {
	separator := strings.IndexByte(item, '=')
	if separator == 0 && windows {
		// Windows inherited environments can contain hidden per-drive
		// current-directory entries such as "=C:=C:\work". Their leading
		// equals sign is part of the key, matching os/exec's parsing contract.
		next := strings.IndexByte(item[1:], '=')
		if next >= 0 {
			separator = next + 1
		}
	}
	if separator <= 0 {
		return "", "", false
	}
	return item[:separator], item[separator+1:], true
}

// RedactConfiguredValues removes every non-empty configured value from a byte
// payload without exposing the values to callers. Both raw and JSON-escaped
// representations are covered.
func (s Snapshot) RedactConfiguredValues(data []byte) ([]byte, bool) {
	if !s.resolved || len(s.configuredValues) == 0 || len(data) == 0 {
		return append([]byte(nil), data...), false
	}
	values := s.configuredRedactionValues(true)
	out, changed := redactBytesWithValues(data, values)
	return out, changed
}

// RedactConfiguredJSON redacts configured values only inside JSON string
// values. Object keys and JSON syntax remain intact even for one-character
// configured values.
func (s Snapshot) RedactConfiguredJSON(data []byte) ([]byte, bool, error) {
	if !s.resolved || len(s.configuredValues) == 0 || len(data) == 0 {
		return append([]byte(nil), data...), false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, false, err
	}
	values := s.configuredRedactionValues(false)
	changed := redactJSONValue(&value, values)
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), changed, nil
}

func (s Snapshot) configuredRedactionValues(includeEscaped bool) []string {
	seen := map[string]struct{}{}
	values := make([]string, 0, len(s.configuredValues)*2)
	for _, value := range s.configuredValues {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			values = append(values, value)
		}
		if !includeEscaped {
			continue
		}
		encoded, err := json.Marshal(value)
		if err == nil && len(encoded) >= 2 {
			escaped := string(encoded[1 : len(encoded)-1])
			if escaped != value {
				if _, ok := seen[escaped]; !ok {
					seen[escaped] = struct{}{}
					values = append(values, escaped)
				}
			}
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) == len(values[j]) {
			return values[i] < values[j]
		}
		return len(values[i]) > len(values[j])
	})
	return values
}

func redactBytesWithValues(data []byte, values []string) ([]byte, bool) {
	out := append([]byte(nil), data...)
	changed := false
	for _, value := range values {
		if bytes.Contains(out, []byte(value)) {
			out = bytes.ReplaceAll(out, []byte(value), []byte("[REDACTED_ENV]"))
			changed = true
		}
	}
	return out, changed
}

func redactJSONValue(value *any, configuredValues []string) bool {
	switch current := (*value).(type) {
	case string:
		redacted, changed := redactBytesWithValues([]byte(current), configuredValues)
		if changed {
			*value = string(redacted)
		}
		return changed
	case []any:
		changed := false
		for i := range current {
			if redactJSONValue(&current[i], configuredValues) {
				changed = true
			}
		}
		return changed
	case map[string]any:
		changed := false
		for key, item := range current {
			if redactJSONValue(&item, configuredValues) {
				current[key] = item
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("environment: JSON payload contains multiple values")
	}
	return err
}

// FromEnviron creates a snapshot from an already-captured inherited
// environment. It cannot fail because inherited names are accepted verbatim.
func FromEnviron(env []string) Snapshot {
	snapshot, _ := Resolve(Options{Inherited: env})
	return snapshot
}

func (s Snapshot) IsZero() bool {
	return !s.resolved
}

func (s Snapshot) Lookup(key string) (string, bool) {
	if !s.resolved {
		return os.LookupEnv(key)
	}
	item, ok := s.entries[canonicalKey(key, s.caseInsensitive)]
	if !ok {
		return "", false
	}
	return item.value, true
}

// LookPath resolves an executable using this snapshot's PATH (and PATHEXT on
// Windows), instead of the ambient process environment.
func (s Snapshot) LookPath(file string) (string, error) {
	return s.lookPathInDir(file, "")
}

// LookPathInDir resolves slash-relative executables against the child working
// directory. A blank dir validates them against the current working directory.
func (s Snapshot) LookPathInDir(file, dir string) (string, error) {
	return s.lookPathInDir(file, dir)
}

func (s Snapshot) lookPathInDir(file, dir string) (string, error) {
	if !s.resolved {
		s = FromEnviron(os.Environ())
	}
	if strings.TrimSpace(file) == "" {
		return "", exec.ErrNotFound
	}
	if strings.ContainsAny(file, `/\`) {
		candidate := file
		if !filepath.IsAbs(candidate) && strings.TrimSpace(dir) != "" {
			candidate = filepath.Join(dir, candidate)
		}
		return s.resolveExecutableCandidate(candidate)
	}
	pathValue, _ := s.Lookup("PATH")
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		for _, ext := range s.executableExtensions(file) {
			candidate := filepath.Join(dir, file+ext)
			if !executableFile(candidate, s.caseInsensitive) {
				continue
			}
			if !filepath.IsAbs(candidate) {
				return candidate, exec.ErrDot
			}
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func (s Snapshot) resolveExecutableCandidate(file string) (string, error) {
	for _, ext := range s.executableExtensions(file) {
		candidate := file + ext
		if executableFile(candidate, s.caseInsensitive) {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func (s Snapshot) executableExtensions(file string) []string {
	if !s.caseInsensitive {
		return []string{""}
	}
	pathext, ok := s.Lookup("PATHEXT")
	if !ok || strings.TrimSpace(pathext) == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	extensions := make([]string, 0, 4)
	for _, ext := range strings.Split(pathext, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		extensions = append(extensions, ext)
	}
	if filepath.Ext(file) != "" {
		// Match os/exec on Windows: try an explicitly dotted name as-is,
		// then continue through PATHEXT if that exact file does not exist.
		return append([]string{""}, extensions...)
	}
	return extensions
}

// Environ returns a sorted, duplicate-free environment. Overlays are applied
// in order, so callers express child-local values before Juex-reserved runtime
// injection.
func (s Snapshot) Environ(overlays ...map[string]string) []string {
	if !s.resolved {
		s = FromEnviron(os.Environ())
	}
	entries := make(map[string]entry, len(s.entries))
	for key, item := range s.entries {
		entries[key] = item
	}
	for index, overlay := range overlays {
		source := SourceChild
		if index == len(overlays)-1 && len(overlays) > 1 {
			source = SourceRuntime
		}
		for _, key := range sortedMapKeys(overlay, s.caseInsensitive) {
			canonical := canonicalKey(key, s.caseInsensitive)
			entries[canonical] = entry{
				key:    key,
				value:  overlay[key],
				source: source,
			}
		}
	}
	out := make([]string, 0, len(entries))
	for _, item := range entries {
		out = append(out, item.key+"="+item.value)
	}
	sort.Strings(out)
	return out
}

func (s Snapshot) Metadata() []Metadata {
	if !s.resolved {
		s = FromEnviron(os.Environ())
	}
	out := make([]Metadata, 0, len(s.entries))
	for _, item := range s.entries {
		out = append(out, Metadata{
			Key:    item.key,
			Source: item.source,
			Path:   item.path,
		})
	}
	sortMetadata(out)
	return out
}

func (s Snapshot) ConfiguredMetadata() []Metadata {
	if !s.resolved {
		return nil
	}
	out := make([]Metadata, 0, len(s.configured))
	for _, item := range s.configured {
		out = append(out, item)
	}
	sortMetadata(out)
	return out
}

var activationMu sync.Mutex

// Activate overlays effective configured values onto the current process.
// The returned restore function is primarily useful to keep in-process CLI
// tests isolated; a normal Juex command holds the activation until exit.
func (s Snapshot) Activate() (func() error, error) {
	if !s.resolved {
		return func() error { return nil }, nil
	}
	if !activationMu.TryLock() {
		return nil, fmt.Errorf("environment: a runtime environment is already active in this process")
	}
	var previous []previousValue
	for _, item := range s.entries {
		if item.source == SourceInherited {
			continue
		}
		value, set := os.LookupEnv(item.key)
		previous = append(previous, previousValue{key: item.key, value: value, set: set})
		if err := os.Setenv(item.key, item.value); err != nil {
			restoreErr := restoreEnvironment(previous)
			activationMu.Unlock()
			if restoreErr != nil {
				return nil, fmt.Errorf(
					"environment: activate %s: %w (restore environment: %v)",
					item.key,
					err,
					restoreErr,
				)
			}
			return nil, fmt.Errorf("environment: activate %s: %w", item.key, err)
		}
	}
	var once sync.Once
	var restoreErr error
	return func() error {
		once.Do(func() {
			restoreErr = restoreEnvironment(previous)
			activationMu.Unlock()
		})
		return restoreErr
	}, nil
}

func restoreEnvironment(previous []previousValue) error {
	var firstErr error
	for i := len(previous) - 1; i >= 0; i-- {
		item := previous[i]
		var err error
		if item.set {
			err = os.Setenv(item.key, item.value)
		} else {
			err = os.Unsetenv(item.key)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func validateStrictLayer(layer Layer, caseInsensitive bool) error {
	seen := make(map[string]string, len(layer.Values))
	for _, key := range sortedMapKeys(layer.Values, caseInsensitive) {
		value := layer.Values[key]
		if err := validateConfiguredEntry(key, value); err != nil {
			return fmt.Errorf("environment: %s: %w", sourceLabel(layer.Path, layer.Source), err)
		}
		canonical := canonicalKey(key, caseInsensitive)
		if previous, ok := seen[canonical]; ok && previous != key {
			return fmt.Errorf(
				"environment: %s: case-conflicting names %q and %q",
				sourceLabel(layer.Path, layer.Source),
				previous,
				key,
			)
		}
		seen[canonical] = key
	}
	return nil
}

func validateConfiguredEntry(key, value string) error {
	if strings.IndexByte(key, 0) >= 0 || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("variable %q contains a NUL byte", key)
	}
	if !validPortableName(key) {
		return fmt.Errorf("invalid variable name %q", key)
	}
	if _, reserved := reservedNames[strings.ToUpper(key)]; reserved {
		return fmt.Errorf("variable %q is reserved by Juex", key)
	}
	return nil
}

func validPortableName(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		b := key[i]
		if i == 0 {
			if b != '_' && (b < 'A' || b > 'Z') && (b < 'a' || b > 'z') {
				return false
			}
			continue
		}
		if b != '_' && (b < 'A' || b > 'Z') && (b < 'a' || b > 'z') && (b < '0' || b > '9') {
			return false
		}
	}
	return true
}

func executableFile(path string, windows bool) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return windows || info.Mode().Perm()&0o111 != 0
}

func canonicalKey(key string, caseInsensitive bool) string {
	if caseInsensitive {
		return strings.ToUpper(key)
	}
	return key
}

func sortedMapKeys(values map[string]string, caseInsensitive bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := canonicalKey(keys[i], caseInsensitive)
		right := canonicalKey(keys[j], caseInsensitive)
		if left == right {
			return keys[i] < keys[j]
		}
		return left < right
	})
	return keys
}

func sortMetadata(items []Metadata) {
	sort.Slice(items, func(i, j int) bool {
		left := strings.ToUpper(items[i].Key)
		right := strings.ToUpper(items[j].Key)
		if left == right {
			if items[i].Source == items[j].Source {
				return items[i].Path < items[j].Path
			}
			return items[i].Source < items[j].Source
		}
		return left < right
	})
}

func sourceLabel(path string, source Source) string {
	if strings.TrimSpace(path) != "" {
		return path
	}
	if source != "" {
		return string(source)
	}
	return "configured environment"
}
