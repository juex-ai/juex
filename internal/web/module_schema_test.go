package web

import (
	"bytes"
	"os"
	"testing"
)

func TestModuleSchemaMatchesTypeScript(t *testing.T) {
	want, err := GenerateModuleTypeScript()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../frontend/src/module-schema.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("module contract changed: run go run ./scripts/gen-module-schema")
	}
}
