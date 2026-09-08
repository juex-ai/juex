package events

import (
	"reflect"
	"testing"
)

func TestCatalogRegistrationAndLookupAreImmutable(t *testing.T) {
	catalog, err := NewCatalog(Definition{
		Type:         "test.fact",
		Version:      4,
		ReplayPolicy: ReplayRequired,
		NewPayload: func() any {
			return &struct {
				Value string `json:"value"`
			}{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := catalog.Lookup("test.fact")
	if !ok || definition.Version != 4 {
		t.Fatalf("definition = %+v, ok = %v", definition, ok)
	}
	if _, err := NewCatalog(definition, definition); err == nil {
		t.Fatal("duplicate definition error = nil")
	}
	if !reflect.DeepEqual(catalog.BrowserTypes(), []string(nil)) {
		t.Fatalf("browser types = %v", catalog.BrowserTypes())
	}
}
