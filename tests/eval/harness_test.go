package eval

import (
	"os/exec"
	"testing"
)

func TestManagedValidationHarness(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("uv", "run", "python", "-m", "unittest", "tests.eval.test_harness")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("managed validation harness: %v\n%s", err, output)
	}
}
