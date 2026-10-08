package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func readAgentInstructions(ctx context.Context, directory, global string, writer io.Writer) error {
	paths, err := execprotocol.InstructionPaths(directory, global)
	if err != nil {
		return err
	}
	snapshot := execprotocol.InstructionSnapshot{}
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, err := readInstructionSource(name)
		if err != nil {
			return fmt.Errorf("instruction source %s: %w", name, err)
		}
		snapshot.Sources = append(snapshot.Sources, source)
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	// Publish only a complete snapshot; a later bad source must not leave a
	// successful prefix that recovery could mistake for all configured guidance.
	return json.NewEncoder(writer).Encode(snapshot)
}

func readInstructionSource(name string) (execprotocol.InstructionSource, error) {
	source := execprotocol.InstructionSource{Path: name, State: "missing"}
	file, err := openRegular(name, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return source, nil
	}
	if err != nil {
		return source, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return source, err
	}
	if !info.Mode().IsRegular() || info.Size() > execprotocol.MaxInstructionFileBytes {
		return source, errors.New("instruction source must be a regular file no larger than 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, execprotocol.MaxInstructionFileBytes+1))
	if err != nil {
		return source, err
	}
	if len(data) > execprotocol.MaxInstructionFileBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return source, errors.New("instruction source must contain bounded UTF-8 text")
	}
	source.State, source.Text, source.Size, source.SHA256 = "loaded", string(data), int64(len(data)), execprotocol.FileDigest(data)
	if strings.TrimSpace(source.Text) == "" {
		source.State = "empty"
	}
	return source, nil
}
