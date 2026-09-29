// Package management owns platform identities and tenant resource authorization.
package management

import (
	"errors"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrInvalid    = errors.New("invalid management request")
	ErrDenied     = errors.New("resource unavailable or access denied")
	ErrConflict   = errors.New("management resource already exists")
	ErrLastAdmin  = errors.New("tenant must retain an active administrator")
	ErrInvitation = errors.New("invitation unavailable")
)

type Role string

const (
	Admin  Role = "admin"
	Member Role = "member"
)

func (r Role) Valid() bool { return r == Admin || r == Member }

type MembershipStatus string

const (
	Active    MembershipStatus = "active"
	Suspended MembershipStatus = "suspended"
	Removed   MembershipStatus = "removed"
)

type User struct {
	ID            string
	Email         string
	EmailVerified bool
}

type Tenant struct {
	ID   string
	Name string
}

type Membership struct {
	TenantID string
	UserID   string
	Role     Role
	Status   MembershipStatus
	Version  int64
}

type Fleet struct {
	ID       string
	TenantID string
	UserID   string
}

type Invitation struct {
	ID        string
	TenantID  string
	Email     string
	Role      Role
	ExpiresAt time.Time
}

// NormalizeEmail defines account equality without provider-specific alias rules.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return "", ErrInvalid
	}
	return email, nil
}

// ValidateMemberChange is for administrative edits. Rejoining a removed
// membership requires accepting a fresh invitation, never this operation.
func ValidateMemberChange(before Membership, role Role, status MembershipStatus) error {
	if !role.Valid() || (status != Active && status != Suspended && status != Removed) {
		return ErrInvalid
	}
	if before.Status == Removed && (status != Removed || role != before.Role) {
		return ErrInvalid
	}
	return nil
}
