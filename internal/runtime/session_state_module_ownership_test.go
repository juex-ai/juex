package runtime

import (
	"reflect"
	"testing"
)

func TestFrameworkRuntimeDoesNotOwnGoalOrNotesStores(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
	}{
		{name: "Engine", typ: reflect.TypeOf(Engine{})},
		{name: "SessionRuntimeSnapshot", typ: reflect.TypeOf(SessionRuntimeSnapshot{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, field := range []string{"GoalState", "Notes"} {
				if _, ok := test.typ.FieldByName(field); ok {
					t.Fatalf("%s still owns feature store field %s", test.name, field)
				}
			}
		})
	}
}
