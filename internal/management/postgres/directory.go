package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
)

// Directory owns Management transactions. Actor IDs must come from a verified
// session, never a request body. Operator/identity methods have no public route.
type Config struct {
	Secrets     *secrets.Box
	PublicURL   string
	MailEnabled bool
}

type Directory struct {
	pool   *pgxpool.Pool
	config Config
}

func NewDirectory(pool *pgxpool.Pool, config Config) *Directory {
	return &Directory{pool: pool, config: config}
}

// CreateUser is an identity-provisioning primitive, not a public signup flow.
// Credentials and proof of email ownership are separate from the stable user.
func (d *Directory) CreateUser(ctx context.Context, email string) (management.User, error) {
	email, err := management.NormalizeEmail(email)
	if err != nil {
		return management.User{}, err
	}
	var user management.User
	err = d.pool.QueryRow(ctx, `INSERT INTO management.users (email) VALUES ($1)
		RETURNING id, email, email_verified`, email).Scan(&user.ID, &user.Email, &user.EmailVerified)
	return user, classify(err)
}

// CreateTenant is for the deployment operator, never a tenant administrator.
// It atomically creates the tenant, first membership and its only Fleet.
func (d *Directory) CreateTenant(ctx context.Context, name, adminID string) (management.Tenant, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Tenant{}, err
	}
	defer rollback(tx)
	tenant, err := createTenant(ctx, tx, name, adminID)
	if err != nil {
		return management.Tenant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Tenant{}, err
	}
	return tenant, nil
}

func createTenant(ctx context.Context, tx pgx.Tx, name, adminID string) (management.Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return management.Tenant{}, management.ErrInvalid
	}
	var tenant management.Tenant
	err := tx.QueryRow(ctx, `INSERT INTO management.tenants (name) VALUES ($1) RETURNING id, name`, name).Scan(&tenant.ID, &tenant.Name)
	if err != nil {
		return management.Tenant{}, err
	}
	member := management.Membership{TenantID: tenant.ID, UserID: adminID, Role: management.Admin, Status: management.Active, Version: 1, ExecutionEpoch: 1}
	if _, err = tx.Exec(ctx, `INSERT INTO management.memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, tenant.ID, adminID); err != nil {
		return management.Tenant{}, classify(err)
	}
	fleet, err := ensureFleet(ctx, tx, member)
	if err != nil {
		return management.Tenant{}, err
	}
	if err := record(ctx, tx, adminID, fleet, "membership.created", management.Membership{}, member, true); err != nil {
		return management.Tenant{}, err
	}
	return tenant, nil
}

func (d *Directory) Invite(ctx context.Context, actorID, tenantID, email string, role management.Role, ttl time.Duration) (management.Invitation, string, error) {
	email, err := management.NormalizeEmail(email)
	if err != nil || !role.Valid() || ttl <= 0 {
		return management.Invitation{}, "", management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Invitation{}, "", err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return management.Invitation{}, "", err
	}
	if err := requireAdmin(ctx, tx, tenantID, actorID); err != nil {
		return management.Invitation{}, "", err
	}
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM management.memberships m JOIN management.users u ON u.id=m.user_id
		WHERE m.tenant_id=$1 AND u.email=$2 AND m.status <> 'removed')`, tenantID, email).Scan(&exists)
	if err != nil {
		return management.Invitation{}, "", err
	}
	if exists {
		return management.Invitation{}, "", management.ErrConflict
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return management.Invitation{}, "", err
	}
	token := hex.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(token))
	cipher, err := d.config.Secrets.Seal("invitation:"+tenantID+":"+email, []byte(token))
	if err != nil {
		return management.Invitation{}, "", err
	}
	var invitation management.Invitation
	err = tx.QueryRow(ctx, `INSERT INTO management.invitations (tenant_id, email, role, token_hash, expires_at, token_cipher)
		VALUES ($1, $2, $3, $4, clock_timestamp()+make_interval(secs => $5), $6)
		ON CONFLICT (tenant_id, email) DO UPDATE SET role=EXCLUDED.role, token_hash=EXCLUDED.token_hash,
		expires_at=EXCLUDED.expires_at, token_cipher=EXCLUDED.token_cipher, consumed_at=NULL
		RETURNING id, tenant_id, email, role, expires_at`, tenantID, email, role, hash[:], ttl.Seconds(), cipher).Scan(
		&invitation.ID, &invitation.TenantID, &invitation.Email, &invitation.Role, &invitation.ExpiresAt)
	if err != nil {
		return management.Invitation{}, "", err
	}
	// A new invitee may not have an account yet. Preserve the granting actor
	// and invitation identity without copying an email or token into the audit.
	if _, err := tx.Exec(ctx, `INSERT INTO management.audit (tenant_id, actor_id, owner_id, invitation_id,
		action, membership_version, before_role, before_status, after_role, after_status)
		VALUES ($1,$2,(SELECT id FROM management.users WHERE email=$3),$4,'invitation.issued',0,'','',$5,'invited')`,
		tenantID, actorID, email, invitation.ID, role); err != nil {
		return management.Invitation{}, "", err
	}
	if d.config.MailEnabled {
		if err := d.queueMail(ctx, tx, email, "Join your JueX tenant", d.config.PublicURL+"/join#token="+token, invitation.ID); err != nil {
			return management.Invitation{}, "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Invitation{}, "", err
	}
	return invitation, token, nil
}

// AcceptInvitation accepts only for the authenticated, matching global account.
// An invitation grants membership; it neither verifies email nor changes login.
func (d *Directory) AcceptInvitation(ctx context.Context, actorID, token string) (management.Fleet, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Fleet{}, err
	}
	defer rollback(tx)
	fleet, err := acceptInvitation(ctx, tx, actorID, token)
	if err != nil {
		return management.Fleet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Fleet{}, err
	}
	return fleet, nil
}

func acceptInvitation(ctx context.Context, tx pgx.Tx, actorID, token string) (management.Fleet, error) {
	hash := sha256.Sum256([]byte(token))
	var tenantID string
	err := tx.QueryRow(ctx, `SELECT tenant_id FROM management.invitations WHERE token_hash=$1`, hash[:]).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return management.Fleet{}, management.ErrInvitation
	}
	if err != nil {
		return management.Fleet{}, err
	}
	// All membership writers lock Tenant first, including concurrent acceptance.
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return management.Fleet{}, err
	}
	var invitationID string
	var role management.Role
	err = tx.QueryRow(ctx, `SELECT i.id, i.role FROM management.invitations i
		JOIN management.users u ON u.email=i.email
		WHERE i.token_hash=$1 AND u.id=$2 AND i.consumed_at IS NULL AND i.expires_at>clock_timestamp()
		FOR UPDATE OF i`, hash[:], actorID).Scan(&invitationID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return management.Fleet{}, management.ErrInvitation
	}
	if err != nil {
		return management.Fleet{}, classify(err)
	}
	before, err := membership(ctx, tx, tenantID, actorID)
	if err != nil && !errors.Is(err, management.ErrDenied) {
		return management.Fleet{}, err
	}
	if err == nil && before.Status != management.Removed {
		return management.Fleet{}, management.ErrInvitation
	}
	after := management.Membership{TenantID: tenantID, UserID: actorID, Role: role, Status: management.Active}
	err = tx.QueryRow(ctx, `INSERT INTO management.memberships (tenant_id, user_id, role, status) VALUES ($1,$2,$3,'active')
		ON CONFLICT (tenant_id, user_id) DO UPDATE SET role=EXCLUDED.role, status='active', version=management.memberships.version+1
		RETURNING version,execution_epoch`, tenantID, actorID, role).Scan(&after.Version, &after.ExecutionEpoch)
	if err != nil {
		return management.Fleet{}, err
	}
	fleet, err := ensureFleet(ctx, tx, after)
	if err != nil {
		return management.Fleet{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE management.invitations SET consumed_at=clock_timestamp() WHERE id=$1`, invitationID); err != nil {
		return management.Fleet{}, err
	}
	if err := record(ctx, tx, actorID, fleet, "membership.joined", before, after, true); err != nil {
		return management.Fleet{}, err
	}
	return fleet, nil
}

func (d *Directory) ChangeMember(ctx context.Context, actorID, tenantID, ownerID string, role management.Role, status management.MembershipStatus) (management.Membership, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Membership{}, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return management.Membership{}, err
	}
	if err := requireAdmin(ctx, tx, tenantID, actorID); err != nil {
		return management.Membership{}, err
	}
	before, err := membership(ctx, tx, tenantID, ownerID)
	if err != nil {
		return management.Membership{}, err
	}
	if err := management.ValidateMemberChange(before, role, status); err != nil {
		return management.Membership{}, err
	}
	if before.Role == role && before.Status == status {
		return before, nil
	}
	if before.Role == management.Admin && before.Status == management.Active && (role != management.Admin || status != management.Active) {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM management.memberships WHERE tenant_id=$1 AND role='admin' AND status='active'`, tenantID).Scan(&count); err != nil {
			return management.Membership{}, err
		}
		if count <= 1 {
			return management.Membership{}, management.ErrLastAdmin
		}
	}
	after := before
	after.Role, after.Status, after.Version = role, status, before.Version+1
	if before.Status == management.Active && status != management.Active {
		after.ExecutionEpoch++
	}
	if _, err := tx.Exec(ctx, `UPDATE management.memberships SET role=$3, status=$4, version=version+1,execution_epoch=$5 WHERE tenant_id=$1 AND user_id=$2`, tenantID, ownerID, role, status, after.ExecutionEpoch); err != nil {
		return management.Membership{}, err
	}
	fleet, err := fleetFor(ctx, tx, tenantID, ownerID)
	if err != nil {
		return management.Membership{}, err
	}
	if err := record(ctx, tx, actorID, fleet, "membership.changed", before, after, true); err != nil {
		return management.Membership{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Membership{}, err
	}
	return after, nil
}

// Fleet is a read operation. Its result is not an execution capability. Removed
// and suspended owners remain readable only by an active tenant administrator.
func (d *Directory) Fleet(ctx context.Context, actorID, tenantID, ownerID string) (management.Fleet, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Fleet{}, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return management.Fleet{}, err
	}
	actor, err := membership(ctx, tx, tenantID, actorID)
	if err != nil {
		return management.Fleet{}, err
	}
	if actor.Status != management.Active || (actor.UserID != ownerID && actor.Role != management.Admin) {
		return management.Fleet{}, management.ErrDenied
	}
	owner, err := membership(ctx, tx, tenantID, ownerID)
	if err != nil {
		return management.Fleet{}, err
	}
	fleet, err := fleetFor(ctx, tx, tenantID, ownerID)
	if err != nil {
		return management.Fleet{}, err
	}
	if actorID != ownerID {
		if err := record(ctx, tx, actorID, fleet, "fleet.read", owner, owner, false); err != nil {
			return management.Fleet{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Fleet{}, err
	}
	return fleet, nil
}

func lockTenant(ctx context.Context, tx pgx.Tx, id string) error {
	var locked string
	return classify(tx.QueryRow(ctx, `SELECT id FROM management.tenants WHERE id=$1 FOR UPDATE`, id).Scan(&locked))
}

func (d *Directory) begin(ctx context.Context) (pgx.Tx, error) {
	// A waiter must see the preceding lock holder's membership writes, even if
	// the deployment changed PostgreSQL's default transaction isolation level.
	return d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
}

func membership(ctx context.Context, tx pgx.Tx, tenantID, userID string) (management.Membership, error) {
	m := management.Membership{TenantID: tenantID, UserID: userID}
	err := tx.QueryRow(ctx, `SELECT role, status, version,execution_epoch FROM management.memberships WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&m.Role, &m.Status, &m.Version, &m.ExecutionEpoch)
	return m, classify(err)
}

func requireAdmin(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	m, err := membership(ctx, tx, tenantID, userID)
	if err != nil {
		return err
	}
	if m.Role != management.Admin || m.Status != management.Active {
		return management.ErrDenied
	}
	return nil
}

func fleetFor(ctx context.Context, tx pgx.Tx, tenantID, userID string) (management.Fleet, error) {
	f := management.Fleet{TenantID: tenantID, UserID: userID}
	err := tx.QueryRow(ctx, `SELECT id FROM management.fleets WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&f.ID)
	return f, classify(err)
}

func ensureFleet(ctx context.Context, tx pgx.Tx, m management.Membership) (management.Fleet, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO management.fleets (tenant_id, user_id) VALUES ($1, $2) ON CONFLICT (tenant_id, user_id) DO NOTHING`, m.TenantID, m.UserID); err != nil {
		return management.Fleet{}, err
	}
	f, err := fleetFor(ctx, tx, m.TenantID, m.UserID)
	if err != nil {
		return f, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management.fleet_settings(fleet_id) VALUES($1) ON CONFLICT DO NOTHING`, f.ID)
	return f, err
}

func record(ctx context.Context, tx pgx.Tx, actorID string, f management.Fleet, action string, before, after management.Membership, publish bool) error {
	var eventID string
	err := tx.QueryRow(ctx, `INSERT INTO management.audit (tenant_id, actor_id, owner_id, fleet_id, action, membership_version,
		before_role, before_status, after_role, after_status) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		f.TenantID, actorID, f.UserID, f.ID, action, after.Version, before.Role, before.Status, after.Role, after.Status).Scan(&eventID)
	if err != nil {
		return err
	}
	if publish {
		_, err = tx.Exec(ctx, `INSERT INTO management.outbox (event_id) VALUES ($1)`, eventID)
	}
	return err
}

func classify(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return management.ErrDenied
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return management.ErrConflict
		case "23503":
			return management.ErrDenied
		case "22P02", "23514":
			return management.ErrInvalid
		}
	}
	return err
}
