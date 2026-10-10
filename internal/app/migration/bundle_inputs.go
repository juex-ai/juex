package migration

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func encodeBundleInputs(inputs BundleInputs) ([]byte, error) {
	if !bundleText(reflect.ValueOf(inputs)) {
		return nil, errors.New("private inputs contain non-UTF-8 text")
	}
	w := bundlePrivate{Config: inputs.Config, Agents: inputs.Agents, ModelsPolicy: inputs.ModelsPolicy, Blobs: map[string][]byte{}}
	if inputs.Extensions != nil {
		w.Extensions = make([]bundleExtensionEvidence, 0, len(inputs.Extensions))
	}
	for _, extension := range inputs.Extensions {
		w.Extensions = append(w.Extensions, bundleExtensionEvidence(extension))
	}
	if inputs.Models != nil {
		w.Models = make([]bundleModelEvidence, 0, len(inputs.Models))
	}
	for _, m := range inputs.Models {
		w.Models = append(w.Models, bundleModelEvidence(m))
	}
	if err := visitBundleFiles(&w, func(f *legacy.SourceFile) error {
		if !validBundleSourceFile(*f) || int64(len(f.Data)) != f.Size || bundleDigest(f.Data) != f.SHA256 {
			return errors.New("private input file differs from its declared bytes")
		}
		w.Blobs[f.SHA256] = f.Data
		return nil
	}); err != nil {
		return nil, err
	}
	data, err := json.Marshal(w)
	if err != nil || len(data) > bundlePrivateLimit {
		return nil, errors.New("private migration inputs are oversized or cannot be encoded")
	}
	if _, err := decodeBundleInputs(data); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeBundleInputs(data []byte) (BundleInputs, error) {
	invalid := errors.New("invalid or incomplete private migration inputs")
	var w bundlePrivate
	if len(data) == 0 || len(data) > bundlePrivateLimit || decodeBundleJSON(data, &w) != nil || w.Blobs == nil {
		return BundleInputs{}, invalid
	}
	used := map[string]bool{}
	count := 0
	if err := visitBundleFiles(&w, func(f *legacy.SourceFile) error {
		count++
		b, ok := w.Blobs[f.SHA256]
		if count > 10000 || !validBundleSourceFile(*f) || !ok || int64(len(b)) != f.Size || bundleDigest(b) != f.SHA256 {
			return invalid
		}
		f.Data = b
		used[f.SHA256] = true
		return nil
	}); err != nil || len(used) != len(w.Blobs) {
		return BundleInputs{}, invalid
	}
	result := BundleInputs{Config: w.Config, Agents: w.Agents, ModelsPolicy: w.ModelsPolicy}
	if w.Extensions != nil {
		result.Extensions = make([]BundleExtension, 0, len(w.Extensions))
	}
	for _, extension := range w.Extensions {
		result.Extensions = append(result.Extensions, BundleExtension(extension))
	}
	if w.Models != nil {
		result.Models = make([]ModelEvidence, 0, len(w.Models))
	}
	for _, m := range w.Models {
		result.Models = append(result.Models, ModelEvidence(m))
	}
	return result, nil
}

func validBundleSourceFile(f legacy.SourceFile) bool {
	return f.Path != "" && !strings.ContainsRune(f.Path, 0) && bundleHash(f.SHA256) && f.Size >= 0 && f.Size <= bundlePrivateLimit/2 && f.Mode&^0777 == 0
}

func visitBundleFiles(w *bundlePrivate, visit func(*legacy.SourceFile) error) error {
	files := func(values []legacy.SourceFile) error {
		for i := range values {
			if err := visit(&values[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := files(w.Config.LocalImports); err != nil {
		return err
	}
	for _, c := range w.Config.Contexts {
		if err := files(c.ExplicitFiles); err != nil {
			return err
		}
	}
	for _, m := range w.Models {
		if m.CodexAuth != nil {
			if err := visit(m.CodexAuth); err != nil {
				return err
			}
		}
	}
	for _, e := range w.Extensions {
		if err := files(e.Snapshot.Files); err != nil {
			return err
		}
	}
	return nil
}
