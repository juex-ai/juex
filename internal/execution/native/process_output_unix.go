//go:build linux || darwin

package native

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// Process pipes belong to the executor so Cmd.Wait cannot close buffered output
// while its receiver is persisting an earlier chunk. The post-exit bound applies
// to descendants retaining writers, independently of durable output delivery.
func startProcessOutput(cmd *exec.Cmd, stdout, stderr io.Writer, cancel func()) (func() error, error) {
	type stream struct {
		reader, writer *os.File
		output         io.Writer
		done           chan error
	}
	streams := make([]stream, 0, 2)
	closeFiles := func() {
		for _, stream := range streams {
			_ = stream.reader.Close()
			_ = stream.writer.Close()
		}
	}
	for _, output := range []io.Writer{stdout, stderr} {
		reader, writer, err := os.Pipe()
		if err != nil {
			closeFiles()
			return nil, err
		}
		streams = append(streams, stream{reader, writer, output, make(chan error, 1)})
	}
	cmd.Stdout, cmd.Stderr = streams[0].writer, streams[1].writer
	if err := cmd.Start(); err != nil {
		closeFiles()
		return nil, err
	}
	for _, stream := range streams {
		_ = stream.writer.Close()
		go func() {
			_, err := io.Copy(stream.output, stream.reader)
			if err != nil {
				cancel()
			}
			stream.done <- err
		}()
	}
	return func() error {
		deadline := time.Now().Add(2 * time.Second)
		done := make(chan error, len(streams))
		for _, stream := range streams {
			go func() {
				err := waitPipeWriters(stream.reader, deadline)
				if err != nil {
					_ = stream.reader.Close()
				}
				copyErr := <-stream.done
				_ = stream.reader.Close()
				done <- errors.Join(err, copyErr)
			}()
		}
		var errs []error
		for range streams {
			errs = append(errs, <-done)
		}
		return errors.Join(errs...)
	}, nil
}

func waitPipeWriters(reader *os.File, deadline time.Time) error {
	raw, err := reader.SyscallConn()
	if err != nil {
		return err
	}
	for {
		remaining := max(time.Until(deadline), 0)
		var pollErr error
		var events int16
		// Keep the descriptor alive throughout poll; only the owner closes it
		// after monitoring and copying finish. HUP is visible with unread data.
		err := raw.Control(func(fd uintptr) {
			// Darwin reports pipe HUP only when read events are requested.
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, pollErr = unix.Poll(poll, int((remaining+time.Millisecond-1)/time.Millisecond))
			events = poll[0].Revents
		})
		if err != nil {
			return err
		}
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		if pollErr != nil {
			return pollErr
		}
		if events&unix.POLLNVAL != 0 {
			return unix.EBADF
		}
		if events&unix.POLLHUP != 0 {
			return nil
		}
		if events&unix.POLLERR != 0 {
			return unix.EIO
		}
		if !time.Now().Before(deadline) {
			return exec.ErrWaitDelay
		}
		// Buffered output is readable while its receiver is busy. Do not spin
		// on POLLIN; the fixed deadline still bounds continuously writing heirs.
		time.Sleep(min(10*time.Millisecond, time.Until(deadline)))
	}
}
