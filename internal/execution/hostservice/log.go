package hostservice

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const logLimit = 2 << 20
const logFiles = 7

type Log struct {
	mu        sync.Mutex
	directory string
	file      *os.File
	size      int64
	day       string
}

func OpenLog(directory string) (*Log, error) {
	l := &Log{directory: directory}
	for i := 0; i <= logFiles; i++ {
		path := l.path(i)
		if info, err := os.Lstat(path); err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
		}
	}
	return l, l.open()
}
func (l *Log) path(index int) string {
	if index == 0 {
		return filepath.Join(l.directory, "executor.log")
	}
	return filepath.Join(l.directory, fmt.Sprintf("executor.log.%d", index))
}
func (l *Log) open() error {
	path := l.path(0)
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("executor log must be a private regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	l.file = file
	l.size = info.Size()
	l.day = info.ModTime().UTC().Format(time.DateOnly)
	return nil
}
func (l *Log) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	if len(data) > logLimit {
		return 0, errors.New("executor log record exceeds limit")
	}
	if l.size+int64(len(data)) > logLimit || l.day != time.Now().UTC().Format(time.DateOnly) {
		if err := l.file.Close(); err != nil {
			return 0, err
		}
		l.file = nil
		if err := os.Remove(l.path(logFiles)); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		for i := logFiles - 1; i >= 0; i-- {
			if info, err := os.Lstat(l.path(i)); err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
				if err := os.Remove(l.path(i)); err != nil {
					return 0, err
				}
				continue
			}
			if err := os.Rename(l.path(i), l.path(i+1)); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.file.Write(data)
	l.size += int64(n)
	return n, err
}
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func Logs(directory string, lines int) (string, error) {
	if lines < 1 || lines > 2000 {
		return "", errors.New("--tail must be between 1 and 2000")
	}
	path := filepath.Join(directory, "executor.log")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if time.Since(info.ModTime()) > 7*24*time.Hour {
		return "", nil
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("executor log must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	start := max(int64(0), info.Size()-(1<<20))
	data, err := io.ReadAll(io.NewSectionReader(file, start, 1<<20))
	if err != nil {
		return "", err
	}
	parts := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if start > 0 && len(parts) > 0 {
		parts = parts[1:]
	}
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return string(bytes.Join(parts, []byte{'\n'})), nil
}
