package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

const bundleSourceRevision = "281889e57a7dec0379878bb42336d0a49d5a94de"
const bundlePrivateLimit = 64 << 20

// BundleTarget is supplied before capture and matched independently at load.
// Newly allocated model/Agent IDs belong to the respective owner's receipts.
type BundleTarget struct {
	DeploymentID string `json:"deployment_id"`
	ActorID      string `json:"actor_id"`
	TenantID     string `json:"tenant_id"`
	UserID       string `json:"user_id"`
	FleetID      string `json:"fleet_id"`
}

type BundleHeader struct {
	Target              BundleTarget        `json:"target"`
	CapturedAt          time.Time           `json:"captured_at"`
	MemoryAdvancedSince time.Time           `json:"memory_advanced_since"`
	MemoryControl       application.Control `json:"memory_control"`
}

type BundleAgentPolicy struct {
	// Activation is an explicit target lifecycle choice. Source process
	// autostart is retained as history, not an OS-job setting on a shared Runtime.
	Activation            string  `json:"activation"`
	Instructions          *string `json:"instructions"`
	FilesEnabled          *bool   `json:"files_enabled"`
	ShellEnabled          *bool   `json:"shell_enabled"`
	CalendarEnabled       *bool   `json:"calendar_enabled"`
	CollaborationEnabled  *bool   `json:"collaboration_enabled"`
	GlobalInstructionPath string  `json:"global_instruction_path"`
	// Workspace stays externally owned; capture does not authorize its deletion.
	Workspace    string                                           `json:"workspace"`
	ModelOrigins map[string]map[string]managedruntime.ModelConfig `json:"model_origins"`
}

type BundleModelPolicy struct {
	Key           ModelKey `json:"key"`
	OutputReserve int      `json:"output_reserve"`
	// Endpoint resolves only an absent source SDK default, never replaces a route.
	Endpoint string `json:"endpoint"`
}

type BundleExtension struct {
	AgentID  string                       `json:"agent_id"`
	Snapshot legacy.ExtensionSnapshot     `json:"snapshot"`
	Bindings map[string]MCPProcessBinding `json:"bindings"`
}

// BundleInputs is deliberately not a printable transport. The private wire
// below preserves fields which ordinary configuration reports omit.
type BundleInputs struct {
	Config       ConfigEvidence               `json:"-"`
	Models       []ModelEvidence              `json:"-"`
	Agents       map[string]BundleAgentPolicy `json:"-"`
	ModelsPolicy []BundleModelPolicy          `json:"-"`
	Extensions   []BundleExtension            `json:"-"`
}

type Bundle struct {
	header BundleHeader
	source legacy.Fleet
	inputs BundleInputs
	digest string
}

type bundlePayload struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type bundleManifest struct {
	Version          int           `json:"version"`
	SourceRevision   string        `json:"source_revision"`
	ConversionPolicy int           `json:"conversion_policy"`
	Header           BundleHeader  `json:"header"`
	Fleet            bundlePayload `json:"fleet"`
	Inputs           bundlePayload `json:"inputs"`
}

type bundleModelEvidence struct {
	AgentID                 string             `json:"agent_id"`
	Environment             map[string]*string `json:"environment"`
	ProcessWorkingDirectory string             `json:"process_working_directory"`
	UserHome                string             `json:"user_home"`
	CodexAuth               *legacy.SourceFile `json:"codex_auth"`
}

type bundlePrivate struct {
	Config       ConfigEvidence               `json:"config"`
	Models       []bundleModelEvidence        `json:"models"`
	Agents       map[string]BundleAgentPolicy `json:"agents"`
	ModelsPolicy []BundleModelPolicy          `json:"models_policy"`
	Extensions   []BundleExtension            `json:"extensions"`
	Blobs        map[string][]byte            `json:"blobs"`
}

func bundleDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func bundleHash(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && strings.ToLower(value) == value
}

func (h BundleHeader) validate() error {
	if h.Target.DeploymentID == "" || !bundleText(reflect.ValueOf(h)) || h.CapturedAt.IsZero() || h.MemoryAdvancedSince.IsZero() || h.MemoryAdvancedSince.After(h.CapturedAt) || h.MemoryControl.Epoch < 1 || h.MemoryControl.Version < 1 {
		return errors.New("invalid frozen migration header")
	}
	for _, value := range []string{h.Target.ActorID, h.Target.TenantID, h.Target.UserID, h.Target.FleetID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return errors.New("migration requires canonical destination identities")
		}
	}
	return nil
}

// WriteBundle creates a new private directory and publishes the manifest last.
// It never overwrites an existing attempt, reads source paths or asserts that
// source writers are stopped. The caller retains the returned digest separately.
func WriteBundle(directory string, source legacy.Fleet, inputs BundleInputs, header BundleHeader) (string, error) {
	if err := header.validate(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", errors.New("bundle directory must be absolute and clean")
	}
	fleet, fleetHash, err := legacy.EncodeCapture(source)
	if err != nil {
		return "", err
	}
	private, err := encodeBundleInputs(inputs)
	if err != nil {
		return "", err
	}
	manifest, err := json.Marshal(bundleManifest{1, bundleSourceRevision, 1, header, bundlePayload{fleetHash, int64(len(fleet))}, bundlePayload{bundleDigest(private), int64(len(private))}})
	if err != nil {
		return "", errors.New("cannot encode bundle manifest")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", errors.New("bundle directory must be new and private")
	}
	root, err := openBundleRoot(directory)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	for _, item := range []struct {
		name string
		data []byte
	}{{"fleet.capture.json", fleet}, {"private-inputs.json", private}, {"manifest.json", manifest}} {
		file, err := root.OpenFile(item.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", errors.New("cannot create private bundle file")
		}
		_, writeErr := file.Write(item.data)
		syncErr, closeErr := file.Sync(), file.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return "", errors.New("cannot persist private bundle file")
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return "", errors.New("cannot sync bundle directory")
	}
	syncErr, closeErr := dir.Sync(), dir.Close()
	if syncErr != nil || closeErr != nil {
		return "", errors.New("cannot sync bundle directory")
	}
	parent, err := os.Open(filepath.Dir(directory))
	if err != nil {
		return "", errors.New("cannot sync bundle parent directory")
	}
	syncErr, closeErr = parent.Sync(), parent.Close()
	if syncErr != nil || closeErr != nil {
		return "", errors.New("cannot sync bundle parent directory")
	}
	return bundleDigest(manifest), nil
}

// LoadBundle validates exactly the three fixed files, reading each only once.
// expectedSHA256 and target must come from the operator, not this directory.
func LoadBundle(directory, expectedSHA256 string, target BundleTarget) (*Bundle, error) {
	if !bundleHash(expectedSHA256) {
		return nil, errors.New("an independent manifest digest is required")
	}
	root, err := openBundleRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	manifest, err := readBundleFile(root, "manifest.json", 1<<20)
	if err != nil {
		return nil, err
	}
	if bundleDigest(manifest) != expectedSHA256 {
		return nil, errors.New("bundle manifest digest mismatch")
	}
	var m bundleManifest
	if decodeBundleJSON(manifest, &m) != nil || m.Version != 1 || m.SourceRevision != bundleSourceRevision || m.ConversionPolicy != 1 || m.Header.validate() != nil || m.Header.Target != target {
		return nil, errors.New("unsupported bundle or destination mismatch")
	}
	read := func(name string, p bundlePayload, limit int64) ([]byte, error) {
		if p.Size <= 0 || p.Size > limit || !bundleHash(p.SHA256) {
			return nil, errors.New("invalid bundle payload descriptor")
		}
		data, err := readBundleFile(root, name, p.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != p.Size || bundleDigest(data) != p.SHA256 {
			return nil, errors.New("bundle payload digest or size mismatch")
		}
		return data, nil
	}
	fleet, err := read("fleet.capture.json", m.Fleet, 1<<30)
	if err != nil {
		return nil, err
	}
	private, err := read("private-inputs.json", m.Inputs, bundlePrivateLimit)
	if err != nil {
		return nil, err
	}
	source, err := legacy.DecodeCapture(fleet, m.Fleet.SHA256)
	if err != nil {
		return nil, err
	}
	inputs, err := decodeBundleInputs(private)
	if err != nil {
		return nil, err
	}
	return &Bundle{header: m.Header, source: source, inputs: inputs, digest: expectedSHA256}, nil
}

func openBundleRoot(directory string) (*os.Root, error) {
	before, err := os.Lstat(directory)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0077 != 0 {
		return nil, errors.New("bundle requires a private real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("cannot open bundle directory")
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		_ = root.Close()
		return nil, errors.New("bundle directory changed while opening")
	}
	return root, nil
}

func readBundleFile(root *os.Root, name string, limit int64) ([]byte, error) {
	invalid := errors.New("bundle file must be private, regular, stable and within its size limit")
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() <= 0 || before.Size() > limit {
		return nil, invalid
	}
	return readBundleOpenedFile(root, name, limit, before)
}

func readBundleOpenedFile(root *os.Root, name string, limit int64, before os.FileInfo) ([]byte, error) {
	invalid := errors.New("bundle file must be private, regular, stable and within its size limit")
	file, err := openBundleFile(root, name)
	if err != nil {
		return nil, invalid
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !sameBundleFile(before, opened) {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) != before.Size() {
		return nil, invalid
	}
	after, err := file.Stat()
	if err != nil || !sameBundleFile(before, after) {
		return nil, invalid
	}
	linked, err := root.Lstat(name)
	if err != nil || !sameBundleFile(before, linked) {
		return nil, invalid
	}
	return data, nil
}

func sameBundleFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func decodeBundleJSON(data []byte, target any) error {
	invalid := errors.New("invalid private bundle JSON")
	if !utf8.Valid(data) {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if uniqueExtensionJSON(d, 0) != nil {
		return invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || !bundleText(reflect.ValueOf(target)) {
		return invalid
	}
	return nil
}

// Go JSON replaces invalid UTF-8 in strings. Reject it before encoding rather
// than silently changing native paths, private variables or map identities.
func bundleText(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return bundleText(v.Elem())
		}
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !bundleText(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			for i := 0; i < v.Len(); i++ {
				if !bundleText(v.Index(i)) {
					return false
				}
			}
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if !bundleText(it.Key()) || !bundleText(it.Value()) {
				return false
			}
		}
	}
	return true
}
