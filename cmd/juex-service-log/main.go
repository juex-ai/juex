package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/juex-ai/juex/internal/foundation/servicelog"
	"github.com/spf13/cobra"
)

func runService(args []string, directory string, waitDelay time.Duration) error {
	logs, err := servicelog.New(directory)
	if err != nil {
		return err
	}
	return runLoggedService(args, logs, waitDelay)
}

type processLogs interface {
	io.Writer
	Prune() error
	Close() error
}

func runLoggedService(args []string, sink processLogs, waitDelay time.Duration) error {
	logs := servicelog.NewBuffered(sink)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitDelay)
		defer cancel()
		if err := logs.Close(ctx); err != nil {
			// A log outage must neither change a clean service exit into a
			// restart-on-failure nor hang exit on a blocked supervisor pipe.
			done := make(chan struct{})
			go func() {
				fmt.Fprintln(os.Stderr, "process logs:", err)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	child := exec.Command(args[0], args[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, logs, logs
	// Bound pipe draining only after the main process exits. This never adds
	// a kill deadline to a running service's graceful shutdown.
	child.WaitDelay = waitDelay
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	if err := child.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	for {
		select {
		case err := <-exited:
			return err
		case sig := <-signals:
			if err := child.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
		}
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
	}
	return 1
}

func main() {
	var directory string
	root := &cobra.Command{Use: "juex-service-log --directory PATH -- COMMAND [ARGS...]", Short: "Run a trusted service with seven-day, 70 MiB bounded process logs", Args: cobra.MinimumNArgs(1), SilenceUsage: true, SilenceErrors: true}
	root.Flags().StringVar(&directory, "directory", "/run/juex/logs", "Private service log directory")
	root.RunE = func(_ *cobra.Command, args []string) error { return runService(args, directory, 5*time.Second) }
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}
