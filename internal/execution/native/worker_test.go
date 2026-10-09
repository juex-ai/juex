package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_TEST_FILE_HELPER") != "1" {
		return
	}
	if err := os.WriteFile("effect", []byte("once"), 0600); err != nil {
		os.Exit(2)
	}
	fmt.Print("complete output")
	if os.Getenv("JUEX_TEST_HELPER_BLOCK") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}

func testFileHelper(t *testing.T, ctx context.Context, extra ...string) (*exec.Cmd, string) {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "JUEX_TEST_FILE_HELPER=1", "GORACE=atexit_sleep_ms=0")
	cmd, err := fileHelperCommand(ctx, executable, directory, append(environment, extra...), nil, "-test.run=^TestFileHelperProcess$")
	if err != nil {
		t.Fatal(err)
	}
	return cmd, directory
}

type slowHelperOutput struct {
	buffer bytes.Buffer
	ready  chan struct{}
	err    error
}

func (w *slowHelperOutput) Write(p []byte) (int, error) {
	if w.ready != nil {
		close(w.ready)
		w.ready = nil
	}
	// The child exits while durable output or a transfer receiver is still busy.
	time.Sleep(2500 * time.Millisecond)
	if w.err != nil {
		return 0, w.err
	}
	return w.buffer.Write(p)
}

func TestFileHelperWaitsForOutputAfterExit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("writer_error=%v", fail), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd, directory := testFileHelper(t, ctx)
			output := &slowHelperOutput{}
			if fail {
				output.err = errors.New("receiver failed")
			}
			cmd.Stdout = output
			err := cmd.Run()
			if !errors.Is(err, output.err) {
				t.Fatalf("helper output result: got %v, want %v", err, output.err)
			}
			if !cmd.ProcessState.Success() {
				t.Fatal("helper did not exit successfully")
			}
			if !fail && output.buffer.String() != "complete output" {
				t.Fatalf("incomplete output: %q", output.buffer.String())
			}
			data, err := os.ReadFile(filepath.Join(directory, "effect"))
			if err != nil || string(data) != "once" {
				t.Fatalf("helper effect: %q, %v", data, err)
			}
		})
	}
}

func TestFileHelperCancellationStopsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, _ := testFileHelper(t, ctx, "JUEX_TEST_HELPER_BLOCK=1")
	ready := make(chan struct{})
	cmd.Stdout = &slowHelperOutput{ready: ready}
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
			t.Fatalf("cancelled helper result: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled helper did not stop")
	}
}
