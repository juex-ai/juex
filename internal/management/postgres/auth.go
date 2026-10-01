package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/management"
)

type Auth struct {
	d         *Directory
	slots     chan struct{}
	dummyHash string
}

func NewAuth(d *Directory) (*Auth, error) {
	password, err := randomToken()
	if err != nil {
		return nil, err
	}
	dummy, err := management.HashPassword(password)
	if err != nil {
		return nil, err
	}
	return &Auth{d: d, slots: make(chan struct{}, 4), dummyHash: dummy}, nil
}

func (a *Auth) hash(ctx context.Context, password string) (string, error) {
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return management.HashPassword(password)
}

func (a *Auth) verify(ctx context.Context, hash, password string) bool {
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return false
	}
	return management.VerifyPassword(hash, password)
}

// BeginBootstrap is reachable only from the local operator CLI. No anonymous
// HTTP endpoint can claim the first administrator or issue this bearer link.
func (a *Auth) BeginBootstrap(ctx context.Context, name, email string) (string, error) {
	email, err := management.NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	tx, err := a.d.begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.management.bootstrap'))`); err != nil {
		return "", err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.tenants)`).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "", management.ErrConflict
	}
	var userID string
	if err := tx.QueryRow(ctx, `INSERT INTO management.users(email) VALUES($1) RETURNING id`, email).Scan(&userID); err != nil {
		return "", classify(err)
	}
	if _, err := createTenant(ctx, tx, name, userID); err != nil {
		return "", err
	}
	token, err := issueToken(ctx, tx, userID, "bootstrap", 30*time.Minute)
	if err != nil {
		return "", err
	}
	if err := identityAudit(ctx, tx, userID, "bootstrap.issued"); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return a.d.config.PublicURL + "/set-password#token=" + token, nil
}

func (a *Auth) RegisterInvitation(ctx context.Context, token, password string) (management.Session, error) {
	encoded, err := a.hash(ctx, password)
	if err != nil {
		return management.Session{}, err
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := a.d.begin(ctx)
	if err != nil {
		return management.Session{}, err
	}
	defer rollback(tx)
	var tenantID, email string
	if err := tx.QueryRow(ctx, `SELECT tenant_id,email FROM management.invitations WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp()`, hash[:]).Scan(&tenantID, &email); errors.Is(err, pgx.ErrNoRows) {
		return management.Session{}, management.ErrInvitation
	} else if err != nil {
		return management.Session{}, err
	}
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return management.Session{}, err
	}
	var user management.User
	err = tx.QueryRow(ctx, `INSERT INTO management.users(email) VALUES($1) RETURNING id,email,email_verified`, email).Scan(&user.ID, &user.Email, &user.EmailVerified)
	if errors.Is(classify(err), management.ErrConflict) {
		return management.Session{}, management.ErrLoginRequired
	}
	if err != nil {
		return management.Session{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.passwords(user_id,encoded) VALUES($1,$2)`, user.ID, encoded); err != nil {
		return management.Session{}, err
	}
	if _, err := acceptInvitation(ctx, tx, user.ID, token); err != nil {
		return management.Session{}, err
	}
	session, err := newSession(ctx, tx, user)
	if err != nil {
		return management.Session{}, err
	}
	if err := identityAudit(ctx, tx, user.ID, "account.registered"); err != nil {
		return management.Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Session{}, err
	}
	return session, nil
}

func (a *Auth) Login(ctx context.Context, email, password string) (management.Session, error) {
	email, normalizeErr := management.NormalizeEmail(email)
	if err := a.allowAttempt(ctx, "login:"+email, 10); err != nil {
		return management.Session{}, err
	}
	var user management.User
	var encoded string
	err := a.d.pool.QueryRow(ctx, `SELECT u.id,u.email,u.email_verified,p.encoded FROM management.users u JOIN management.passwords p ON p.user_id=u.id WHERE u.email=$1`, email).Scan(&user.ID, &user.Email, &user.EmailVerified, &encoded)
	if errors.Is(err, pgx.ErrNoRows) || normalizeErr != nil {
		a.verify(ctx, a.dummyHash, password)
		return management.Session{}, management.ErrCredentials
	}
	if err != nil {
		return management.Session{}, err
	}
	if !a.verify(ctx, encoded, password) {
		return management.Session{}, management.ErrCredentials
	}
	tx, err := a.d.begin(ctx)
	if err != nil {
		return management.Session{}, err
	}
	defer rollback(tx)
	if err := lockUser(ctx, tx, user.ID); err != nil {
		return management.Session{}, err
	}
	var current string
	if err := tx.QueryRow(ctx, `SELECT encoded FROM management.passwords WHERE user_id=$1`, user.ID).Scan(&current); err != nil {
		return management.Session{}, err
	}
	// A reset that won the user lock invalidates a password verified earlier.
	if current != encoded {
		return management.Session{}, management.ErrCredentials
	}
	session, err := newSession(ctx, tx, user)
	if err != nil {
		return management.Session{}, err
	}
	if err := identityAudit(ctx, tx, user.ID, "session.created"); err != nil {
		return management.Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return management.Session{}, err
	}
	return session, nil
}

func (a *Auth) Authenticate(ctx context.Context, token string) (management.User, error) {
	if len(token) != 64 {
		return management.User{}, management.ErrSession
	}
	hash := sha256.Sum256([]byte(token))
	var user management.User
	err := a.d.pool.QueryRow(ctx, `SELECT u.id,u.email,u.email_verified FROM management.sessions s JOIN management.users u ON u.id=s.user_id
	WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp()`, hash[:]).Scan(&user.ID, &user.Email, &user.EmailVerified)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, management.ErrSession
	}
	return user, err
}

func (a *Auth) Logout(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := a.d.pool.Exec(ctx, `WITH removed AS (DELETE FROM management.sessions WHERE token_hash=$1 RETURNING user_id)
	INSERT INTO management.identity_audit(user_id,action) SELECT user_id,'session.revoked' FROM removed`, hash[:])
	return err
}

func (a *Auth) SetPassword(ctx context.Context, token, password string) error {
	encoded, err := a.hash(ctx, password)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := a.d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var userID string
	if err := tx.QueryRow(ctx, `SELECT user_id FROM management.auth_tokens WHERE token_hash=$1 AND purpose IN ('bootstrap','recover')`, hash[:]).Scan(&userID); errors.Is(err, pgx.ErrNoRows) {
		return management.ErrInvitation
	} else if err != nil {
		return err
	}
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	var purpose string
	if err := tx.QueryRow(ctx, `SELECT purpose FROM management.auth_tokens WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp() AND purpose IN ('bootstrap','recover') FOR UPDATE`, hash[:]).Scan(&purpose); errors.Is(err, pgx.ErrNoRows) {
		return management.ErrInvitation
	} else if err != nil {
		return err
	}
	if purpose == "bootstrap" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.passwords WHERE user_id=$1)`, userID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return management.ErrInvitation
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.passwords(user_id,encoded) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET encoded=EXCLUDED.encoded`, userID, encoded); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE management.auth_tokens SET consumed_at=clock_timestamp() WHERE user_id=$1 AND purpose IN ('bootstrap','recover')`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM management.sessions WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if err := identityAudit(ctx, tx, userID, "password.reset"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OperatorRecovery is deliberately not part of the public HTTP auth interface.
func (a *Auth) OperatorRecovery(ctx context.Context, email string) (string, error) {
	email, err := management.NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	tx, err := a.d.begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	var userID string
	if err := tx.QueryRow(ctx, `SELECT id FROM management.users WHERE email=$1 FOR UPDATE`, email).Scan(&userID); err != nil {
		return "", classify(err)
	}
	token, err := issueToken(ctx, tx, userID, "recover", 30*time.Minute)
	if err != nil {
		return "", err
	}
	if err := identityAudit(ctx, tx, userID, "operator.recovery_issued"); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return a.d.config.PublicURL + "/set-password#token=" + token, nil
}

func (a *Auth) RequestRecovery(ctx context.Context, email string) error {
	if !a.d.config.MailEnabled {
		return management.ErrMailUnavailable
	}
	email, err := management.NormalizeEmail(email)
	if err != nil {
		return nil
	}
	if err := a.allowAttempt(ctx, "recovery:"+email, 3); err != nil {
		return err
	}
	tx, err := a.d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var userID string
	err = tx.QueryRow(ctx, `SELECT id FROM management.users WHERE email=$1 AND email_verified=true FOR UPDATE`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	token, err := issueToken(ctx, tx, userID, "recover", 30*time.Minute)
	if err != nil {
		return err
	}
	if err := a.d.queueMail(ctx, tx, email, "Reset your JueX password", a.d.config.PublicURL+"/set-password#token="+token); err != nil {
		return err
	}
	if err := identityAudit(ctx, tx, userID, "email.recovery_requested"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *Auth) RequestVerification(ctx context.Context, userID string) error {
	if !a.d.config.MailEnabled {
		return management.ErrMailUnavailable
	}
	if err := a.allowAttempt(ctx, "verification:"+userID, 3); err != nil {
		return err
	}
	tx, err := a.d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var email string
	var verified bool
	if err := tx.QueryRow(ctx, `SELECT email,email_verified FROM management.users WHERE id=$1 FOR UPDATE`, userID).Scan(&email, &verified); err != nil {
		return classify(err)
	}
	if verified {
		return nil
	}
	token, err := issueToken(ctx, tx, userID, "verify", 30*time.Minute)
	if err != nil {
		return err
	}
	if err := a.d.queueMail(ctx, tx, email, "Verify your JueX email", a.d.config.PublicURL+"/verify-email#token="+token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *Auth) VerifyEmail(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	tx, err := a.d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var userID string
	if err := tx.QueryRow(ctx, `SELECT user_id FROM management.auth_tokens WHERE token_hash=$1 AND purpose='verify'`, hash[:]).Scan(&userID); errors.Is(err, pgx.ErrNoRows) {
		return management.ErrInvitation
	} else if err != nil {
		return err
	}
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE management.auth_tokens SET consumed_at=clock_timestamp() WHERE token_hash=$1 AND purpose='verify' AND consumed_at IS NULL AND expires_at>clock_timestamp()`, hash[:])
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return management.ErrInvitation
	}
	if _, err := tx.Exec(ctx, `UPDATE management.users SET email_verified=true WHERE id=$1`, userID); err != nil {
		return err
	}
	if err := identityAudit(ctx, tx, userID, "email.verified"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *Auth) allowAttempt(ctx context.Context, key string, limit int) error {
	hash := sha256.Sum256([]byte(key))
	var attempts int
	err := a.d.pool.QueryRow(ctx, `INSERT INTO management.auth_throttles(bucket) VALUES($1) ON CONFLICT(bucket) DO UPDATE SET
	attempts=CASE WHEN management.auth_throttles.window_start<clock_timestamp()-interval '15 minutes' THEN 1 ELSE LEAST(management.auth_throttles.attempts+1,100000) END,
	window_start=CASE WHEN management.auth_throttles.window_start<clock_timestamp()-interval '15 minutes' THEN clock_timestamp() ELSE management.auth_throttles.window_start END RETURNING attempts`, hash[:]).Scan(&attempts)
	if err != nil {
		return err
	}
	if attempts > limit {
		return management.ErrRateLimit
	}
	return nil
}

func issueToken(ctx context.Context, tx pgx.Tx, userID, purpose string, ttl time.Duration) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(token))
	_, err = tx.Exec(ctx, `INSERT INTO management.auth_tokens(token_hash,user_id,purpose,expires_at) VALUES($1,$2,$3,clock_timestamp()+make_interval(secs=>$4))
	ON CONFLICT(user_id,purpose) DO UPDATE SET token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at,consumed_at=NULL`, hash[:], userID, purpose, ttl.Seconds())
	return token, err
}

func newSession(ctx context.Context, tx pgx.Tx, user management.User) (management.Session, error) {
	token, err := randomToken()
	if err != nil {
		return management.Session{}, err
	}
	hash := sha256.Sum256([]byte(token))
	session := management.Session{User: user, Token: token}
	err = tx.QueryRow(ctx, `INSERT INTO management.sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '7 days') RETURNING expires_at`, hash[:], user.ID).Scan(&session.ExpiresAt)
	return session, err
}

func randomToken() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret[:]), nil
}
func lockUser(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	return classify(tx.QueryRow(ctx, `SELECT id FROM management.users WHERE id=$1 FOR UPDATE`, userID).Scan(&id))
}
func identityAudit(ctx context.Context, tx pgx.Tx, userID, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO management.identity_audit(user_id,action) VALUES($1,$2)`, userID, action)
	return err
}
