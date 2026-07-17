package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	maxLogBytes    = 1 << 20
	rotateLogBytes = 10 << 20
	truncatedLine  = "[fleet log output truncated]\n"
)

func logsDir(agentDir string) string {
	return filepath.Join(agentDir, "logs")
}

func fleetLogPath(agentDir string) string {
	return filepath.Join(logsDir(agentDir), "fleet.log")
}

func (m *Manager) Logs(selector string, lines int) ([]byte, error) {
	if lines < 1 || lines > 10_000 {
		return nil, fmt.Errorf("fleet: --lines must be between 1 and 10000")
	}
	entries, err := m.deps.listRegistry(m.homeDir)
	if err != nil {
		return nil, err
	}
	entry, err := resolveSelector(entries, selector)
	if err != nil {
		return nil, err
	}
	return tailLog(fleetLogPath(entry.Dir), lines)
}

func tailLog(path string, lines int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	readSize := size
	truncated := false
	if readSize > maxLogBytes {
		readSize = maxLogBytes
		truncated = true
	}
	if _, err := file.Seek(size-readSize, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, readSize))
	if err != nil {
		return nil, err
	}
	if truncated {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	data = lastLines(data, lines)
	if !truncated {
		return data, nil
	}
	limit := maxLogBytes - len(truncatedLine)
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return append([]byte(truncatedLine), data...), nil
}

func lastLines(data []byte, lines int) []byte {
	if len(data) == 0 {
		return data
	}
	end := len(data)
	if data[end-1] == '\n' {
		end--
	}
	start := end
	for count := 0; start > 0; {
		start--
		if data[start] == '\n' {
			count++
			if count == lines {
				start++
				break
			}
		}
	}
	return data[start:]
}

func rotateFleetLog(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= rotateLogBytes {
		return nil
	}
	rotated := path + ".1"
	if err := os.Remove(rotated); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(path, rotated)
}
