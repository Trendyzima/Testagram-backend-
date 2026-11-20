package main

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) identityApplicationToken(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.PathValue("application_id"))
	token := strings.TrimSpace(r.Header.Get("X-Application-Token"))
	if !identityApplicationIDPattern.MatchString(id) || len(token) < 32 || len(token) > 128 || s.db == nil {
		return "", pgx.ErrNoRows
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	var digest []byte
	err := s.db.QueryRow(ctx, `SELECT status_access_token_sha256
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()`, id).Scan(&digest)
	if err != nil || subtle.ConstantTimeCompare(digest, authTokenHash(token)) != 1 {
		return "", pgx.ErrNoRows
	}
	return id, nil
}

func (s *service) identityProvisioningToken(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}

	applicationID, err := s.identityApplicationToken(r)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	allowed, rateErr := s.authRateAllowed(ctx, "identity-provisioning-token-ip", authClientIP(r), 5)
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Provisioning authorization is temporarily unavailable", requestID(r)))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many provisioning authorization requests. Try again later.", requestID(r)))
		return
	}
	token, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Provisioning authorization could not be issued", requestID(r)))
		return
	}
	jti, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Provisioning authorization could not be issued", requestID(r)))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning authorization is unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid FOR UPDATE`, applicationID).Scan(&status)
	if err != nil || status != "approved" {
		writeJSON(w, http.StatusConflict, errorBody("APPLICATION_NOT_APPROVED", "Application has not completed both approval stages", requestID(r)))
		return
	}

	tag, err := tx.Exec(ctx, `UPDATE testagram_identity.identity_approval_tokens
		SET token_sha256=$2,jti_sha256=$3,issued_at=now(),expires_at=now()+interval '15 minutes'
		WHERE application_id=$1::uuid AND consumed_at IS NULL`,
		applicationID, authTokenHash(token), authTokenHash(jti))
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_UNAVAILABLE", "Provisioning authorization is already consumed or unavailable", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning authorization could not be committed", requestID(r)))
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "approved", "approval_token": token, "jti": jti, "expires_in_seconds": 900,
	})
}
