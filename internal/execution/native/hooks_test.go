package native

import (
	"io"
	"strings"
	"testing"
)

func TestWorkerDiagnosticBoundSurvivesIOCopy(t *testing.T) {
	var output workerError
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 32768))); err != nil {
		t.Fatal(err)
	}
	if len(output.String()) != 8192 {
		t.Fatal("child diagnostic exceeded bound", len(output.String()))
	}
}
