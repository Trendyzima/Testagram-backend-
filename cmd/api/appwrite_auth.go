package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxAppwriteAccountResponse = 64 << 10

type appwriteAccount struct {
	ID                string `json:"$id"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	EmailVerification bool   `json:"emailVerification"`
	Status            bool   `json:"status"`
}

// fetchAppwriteAccount validates an Appwrite session JWT against Appwrite's
// authenticated Account endpoint. The JWT is sent only to the configured
// Appwrite origin and is never decoded or trusted locally.
func fetchAppwriteAccount(ctx context.Context, client *http.Client, endpoint, projectID, jwt string) (appwriteAccount, int, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	projectID = strings.TrimSpace(projectID)
	jwt = strings.TrimSpace(jwt)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("Appwrite endpoint must be HTTPS (HTTP is allowed only for localhost)")
	}
	if projectID == "" || len(projectID) > 128 || strings.ContainsAny(projectID, "\r\n") || jwt == "" || len(jwt) > 8192 || strings.ContainsAny(jwt, "\r\n") {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("Appwrite project or session configuration is invalid")
	}
	if !strings.HasSuffix(parsed.Path, "/v1") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1"
	}
	parsed.Path += "/account"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("could not construct Appwrite account request")
	}
	req.Header.Set("X-Appwrite-Project", projectID)
	req.Header.Set("X-Appwrite-JWT", jwt)
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	// Never follow redirects with a bearer-equivalent Appwrite JWT.
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("Appwrite authentication service is unavailable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return appwriteAccount{}, http.StatusUnauthorized, errors.New("Appwrite session was rejected")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("Appwrite authentication service is temporarily unavailable")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return appwriteAccount{}, http.StatusBadGateway, fmt.Errorf("Appwrite account endpoint returned HTTP %d", resp.StatusCode)
	}
	var account appwriteAccount
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAppwriteAccountResponse))
	if err := decoder.Decode(&account); err != nil || !validAppwriteUserID(account.ID) {
		return appwriteAccount{}, http.StatusBadGateway, errors.New("Appwrite returned an invalid account response")
	}
	if !account.Status {
		return appwriteAccount{}, http.StatusUnauthorized, errors.New("Appwrite account is disabled")
	}
	return account, http.StatusOK, nil
}

func validAppwriteUserID(value string) bool {
	if value == "" || len(value) > 36 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *service) authenticatedAppwriteUser(r *http.Request, jwt string) (string, int, error) {
	endpoint := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT"))
	projectID := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	if endpoint == "" || projectID == "" {
		return "", http.StatusServiceUnavailable, errors.New("Appwrite authentication is not configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	account, status, err := fetchAppwriteAccount(ctx, s.upstream, endpoint, projectID, jwt)
	if err != nil {
		return "", status, err
	}
	// Email verification is a server-side authorization gate. A valid Appwrite
	// session alone must not unlock Testagram product APIs while the address is
	// still unverified; the client-side gate is only a usability measure.
	if !account.EmailVerification {
		return "", http.StatusForbidden, errors.New("email verification is required before accessing Testagram")
	}
	if s.db == nil {
		return "", http.StatusServiceUnavailable, errors.New("identity database is unavailable")
	}
	var canonicalID string
	email := strings.ToLower(strings.TrimSpace(account.Email))
	if len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return "", http.StatusBadGateway, errors.New("Appwrite returned an invalid email address")
	}
	// Authentication must never create a new Testagram identity as a side
	// effect. A valid Appwrite account is not proof that it has been linked to
	// an existing Testagram profile, posts, actor ID, or storage ownership.
	// Signup provisioning and existing-account linking are separate trusted,
	// transactional flows; unknown identities fail closed here.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram identity store is unavailable")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	err = tx.QueryRow(ctx, `
		UPDATE testagram_identity.appwrite_accounts
		SET email = COALESCE(NULLIF($2, ''), email),
		    email_verified = $3,
		    last_seen_at = now(),
		    updated_at = now()
		WHERE appwrite_user_id = $1
		RETURNING canonical_user_id::text`, account.ID, email, account.EmailVerification).Scan(&canonicalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", http.StatusConflict, errors.New("Appwrite identity is not linked to a Testagram account")
	}
	if err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Appwrite identity could not be mapped to the Testagram account")
	}

	if account.EmailVerification {
		// Complete the email-verification activation transition atomically.
		// A partial sequence must never leave an active account with a stale
		// provisioning state, or a completed saga with a still-deactivated user.
		var provisioningStatus string
		provisioningErr := tx.QueryRow(ctx, `
			SELECT status
			FROM testagram_identity.registration_provisioning
			WHERE appwrite_user_id=$1 AND canonical_user_id=$2::uuid
			FOR UPDATE
		`, account.ID, canonicalID).Scan(&provisioningStatus)
		if provisioningErr == nil && provisioningStatus == "linked" {
			var accountStatus string
			if err := tx.QueryRow(ctx, `
				SELECT account_status FROM testagram_local.accounts
				WHERE user_id=$1::uuid FOR UPDATE
			`, canonicalID).Scan(&accountStatus); err != nil {
					return "", http.StatusServiceUnavailable, errors.New("Testagram account state could not be verified")
			}
			if accountStatus == "suspended" {
				return "", http.StatusForbidden, errors.New("Testagram account is suspended")
			}
			if accountStatus != "active" && accountStatus != "deactivated" {
				return "", http.StatusForbidden, errors.New("Testagram account is not eligible for activation")
			}
			if accountStatus == "deactivated" {
				tag, err := tx.Exec(ctx, `
					UPDATE testagram_local.accounts
					SET account_status='active',updated_at=now()
					WHERE user_id=$1::uuid AND account_status='deactivated'
				`, canonicalID)
				if err != nil || tag.RowsAffected() != 1 {
					return "", http.StatusServiceUnavailable, errors.New("Testagram account activation could not be completed")
				}
			}
			tag, err := tx.Exec(ctx, `
				UPDATE testagram_identity.registration_provisioning
				SET status='completed',completed_at=COALESCE(completed_at,now()),updated_at=now()
				WHERE appwrite_user_id=$1 AND canonical_user_id=$2::uuid AND status='linked'
			`, account.ID, canonicalID)
			if err != nil || tag.RowsAffected() != 1 {
				return "", http.StatusServiceUnavailable, errors.New("Testagram provisioning state could not be completed")
			}
			tag, err = tx.Exec(ctx, `
				UPDATE testagram_identity.identity_applications a
				SET status='provisioned',updated_at=now()
				FROM testagram_identity.registration_provisioning p
				WHERE p.application_id=a.id AND p.appwrite_user_id=$1
				  AND p.canonical_user_id=$2::uuid AND p.status='completed'
				  AND a.status='provisioning'
			`, account.ID, canonicalID)
			if err != nil || tag.RowsAffected() != 1 {
				return "", http.StatusServiceUnavailable, errors.New("Testagram application state could not be completed")
			}
		} else if provisioningErr != nil && !errors.Is(provisioningErr, pgx.ErrNoRows) {
			return "", http.StatusServiceUnavailable, errors.New("Testagram provisioning state could not be read")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram identity update could not be committed")
	}
	return canonicalID, http.StatusOK, nil
}
