//go:build linux || darwin

package native

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func fileIdentity(info os.FileInfo) string {
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another executor owns this state directory")
	}
	return file, nil
}

func openRegular(path string, flags int, permission os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, flags|unix.O_NONBLOCK, permission)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("file tools require a regular file")
	}
	return file, nil
}

func openRootRegular(root *os.Root, path string) (*os.File, error) {
	file, err := root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("file preview requires a regular file")
	}
	return file, nil
}

func startTerminal(cmd *exec.Cmd, output io.Writer, outputLimit int64, cancel func()) (io.WriteCloser, func() error, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		return nil, nil, err
	}
	// Reopen a nonblocking descriptor so Go can poll stdin writes on both
	// platforms. The original Darwin PTY is a blocking os.File.
	fd, err := unix.FcntlInt(terminal.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		err = unix.SetNonblock(fd, true)
		if err != nil {
			_ = unix.Close(fd)
		}
	}
	if err != nil {
		_ = terminal.Close()
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, err
	}
	pollable := os.NewFile(uintptr(fd), terminal.Name())
	_ = terminal.Close()
	terminal = pollable
	exited := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		err := copyTerminalOutput(terminal, output, outputLimit, exited)
		if err != nil {
			cancel()
		}
		// EOF may precede process exit when the command redirects its streams.
		// Closing the master here would send SIGHUP to that live command.
		done <- err
	}()
	return terminal, func() error {
		close(exited)
		return <-done
	}, nil
}

func copyTerminalOutput(terminal *os.File, output io.Writer, remaining int64, exited <-chan struct{}) error {
	buffer := make([]byte, 32<<10)
	raw, err := terminal.SyscallConn()
	if err != nil {
		return err
	}
	finished, truncated := false, false
	for {
		select {
		case <-exited:
			finished = true
		default:
		}
		if finished && truncated {
			return nil
		}
		timeout, size := 100, len(buffer)
		if finished {
			// Finish each durable write before draining the remaining PTY data.
			// A descendant may still hold or write the terminal: never wait for
			// new data, and read at most the output quota plus one truncation byte.
			timeout = 0
			if int64(size) > remaining {
				size = int(remaining) + 1
			}
		}
		var ready, n int
		var readErr error
		err := raw.Control(func(fd uintptr) {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			ready, readErr = unix.Poll(poll, timeout)
			if readErr == nil && ready > 0 {
				n, readErr = unix.Read(int(fd), buffer[:size])
				if n == 0 && readErr == nil {
					readErr = io.EOF
				}
			}
		})
		if err != nil {
			return err
		}
		if errors.Is(readErr, unix.EAGAIN) && finished {
			return nil
		}
		if errors.Is(readErr, unix.EINTR) || errors.Is(readErr, unix.EAGAIN) {
			continue
		}
		if ready == 0 && readErr == nil {
			if finished {
				return nil
			}
			continue
		}
		if n > 0 {
			written, err := output.Write(buffer[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
			if int64(n) > remaining {
				remaining, truncated = 0, true
			} else {
				remaining -= int64(n)
			}
		}
		// Linux reports EIO when the slave closes; macOS reports EOF.
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, unix.EIO) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
