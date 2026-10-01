package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
)

// inspectExtension runs through the ordinary file worker identity on Hosted.
// It reads explicit manifest resources, never executes or scans parent paths.
func inspectExtension(ctx context.Context, directory string, writer io.Writer) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func(){_ = root.Close()}()
	data, err := extensionFile(root, "juex.extension.json", 64<<10)
	if err != nil {
		return err
	}
	var catalog extensionpolicy.Catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog.Manifest); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return extensionpolicy.ErrInvalid
	}
	if err := catalog.Manifest.Validate(); err != nil {
		return err
	}
	total := len(data)
	catalog.Skills = []extensionpolicy.SkillContent{}
	for _, resource := range catalog.Manifest.Skills {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := extensionFile(root, resource.Path, 64<<10)
		if err != nil {
			return err
		}
		total += len(data)
		if total > extensionpolicy.MaxCatalogBytes {
			return extensionpolicy.ErrInvalid
		}
		catalog.Skills = append(catalog.Skills, extensionpolicy.SkillContent{ID: resource.ID, Content: string(data)})
	}
	catalog.Revision = catalog.Digest()
	if err := catalog.Validate(); err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(catalog)
}

func extensionFile(root *os.Root, name string, limit int64) ([]byte, error) {
	// Nonblocking open prevents named pipes from pinning an inspection worker.
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("extension resource must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || !utf8.Valid(data) {
		return nil, extensionpolicy.ErrInvalid
	}
	return data, nil
}
