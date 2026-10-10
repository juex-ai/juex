package execprotocol

import (
	"io/fs"
	"strings"
	"time"
	"unicode/utf8"
)

const WorkspacePreviewBytes = 256 << 10
const WorkspaceListingBytes = 1 << 20

// WorkspaceQuery addresses a relative path beneath an explicitly selected cwd.
// Search matches names recursively; directory listing is otherwise one level.
type WorkspaceQuery struct {
	Path   string `json:"path"`
	Search string `json:"search,omitempty"`
	Hidden bool   `json:"hidden"`
	After  string `json:"after,omitempty"`
	Read   bool   `json:"read"`
}

func (q WorkspaceQuery) Validate() error {
	if !fs.ValidPath(q.Path) || len(q.Path) > 4096 || strings.ContainsAny(q.Path, "\x00\\") || !utf8.ValidString(q.Search) || len(q.Search) > 200 || strings.ContainsRune(q.Search, 0) || q.After != "" && (!fs.ValidPath(q.After) || len(q.After) > 4096) || q.Read && (q.Search != "" || q.After != "") {
		return ErrInvalid
	}
	return nil
}

type WorkspaceEntry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

type WorkspacePreview struct {
	Entry     WorkspaceEntry `json:"entry"`
	Text      string         `json:"text"`
	MediaType string         `json:"media_type"`
	Binary    bool           `json:"binary"`
	Truncated bool           `json:"truncated"`
}

type WorkspaceListing struct {
	Entries     []WorkspaceEntry  `json:"entries"`
	NextCursor  string            `json:"next_cursor"`
	ScanLimited bool              `json:"scan_limited"`
	Preview     *WorkspacePreview `json:"preview,omitempty"`
}
