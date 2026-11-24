package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const nativeRefreshTTL = 30 * 24 * time.Hour

type nativeSessionTokens struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	TokenType    string         `json:"token_type"`
	ExpiresIn    int            `json:"expires_in"`
	User         map[string]any `json:"user"`
}

// Authentication is Testagram-owned and PostgreSQL-backed.
func authFingerprint(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func issueNativeSession(ctx context.Context, db interface {
	Begin(context.Context) (pgx.Tx, error)
}, userID, email string, version int64, userAgent, ip string) (nativeSessionTokens, error) {
	refresh, err := authRandomToken()
	if err != nil {
		return nativeSessionTokens{}, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nativeSessionTokens{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Serialize session creation with password resets and account disablement.
	// If either operation wins first, the stale login must not create a usable
	// session with an old token version.
	var currentEmail string
	var currentVersion int64
	var emailVerified, accountEnabled, resetNotRequired bool
	if err := tx.QueryRow(ctx, `
		SELECT email_normalized, token_version, email_verified_at IS NOT NULL,
			disabled_at IS NULL, NOT password_reset_required
		FROM testagram_auth.identities
		WHERE user_id=$1::uuid
		FOR UPDATE
	`, userID).Scan(&currentEmail, &currentVersion, &emailVerified, &accountEnabled, &resetNotRequired); err != nil {
		return nativeSessionTokens{}, err
	}

	// Ordinary native sign-in is independent of the separate identity-evidence
	// and account-provisioning workflow. Email verification, account state,
	// password-reset state, and token version remain enforced here.
	if currentEmail != strings.ToLower(strings.TrimSpace(email)) || currentVersion != version ||
		!emailVerified || !accountEnabled || !resetNotRequired {
		return nativeSessionTokens{}, errors.New("identity changed during sign-in")
	}
	var sessionID, familyID string
	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text").Scan(&sessionID, &familyID); err != nil {
		return nativeSessionTokens{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_auth.sessions
			(session_id,user_id,refresh_token_hash,family_id,token_version,expires_at,user_agent_hash,ip_hash)
		VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,now()+interval '30 days',$6,$7)
	`, sessionID, userID, authTokenHash(refresh), familyID, version, authFingerprint(userAgent), authFingerprint(ip)); err != nil {
		return nativeSessionTokens{}, err
	}
	access, err := authSignJWT(userID, email, version, sessionID)
	if err != nil {
		return nativeSessionTokens{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_auth.security_audit(subject_user_id,event_type,outcome,ip_hash)
		VALUES($1::uuid,'auth.login','success',$2)`, userID, authFingerprint(ip)); err != nil {
		return nativeSessionTokens{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nativeSessionTokens{}, err
	}
	return nativeSessionTokens{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: 900,
		User: map[string]any{"id": userID, "email": email},
	}, nil
}

func verifyNativeAccessToken(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, token string) (authClaims, int, error) {
	var claims authClaims
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || len(token) > 8192 {
		return claims, http.StatusUnauthorized, errors.New("invalid bearer token")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, http.StatusUnauthorized, errors.New("invalid token header")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return claims, http.StatusUnauthorized, errors.New("unsupported token header")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, http.StatusUnauthorized, errors.New("invalid token signature")
	}
	secret := strings.TrimSpace(env("TESTAGRAM_AUTH_JWT_SECRET", ""))
	if len(secret) < 32 {
		return claims, http.StatusServiceUnavailable, errors.New("native authentication is not configured")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, http.StatusUnauthorized, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return claims, http.StatusUnauthorized, errors.New("invalid token claims")
	}
	now := time.Now().UTC().Unix()
	if claims.Issuer != "testagram-auth" || claims.Audience != "testagram-api" || claims.Role != "authenticated" ||
		!localUUIDPattern.MatchString(claims.Subject) || !localUUIDPattern.MatchString(claims.SessionID) ||
		claims.TokenVersion < 1 || claims.IssuedAt <= 0 || claims.IssuedAt > now+60 ||
		claims.ExpiresAt <= now || claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > int64((15*time.Minute+time.Minute)/time.Second) {
		return authClaims{}, http.StatusUnauthorized, errors.New("expired or invalid token claims")
	}
	var active bool
	err = db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM testagram_auth.sessions s
			JOIN testagram_auth.identities i ON i.user_id=s.user_id
			WHERE s.session_id=$1::uuid AND s.user_id=$2::uuid
			  AND s.token_version=$3 AND s.revoked_at IS NULL AND s.expires_at>now()
			  AND i.token_version=$3 AND i.disabled_at IS NULL
			  AND i.email_verified_at IS NOT NULL AND NOT i.password_reset_required
		)
	`, claims.SessionID, claims.Subject, claims.TokenVersion).Scan(&active)
	if err != nil {
		return authClaims{}, http.StatusServiceUnavailable, errors.New("native session store is unavailable")
	}
	if !active {
		return authClaims{}, http.StatusUnauthorized, errors.New("native session has been revoked or expired")
	}
	return claims, http.StatusOK, nil
}

func (s *service) rotateNativeRefresh(ctx context.Context, raw, userAgent, ip string) (nativeSessionTokens, error) {
	if len(raw) < 40 || len(raw) > 128 {
		return nativeSessionTokens{}, errors.New("invalid refresh token")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nativeSessionTokens{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var oldID, userID, familyID, email string
	var version int64
	var expires time.Time
	var revoked bool
	var disabled bool
	var verified bool
	var resetRequired bool
	err = tx.QueryRow(ctx, `
		SELECT s.session_id::text,s.user_id::text,s.family_id::text,s.token_version,s.expires_at,
		       s.revoked_at IS NOT NULL,i.email,i.disabled_at IS NOT NULL,i.email_verified_at IS NOT NULL,
		       i.password_reset_required
		FROM testagram_auth.sessions s
		JOIN testagram_auth.identities i ON i.user_id=s.user_id
		WHERE s.refresh_token_hash=$1
		FOR UPDATE OF s,i
	`, authTokenHash(raw)).Scan(&oldID, &userID, &familyID, &version, &expires, &revoked, &email, &disabled, &verified, &resetRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nativeSessionTokens{}, errors.New("invalid refresh token")
	}
	if err != nil {
		return nativeSessionTokens{}, err
	}
	if revoked {
		_, _ = tx.Exec(ctx, "UPDATE testagram_auth.sessions SET revoked_at=COALESCE(revoked_at,now()),revoke_reason='refresh_reuse_detected' WHERE family_id=$1::uuid", familyID)
		_, _ = tx.Exec(ctx, `INSERT INTO testagram_auth.security_audit(subject_user_id,event_type,outcome,ip_hash)
			VALUES($1::uuid,'auth.refresh_reuse','denied',$2)`, userID, authFingerprint(ip))
		_ = tx.Commit(ctx)
		return nativeSessionTokens{}, errors.New("refresh token reuse detected; session family revoked")
	}
	if !expires.After(time.Now()) || disabled || !verified || resetRequired {
		_, _ = tx.Exec(ctx, "UPDATE testagram_auth.sessions SET revoked_at=now(),revoke_reason='refresh_invalidated' WHERE family_id=$1::uuid AND revoked_at IS NULL", familyID)
		_ = tx.Commit(ctx)
		return nativeSessionTokens{}, errors.New("refresh token is expired or account is disabled")
	}
	var currentVersion int64
	if err := tx.QueryRow(ctx, "SELECT token_version FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&currentVersion); err != nil || currentVersion != version {
		_, _ = tx.Exec(ctx, "UPDATE testagram_auth.sessions SET revoked_at=now(),revoke_reason='token_version_changed' WHERE family_id=$1::uuid AND revoked_at IS NULL", familyID)
		_ = tx.Commit(ctx)
		return nativeSessionTokens{}, errors.New("refresh token has been invalidated")
	}
	nextRefresh, err := authRandomToken()
	if err != nil {
		return nativeSessionTokens{}, err
	}
	var nextID string
	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&nextID); err != nil {
		return nativeSessionTokens{}, err
	}
	// Insert the replacement first because replaced_by has an immediate
	// foreign-key reference to the new session row.
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_auth.sessions
		(session_id,user_id,refresh_token_hash,family_id,token_version,expires_at,user_agent_hash,ip_hash)
		VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,now()+$6::interval,$7,$8)`,
		nextID, userID, authTokenHash(nextRefresh), familyID, version, "30 days", authFingerprint(userAgent), authFingerprint(ip)); err != nil {
		return nativeSessionTokens{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE testagram_auth.sessions
		SET revoked_at=now(),revoke_reason='rotated',replaced_by=$2::uuid,last_used_at=now()
		WHERE session_id=$1::uuid AND revoked_at IS NULL`, oldID, nextID); err != nil {
		return nativeSessionTokens{}, err
	}
	access, err := authSignJWT(userID, email, version, nextID)
	if err != nil {
		return nativeSessionTokens{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_auth.security_audit(subject_user_id,event_type,outcome,ip_hash)
		VALUES($1::uuid,'auth.refresh_rotated','success',$2)`, userID, authFingerprint(ip)); err != nil {
		return nativeSessionTokens{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nativeSessionTokens{}, err
	}
	return nativeSessionTokens{AccessToken: access, RefreshToken: nextRefresh, TokenType: "Bearer", ExpiresIn: 900, User: map[string]any{"id": userID, "email": email}}, nil
}

func (s *service) revokeNativeSession(ctx context.Context, claims authClaims, all bool, ip string) error {
	if all {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, "UPDATE testagram_auth.identities SET token_version=token_version+1,updated_at=now() WHERE user_id=$1::uuid", claims.Subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE testagram_auth.sessions SET revoked_at=COALESCE(revoked_at,now()),revoke_reason='logout_all' WHERE user_id=$1::uuid AND revoked_at IS NULL", claims.Subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO testagram_auth.security_audit(actor_user_id,subject_user_id,event_type,outcome,ip_hash)
			VALUES($1::uuid,$1::uuid,'auth.logout_all','success',$2)`, claims.Subject, authFingerprint(ip)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `UPDATE testagram_auth.sessions SET revoked_at=COALESCE(revoked_at,now()),revoke_reason='logout'
		WHERE session_id=$1::uuid AND user_id=$2::uuid AND revoked_at IS NULL`, claims.SessionID, claims.Subject)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("session is already revoked")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_auth.security_audit(actor_user_id,subject_user_id,event_type,outcome,ip_hash)
		VALUES($1::uuid,$1::uuid,'auth.logout','success',$2)`, claims.Subject, authFingerprint(ip)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nativeBearerToken(r *http.Request) (string, error) {
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 8 || len(parts[1]) > 8192 {
		return "", errors.New("bearer token required")
	}
	return parts[1], nil
}
