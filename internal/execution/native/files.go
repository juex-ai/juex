package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type FileArguments struct {
	Path             string `json:"path"`
	WorkingDirectory string `json:"working_directory"`
	Content          string `json:"content"`
	OldText          string `json:"old_text"`
	NewText          string `json:"new_text"`
	Pattern          string `json:"pattern"`
	Offset           int64  `json:"offset"`
	Limit            int    `json:"limit"`
}

func (e *Engine) fileOperation(ctx context.Context, operation *operation) error {
	var args FileArguments
	if err := decodeArguments(operation.record.Request.Arguments, &args); err != nil {
		return err
	}
	if e.config.ProcessUser != nil {
		return e.fileWorker(ctx, operation)
	}
	directory, err := e.workingDirectory(args.WorkingDirectory)
	if err != nil {
		return err
	}
	return RunFileOperation(ctx, directory, operation.record.Request.Kind, args, outputWriter{engine: e, operation: operation})
}

// RunFileOperation executes under its caller's OS identity. Hosted execution
// invokes it in a separate unprivileged process, never in the control process.
func RunFileOperation(ctx context.Context, directory, kind string, args FileArguments, writer io.Writer) error {
	if !filepath.IsAbs(directory) {
		return execprotocol.ErrInvalid
	}
	path := args.Path
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(directory, path)
	}
	if args.Offset < 0 || args.Limit < 0 || args.Limit > 1<<20 {
		return execprotocol.ErrInvalid
	}
	if args.Limit == 0 {
		args.Limit = 64 << 10
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch kind {
	case "read_agent_instructions":
		return readAgentInstructions(ctx, directory, args.Path, writer)
	case "inspect_extension":
		return inspectExtension(ctx, path, writer)
	case "read":
		file, err := openRegular(path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("read requires a regular file")
		}
		_, err = io.Copy(writer, io.NewSectionReader(file, args.Offset, int64(args.Limit)))
		return err
	case "write":
		// New Threads create working directories on their first real write.
		// This stays inside the existing journaled file operation and identity.
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		file, err := openRegular(path, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return err
		}
		_, writeErr := io.WriteString(file, args.Content)
		syncErr, closeErr := file.Sync(), file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		_, err = fmt.Fprint(writer, "Written ", path)
		return err
	case "edit":
		if args.OldText == "" {
			return execprotocol.ErrInvalid
		}
		file, err := openRegular(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return errors.New("edit requires a regular file no larger than 8 MiB")
		}
		data, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		if strings.Count(string(data), args.OldText) != 1 {
			return errors.New("old_text must match exactly once")
		}
		updated := strings.Replace(string(data), args.OldText, args.NewText, 1)
		if _, err := file.WriteAt([]byte(updated), 0); err != nil {
			return err
		}
		if err := file.Truncate(int64(len(updated))); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		_, err = fmt.Fprint(writer, "Edited ", path)
		return err
	case "glob", "grep":
		return searchFiles(ctx, path, kind, args.Pattern, args.Limit, writer)
	default:
		return execprotocol.ErrInvalid
	}
}

func searchFiles(ctx context.Context, root, kind, pattern string, limit int, output io.Writer) error {
	if pattern == "" {
		return execprotocol.ErrInvalid
	}
	limit = min(limit, 2000)
	var matcher *regexp.Regexp
	if kind == "grep" {
		var err error
		matcher, err = regexp.Compile(pattern)
		if err != nil {
			return err
		}
	} else if _, err := filepath.Match(pattern, ""); err != nil {
		return err
	}
	count := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if kind == "glob" {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			matched, _ := filepath.Match(pattern, relative)
			if !matched {
				matched, _ = filepath.Match(pattern, entry.Name())
			}
			if matched {
				count++
				if err := json.NewEncoder(output).Encode(map[string]string{"path": path}); err != nil {
					return err
				}
			}
		} else {
			file, err := openRegular(path, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				return err
			}
			scanner := bufio.NewScanner(io.LimitReader(file, 16<<20))
			scanner.Buffer(make([]byte, 64<<10), 1<<20)
			line := 0
			for scanner.Scan() {
				if err := ctx.Err(); err != nil {
					return err
				}
				line++
				if matcher.Match(scanner.Bytes()) {
					count++
					if err := json.NewEncoder(output).Encode(map[string]any{"path": path, "line": line, "text": scanner.Text()}); err != nil {
						return err
					}
				}
				if count >= limit {
					break
				}
			}
			if err := scanner.Err(); err != nil {
				return err
			}
			if info.Size() > 16<<20 {
				if err := json.NewEncoder(output).Encode(map[string]any{"path": path, "truncated": true, "reason": "file_size_limit"}); err != nil {
					return err
				}
			}
		}
		if count >= limit {
			if _, err := io.WriteString(output, "{\"truncated\":true,\"reason\":\"match_limit\"}\n"); err != nil {
				return err
			}
			return filepath.SkipAll
		}
		return nil
	})
}
