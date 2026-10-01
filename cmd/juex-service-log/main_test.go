package main

import (
	"runtime"
	"testing"
	"time"
)

func TestChildFailureRetainsSignalAndDoesNotWaitForDescendantPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux deployment wrapper")
	}
	start := time.Now()
	err := runService([]string{"sh", "-c", "sleep 2 & exit 7"}, t.TempDir(), 50*time.Millisecond)
	if exitCode(err) != 7 || time.Since(start) > time.Second {
		t.Fatal("child failure hidden by descendant pipe", err, time.Since(start))
	}
	err = runService([]string{"sh", "-c", "kill -KILL $$"}, t.TempDir(), 50*time.Millisecond)
	if exitCode(err) != 137 {
		t.Fatal("crash mistaken for graceful termination", err, exitCode(err))
	}
}
