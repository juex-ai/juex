//go:build !unix

package cancellation

import (
	"os"
	"strconv"
	"syscall"
)

func DefaultSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func describeSignal(sig os.Signal) (string, int) {
	switch sig {
	case os.Interrupt:
		return "SIGINT", 2
	case syscall.SIGTERM:
		return "SIGTERM", 15
	}
	if s, ok := sig.(syscall.Signal); ok {
		return "SIG" + strconv.Itoa(int(s)), int(s)
	}
	return sig.String(), 0
}

func isInterruptSignal(sig os.Signal, name string, number int) bool {
	return sig == os.Interrupt || name == "SIGINT" || number == 2
}
