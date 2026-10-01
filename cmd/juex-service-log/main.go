package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/juex-ai/juex/internal/foundation/servicelog"
	"github.com/spf13/cobra"
)

func runService(args []string, directory string, waitDelay time.Duration) (result error) {
	logs, err := servicelog.New(directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, logs.Close()) }()
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
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		select {
		case err := <-exited:
			return err
		case sig := <-signals:
			if err := child.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return err
			}
		case <-timer.C:
			if err := logs.Prune(); err != nil {
				fmt.Fprintln(os.Stderr, "process log retention:", err)
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
