package agentpolicy

import (
	"slices"
	"testing"
)

func TestPolicyValidationAndDefaults(t *testing.T) {
	for _, capability := range []Capability{Files, Shell, Workers, Collaboration, MCP, Observations, Memory, Calendar, Hooks, Extensions} {
		if !(Policy{}).Allows(capability) {
			t.Fatalf("default policy denies %s", capability)
		}
		policy := Policy{Disabled: []Capability{capability}}
		if err := policy.Validate(); err != nil || policy.Allows(capability) {
			t.Fatalf("disabled %s remains permitted: %v", capability, err)
		}
	}
	if (Policy{}).Allows("unknown") {
		t.Fatal("unknown capability allowed")
	}
	for _, policy := range []Policy{{Disabled: []Capability{"unknown"}}, {Disabled: []Capability{Memory, Memory}}} {
		if policy.Validate() == nil {
			t.Fatal("invalid policy accepted", policy)
		}
	}
}

func TestNormalizationAndRestriction(t *testing.T) {
	before := Policy{Disabled: []Capability{Workers, Memory}}
	after := before.Normalized()
	if !slices.Equal(after.Disabled, []Capability{Memory, Workers}) || after.Restricts(before) || before.Restricts(after) {
		t.Fatal("ordering changes authority", before, after)
	}
	after.Disabled[0] = Shell
	if before.Disabled[0] != Workers || before.Disabled[1] != Memory {
		t.Fatal("normalized policy aliases its input")
	}
	if !after.Restricts(before) || (Policy{}).Restricts(before) || before.Restricts(before) {
		t.Fatal("policy restriction must detect newly disabled capabilities only")
	}
	if (Policy{}).Normalized().Disabled == nil {
		t.Fatal("normalized policy must encode an empty array")
	}
}
