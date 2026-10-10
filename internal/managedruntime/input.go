package managedruntime

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	_ "golang.org/x/image/webp"
)

const MaxInputImages = 8

func (i InputRequest) Validate() error {
	if i.RequestID == "" || len(i.RequestID) > 200 || len(i.Text) > 256<<10 || len(i.Images) > MaxInputImages || strings.TrimSpace(i.Text) == "" && len(i.Images) == 0 {
		return ErrInvalid
	}
	for _, image := range i.Images {
		id, err := uuid.Parse(image.ArtifactID)
		if err != nil || id == uuid.Nil || id.String() != image.ArtifactID || image.Size <= 0 || image.Size > llm.MaxProviderImageArtifactBytes || (execprotocol.FileManifest{Size: image.Size, SHA256: image.SHA256}).Validate() != nil {
			return ErrInvalid
		}
		switch image.MediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return ErrInvalid
		}
	}
	return nil
}

func (s *Service) validateNewInput(ctx context.Context, scope Scope, input InputRequest) error {
	plan, err := s.Authority.Snapshot(ctx, scope)
	if err != nil {
		return err
	}
	if len(input.Images) == 0 {
		return nil
	}
	available := false
	for _, model := range plan.Models {
		if _, err := s.Authority.Provider(ctx, scope, model, ModelRequirements{Vision: true}); err == nil {
			available = true
			break
		} else if !errors.Is(err, ErrModelUnavailable) {
			return err
		}
	}
	if !available {
		return ErrModelUnavailable
	}
	if s.Media == nil {
		return ErrMediaUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, ref := range input.Images {
		data, err := s.Media.ReadMedia(readCtx, scope, *ref.MediaRef())
		if err != nil {
			return ErrMediaUnavailable
		}
		if int64(len(data)) != ref.Size || execprotocol.FileDigest(data) != ref.SHA256 {
			return ErrMediaUnavailable
		}
		// Header decoding is bounded by Artifact size and does not allocate a
		// bitmap using untrusted dimensions. Providers still decode full pixels.
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || "image/"+format != ref.MediaType || config.Width <= 0 || config.Height <= 0 {
			return ErrMediaUnavailable
		}
	}
	return nil
}
