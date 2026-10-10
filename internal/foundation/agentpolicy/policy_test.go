package agentpolicy

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestResourcePolicyPreservesFrozenPermissions(t *testing.T) {
	for _, parent := range []Capability{Files, Extensions} {
		resource := FileSearch
		if parent == Extensions {
			resource = Skills
		}
		old := Policy{Disabled: []Capability{parent}}
		if old.Allows(resource) || !(Policy{}).Allows(resource) {
			t.Fatal("changed historical resource permission", parent)
		}
		current := Policy{Version: 1, Disabled: []Capability{parent}}
		if !current.Allows(resource) {
			t.Fatal("independent resource remains coupled", parent)
		}
		current.Disabled = append(current.Disabled, resource)
		if current.Allows(resource) {
			t.Fatal("explicit resource disable ignored")
		}
		encoded, _ := json.Marshal(old.Normalized())
		if string(encoded) != `{"disabled":["`+string(parent)+`"]}` {
			t.Fatalf("historical encoding changed: %s", encoded)
		}
	}
	if (Policy{Version: 2}).Validate() == nil || (Policy{Version: 2}).Allows(Files) {
		t.Fatal("unknown policy format accepted")
	}
	for _, old := range []Policy{{SkillSources: true}, {Disabled: []Capability{Skills}}, {Disabled: []Capability{FileSearch}}} {
		if old.Validate() == nil {
			t.Fatal("historical format accepted new permissions", old)
		}
	}
}

func TestOptionalPolicyDoesNotExpandFrozenAuthority(t *testing.T) {
	var old Policy
	if err := json.Unmarshal([]byte(`{"disabled":[]}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Allows(ApplyPatch) {
		t.Fatal("upgrade enabled optional tool")
	}
	enabled := Policy{Enabled: []Capability{ApplyPatch}}
	if err := enabled.Validate(); err != nil || !enabled.Allows(ApplyPatch) {
		t.Fatal(enabled, err)
	}
	if !old.Restricts(enabled) || enabled.Restricts(old) {
		t.Fatal("optional capability revocation is not fenced")
	}
	for _, invalid := range []Policy{{Enabled: []Capability{Files}}, {Enabled: []Capability{ApplyPatch, ApplyPatch}}, {Enabled: []Capability{ApplyPatch}, Disabled: []Capability{ApplyPatch}}} {
		if invalid.Validate() == nil {
			t.Fatal("invalid optional declaration accepted", invalid)
		}
	}
	encoded, _ := json.Marshal(old.Normalized())
	if string(encoded) != `{"disabled":[]}` {
		t.Fatalf("changed frozen empty policy: %s", encoded)
	}
	copy := enabled.Normalized()
	copy.Enabled[0] = Files
	if enabled.Enabled[0] != ApplyPatch {
		t.Fatal("optional grants alias source")
	}
}

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
