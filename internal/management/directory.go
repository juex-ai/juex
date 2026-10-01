// Package management owns platform identities and tenant resource authorization.
package management

import (
	"errors"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrInvalid         = errors.New("invalid management request")
	ErrDenied          = errors.New("resource unavailable or access denied")
	ErrConflict        = errors.New("management resource already exists")
	ErrLastAdmin       = errors.New("tenant must retain an active administrator")
	ErrInvitation      = errors.New("invitation unavailable")
	ErrCredentials     = errors.New("invalid email or password")
	ErrSession         = errors.New("authentication required")
	ErrLoginRequired   = errors.New("account already exists; sign in to accept the invitation")
	ErrRateLimit       = errors.New("too many attempts; try again later")
	ErrMailUnavailable = errors.New("email delivery is not configured")
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
	ID            string `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

type Tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Membership struct {
	TenantID       string           `json:"tenant_id"`
	UserID         string           `json:"user_id"`
	Role           Role             `json:"role"`
	Status         MembershipStatus `json:"status"`
	Version        int64            `json:"version"`
	ExecutionEpoch int64            `json:"-"`
}

type Fleet struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
}

type Invitation struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Session struct {
	User      User      `json:"user"`
	Token     string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}

type TenantAccess struct {
	Tenant
	Role    Role   `json:"role"`
	FleetID string `json:"fleet_id"`
}

type MemberView struct {
	User
	Membership
	FleetID string `json:"fleet_id"`
}

type InvitationView struct {
	Invitation
	Link           string `json:"link"`
	DeliveryStatus string `json:"delivery_status"`
}

type InvitationPreview struct {
	Invitation
	TenantName    string `json:"tenant_name"`
	RequiresLogin bool   `json:"requires_login"`
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
