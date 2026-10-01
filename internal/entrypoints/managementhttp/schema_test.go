package managementhttp

import (
	"bytes"
	"os"
	"testing"
)

func TestWebContract(t *testing.T) {
	want, err := GenerateTypeScript()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../../frontend/src/management/schema.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Web contract is stale; run go run ./scripts/gen-management-schema")
	}
}
