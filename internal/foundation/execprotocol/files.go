package execprotocol

import (
	"crypto/sha256"
	"encoding/hex"
)

const FileChunkBytes = 256 << 10
const MaxFileBytes = 256 << 20

// FileManifest describes immutable bytes, independently of a device path.
type FileManifest struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func FileDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (m FileManifest) Validate() error {
	decoded, err := hex.DecodeString(m.SHA256)
	if m.Size < 0 || m.Size > MaxFileBytes || err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != m.SHA256 {
		return ErrInvalid
	}
	return nil
}

// FileChunk travels on the binary data plane, never through model tool output.
type FileChunk struct {
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

func (c FileChunk) Validate() error {
	if c.Offset < 0 || len(c.Data) > FileChunkBytes || FileDigest(c.Data) != c.SHA256 {
		return ErrInvalid
	}
	return nil
}
