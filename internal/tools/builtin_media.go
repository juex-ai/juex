package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

const (
	readImageMaxBytes  = 4 * 1024 * 1024
	readImageMaxSide   = 2000
	readImageMaxPixels = 24_000_000
)

type MediaResult struct {
	Media llm.MediaRef `json:"media"`
}

func MediaRefFromStructuredResult(result any) (*llm.MediaRef, bool) {
	switch v := result.(type) {
	case MediaResult:
		media := v.Media
		return &media, true
	case *MediaResult:
		if v == nil {
			return nil, false
		}
		media := v.Media
		return &media, true
	default:
		return nil, false
	}
}

type readImageKind struct {
	mediaType string
	ext       string
}

func detectReadImage(path string, data []byte) (readImageKind, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			return readImageKind{mediaType: "image/png", ext: ".png"}, true
		}
	case ".jpg", ".jpeg":
		if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
			return readImageKind{mediaType: "image/jpeg", ext: ".jpg"}, true
		}
	case ".gif":
		if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
			return readImageKind{mediaType: "image/gif", ext: ".gif"}, true
		}
	case ".webp":
		if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
			return readImageKind{mediaType: "image/webp", ext: ".webp"}, true
		}
	}
	return readImageKind{}, false
}

func readImageResult(workDir string, source []byte, kind readImageKind) (Result, error) {
	artifactData := source
	width, height := imageConfigDimensions(kind.mediaType, source)
	downsampled := false
	if omitted, reason := shouldOmitReadImage(kind.mediaType, len(source), width, height); omitted {
		return Result{Text: readImageOmittedSummary(kind.mediaType, len(source), width, height, reason)}, nil
	}
	if shouldDownsampleImage(kind.mediaType, len(source), width, height) {
		resized, resizedWidth, resizedHeight, ok, err := downsampleReadImage(source, kind.mediaType, readImageMaxSide)
		if err == nil && ok {
			artifactData = resized
			width = resizedWidth
			height = resizedHeight
			downsampled = true
		}
	}
	if len(artifactData) > readImageMaxBytes {
		reason := fmt.Sprintf("artifact byte size %d exceeds limit %d after downsampling", len(artifactData), readImageMaxBytes)
		return Result{Text: readImageOmittedSummary(kind.mediaType, len(artifactData), width, height, reason)}, nil
	}
	sum := sha256.Sum256(artifactData)
	sha := hex.EncodeToString(sum[:])
	artifactPath := filepath.ToSlash(filepath.Join(".juex", "artifacts", "media", "read", sha+kind.ext))
	root, err := mediaArtifactRoot(workDir)
	if err != nil {
		return Result{}, err
	}
	absArtifact := filepath.Join(root, filepath.FromSlash(artifactPath))
	if err := writeContentAddressedArtifact(absArtifact, artifactData); err != nil {
		return Result{}, err
	}
	media := llm.MediaRef{
		ArtifactPath:  artifactPath,
		MediaType:     kind.mediaType,
		SHA256:        sha,
		OriginalBytes: len(source),
		Width:         width,
		Height:        height,
	}
	return Result{
		Text:       readImageSummary(media, len(artifactData), downsampled),
		Structured: MediaResult{Media: media},
	}, nil
}

func writeContentAddressedArtifact(path string, data []byte) error {
	if ok, err := contentAddressedArtifactMatches(path, data); err == nil && ok {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		ok, verifyErr := contentAddressedArtifactMatches(path, data)
		if verifyErr != nil {
			return verifyErr
		}
		if ok {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return nil
}

func contentAddressedArtifactMatches(path string, data []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Equal(existing, data), nil
}

func mediaArtifactRoot(workDir string) (string, error) {
	if strings.TrimSpace(workDir) != "" {
		return workDir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd, nil
}

func imageConfigDimensions(mediaType string, data []byte) (int, int) {
	if mediaType == "image/webp" {
		return webpDimensions(data)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func shouldDownsampleImage(mediaType string, bytesLen, width, height int) bool {
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return false
	}
	if width <= 0 || height <= 0 {
		return false
	}
	return bytesLen > readImageMaxBytes || width > readImageMaxSide || height > readImageMaxSide
}

func shouldOmitReadImage(mediaType string, bytesLen, width, height int) (bool, string) {
	if imagePixelsExceedLimit(width, height) {
		return true, fmt.Sprintf("pixel count %d exceeds safe limit %d", imagePixelCount(width, height), readImageMaxPixels)
	}
	switch mediaType {
	case "image/png", "image/jpeg":
		if width <= 0 || height <= 0 {
			return true, fmt.Sprintf("dimensions unknown for %s", mediaType)
		}
		return false, ""
	case "image/gif", "image/webp":
		if bytesLen > readImageMaxBytes {
			return true, fmt.Sprintf("byte size %d exceeds limit %d for non-downsampled %s", bytesLen, readImageMaxBytes, mediaType)
		}
		if width <= 0 || height <= 0 {
			return true, fmt.Sprintf("dimensions unknown for %s", mediaType)
		}
		if width > readImageMaxSide || height > readImageMaxSide {
			return true, fmt.Sprintf("dimensions %dx%d exceed limit %d for non-downsampled %s", width, height, readImageMaxSide, mediaType)
		}
	}
	return false, ""
}

func webpDimensions(data []byte) (int, int) {
	if len(data) < 20 || !bytes.Equal(data[:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WEBP")) {
		return 0, 0
	}
	for offset := 12; offset+8 <= len(data); {
		chunkType := string(data[offset : offset+4])
		chunkStart := offset + 8
		chunkSizeRaw := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		if uint64(chunkSizeRaw) > uint64(len(data)-chunkStart) {
			return 0, 0
		}
		chunkSize := int(chunkSizeRaw)
		chunkEnd := chunkStart + chunkSize
		chunk := data[chunkStart:chunkEnd]
		switch chunkType {
		case "VP8X":
			if len(chunk) >= 10 {
				return 1 + int(readLittleEndian24(chunk[4:7])), 1 + int(readLittleEndian24(chunk[7:10]))
			}
		case "VP8L":
			if len(chunk) >= 5 && chunk[0] == 0x2f {
				bits := binary.LittleEndian.Uint32(chunk[1:5])
				width := 1 + int(bits&0x3fff)
				height := 1 + int((bits>>14)&0x3fff)
				return width, height
			}
		case "VP8 ":
			if len(chunk) >= 10 && bytes.Equal(chunk[3:6], []byte{0x9d, 0x01, 0x2a}) {
				width := int(binary.LittleEndian.Uint16(chunk[6:8]) & 0x3fff)
				height := int(binary.LittleEndian.Uint16(chunk[8:10]) & 0x3fff)
				return width, height
			}
		}
		offset = chunkEnd
		if chunkSize%2 == 1 {
			offset++
		}
	}
	return 0, 0
}

func readLittleEndian24(data []byte) uint32 {
	if len(data) < 3 {
		return 0
	}
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
}

func imagePixelsExceedLimit(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	return width > readImageMaxPixels/height
}

func imagePixelCount(width, height int) int64 {
	if width <= 0 || height <= 0 {
		return 0
	}
	return int64(width) * int64(height)
}

func downsampleReadImage(data []byte, mediaType string, maxSide int) ([]byte, int, int, bool, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, false, err
	}
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, 0, 0, false, fmt.Errorf("invalid image dimensions %dx%d", width, height)
	}
	scale := math.Min(float64(maxSide)/float64(width), float64(maxSide)/float64(height))
	if scale > 1 {
		scale = 1
	}
	newWidth := max(1, int(math.Round(float64(width)*scale)))
	newHeight := max(1, int(math.Round(float64(height)*scale)))
	dst := src
	if newWidth != width || newHeight != height {
		dst = resizeNearest(src, newWidth, newHeight)
	}
	var out bytes.Buffer
	switch mediaType {
	case "image/jpeg":
		if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
			return nil, 0, 0, false, err
		}
	case "image/png":
		if err := png.Encode(&out, dst); err != nil {
			return nil, 0, 0, false, err
		}
	default:
		return nil, 0, 0, false, fmt.Errorf("unsupported downsample media type %s", mediaType)
	}
	return out.Bytes(), newWidth, newHeight, true, nil
}

func resizeNearest(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sy := bounds.Min.Y + int(float64(y)*float64(bounds.Dy())/float64(height))
		if sy >= bounds.Max.Y {
			sy = bounds.Max.Y - 1
		}
		for x := 0; x < width; x++ {
			sx := bounds.Min.X + int(float64(x)*float64(bounds.Dx())/float64(width))
			if sx >= bounds.Max.X {
				sx = bounds.Max.X - 1
			}
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func readImageSummary(media llm.MediaRef, artifactBytes int, downsampled bool) string {
	dimensions := "unknown"
	if media.Width > 0 && media.Height > 0 {
		dimensions = fmt.Sprintf("%dx%d", media.Width, media.Height)
	}
	summary := fmt.Sprintf("[image %s, %d bytes, %s]", dimensions, artifactBytes, media.MediaType)
	if downsampled {
		summary = fmt.Sprintf("[image %s, %d bytes, %s, downsampled from %d bytes]", dimensions, artifactBytes, media.MediaType, media.OriginalBytes)
	}
	return summary
}

func readImageOmittedSummary(mediaType string, bytesLen, width, height int, reason string) string {
	dimensions := "unknown"
	if width > 0 && height > 0 {
		dimensions = fmt.Sprintf("%dx%d", width, height)
	}
	return fmt.Sprintf("[image omitted: %s, %d bytes, %s: %s]", dimensions, bytesLen, mediaType, reason)
}
