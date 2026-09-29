package management

import (
	"errors"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"  Person@Example.COM ", "person@example.com"},
		{"person+project@example.com", "person+project@example.com"},
		{"Person <person@example.com>", ""},
		{"not-an-address", ""},
		{"", ""},
	} {
		got, err := NormalizeEmail(tc.input)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("NormalizeEmail(%q) = %q, %v", tc.input, got, err)
		}
	}
}

func TestRemovedMemberRequiresInvitation(t *testing.T) {
	member := Membership{Role: Member, Status: Removed}
	for _, status := range []MembershipStatus{Active, Suspended} {
		if !errors.Is(ValidateMemberChange(member, Member, status), ErrInvalid) {
			t.Fatalf("removed member can transition directly to %s", status)
		}
	}
	if err := ValidateMemberChange(member, Member, Removed); err != nil {
		t.Fatal(err)
	}
}
