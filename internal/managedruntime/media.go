package managedruntime

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

var ErrMediaUnavailable = errors.New("referenced media is unavailable or exceeds the request limit")

// Match one full eight-image input while keeping historical wire bytes bounded.
const maxRequestMediaBytes = MaxInputImages * llm.MaxProviderImageArtifactBytes

type MediaGateway interface {
	ReadMedia(context.Context, Scope, llm.MediaRef) ([]byte, error)
}

func hydrateMedia(ctx context.Context, gateway MediaGateway, scope Scope, history []llm.Message) ([]llm.Message, error) {
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := slices.Clone(history)
	cache := map[string][]byte{}
	total := 0
	for i, message := range history {
		result[i].Blocks = slices.Clone(message.Blocks)
		for j, block := range message.Blocks {
			if block.Type != llm.BlockImage && block.Media == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if block.Media == nil || gateway == nil || block.Media.ArtifactPath != "" {
				return nil, ErrMediaUnavailable
			}
			ref := *block.Media
			if id, err := uuid.Parse(ref.ArtifactID); err != nil || id == uuid.Nil || id.String() != ref.ArtifactID {
				return nil, ErrMediaUnavailable
			}
			key := ref.ArtifactID + "/" + ref.SHA256
			data, ok := cache[key]
			if !ok {
				var err error
				data, err = gateway.ReadMedia(readCtx, scope, ref)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil || len(data) == 0 || len(data) > llm.MaxProviderImageArtifactBytes || execprotocol.FileDigest(data) != ref.SHA256 {
					return nil, ErrMediaUnavailable
				}
				cache[key] = data
			}
			// Count every wire occurrence even when an authorized read is reused.
			total += len(data)
			if total > maxRequestMediaBytes || !providerImageType(ref.MediaType, data) {
				return nil, ErrMediaUnavailable
			}
			ref.Data = data
			result[i].Blocks[j].Media = &ref
		}
	}
	return result, nil
}

func providerImageType(mediaType string, data []byte) bool {
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp":
		return true
	}
	return false
}
