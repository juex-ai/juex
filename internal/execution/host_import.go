package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// HostImport carries private bytes only during an offline import. Execution
// derives destination paths; callers cannot name arbitrary host destinations.
type HostImport struct {
	SourceSHA256 string                         `json:"source_sha256"`
	Threads      map[string][]HostImportFile    `json:"threads"`
	Extensions   map[string]HostExtensionImport `json:"extensions"`
}

type HostImportFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Data   []byte `json:"-"`
}

type HostExtensionImport struct {
	Installation []HostImportFile `json:"installation"`
	Private      []HostImportFile `json:"private"`
}

type HostImportReceipt struct {
	EnvironmentID        string            `json:"environment_id"`
	ThreadDirectories    map[string]string `json:"thread_directories"`
	ExtensionDirectories map[string]string `json:"extension_directories"`
}

func (v HostImport) Validate() error {
	if !hostImportHash(v.SourceSHA256) || len(v.Threads)+len(v.Extensions) == 0 {
		return execprotocol.ErrInvalid
	}
	var size int64
	count := 0
	validate := func(id string, files []HostImportFile) error {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return execprotocol.ErrInvalid
		}
		seen := map[string]bool{}
		for _, f := range files {
			if !utf8.ValidString(f.Path) || !fs.ValidPath(f.Path) || f.Path == "." || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.ContainsAny(f.Path, "\\\x00") || len(f.Path) > 4096 || !hostImportHash(f.SHA256) || f.Mode&^uint32(0777) != 0 || f.Mode&0400 == 0 {
				return execprotocol.ErrInvalid
			}
			key := strings.ToLower(f.Path)
			if seen[key] {
				return execprotocol.ErrInvalid
			}
			seen[key] = true
			digest := sha256.Sum256(f.Data)
			if hex.EncodeToString(digest[:]) != f.SHA256 {
				return execprotocol.ErrInvalid
			}
			count++
			size += int64(len(f.Data))
			if count > 10000 || size > 256<<20 {
				return execprotocol.ErrInvalid
			}
		}
		return nil
	}
	for id, files := range v.Threads {
		if err := validate(id, files); err != nil {
			return err
		}
	}
	for id, files := range v.Extensions {
		if len(files.Installation) == 0 {
			return execprotocol.ErrInvalid
		}
		if err := validate(id, files.Installation); err != nil {
			return err
		}
		if err := validate(id, files.Private); err != nil {
			return err
		}
	}
	return nil
}

func hostImportHash(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && strings.ToLower(value) == value
}

// RestoreHostFiles is used under deployment-wide offline maintenance. It
// allocates an owned environment but never starts an executor or enrollment.
func (h *ManagedManager) RestoreHostFiles(ctx context.Context, scope Scope, request HostImport) (receipt HostImportReceipt, err error) {
	if err = request.Validate(); err != nil {
		return receipt, err
	}
	backend, ok := h.Backend.(interface {
		RestoreFiles(context.Context, ManagedResource, HostImport) (HostImportReceipt, error)
	})
	if !ok {
		return receipt, execprotocol.ErrUnavailable
	}
	resource, err := h.ensureResource(ctx, scope)
	if err != nil {
		return receipt, err
	}
	called := false
	err = h.Store.LockManaged(ctx, resource.EnvironmentID, func(current ManagedResource) (ManagedResult, error) {
		result := ManagedResult{Running: current.Running, Provisioned: current.Provisioned}
		if err := h.matches(current); err != nil {
			return result, err
		}
		if current.AgentID != scope.AgentID || current.TenantID != scope.TenantID || current.UserID != scope.UserID || current.Backend != "host" || current.Running || current.Online || current.Busy || current.Unconfirmed || current.Purging || current.Provisioned {
			return result, execprotocol.ErrConflict
		}
		called = true
		var err error
		receipt, err = backend.RestoreFiles(ctx, current, request)
		return result, err
	})
	if err == nil && !called {
		err = execprotocol.ErrConflict
	}
	return receipt, err
}
