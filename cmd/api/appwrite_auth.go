package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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


// appwriteDefaultUsername produces a stable, collision-resistant public handle.
// It never derives identity from an email match to an existing Testagram user.
func appwriteDefaultUsername(email, appwriteID string) string {
	local := strings.SplitN(strings.ToLower(strings.TrimSpace(email)), "@", 2)[0]
	var clean strings.Builder
	for _, r := range local {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			clean.WriteRune(r)
		}
	}
	base := strings.Trim(clean.String(), "_")
	if len(base) < 3 {
		base = "user"
	}
	sum := sha256.Sum256([]byte(appwriteID))
	suffix := hex.EncodeToString(sum[:4])
	if len(base) > 21 {
		base = base[:21]
	}
	return base + "_" + suffix
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
	// First verified Appwrite sign-in provisions a canonical Testagram profile
	// atomically. Existing users are never matched by email: only an existing
	// Appwrite ID mapping can resume an established account and its content.
	if _, err := authEmail(email); err != nil {
		return "", http.StatusBadGateway, errors.New("Appwrite returned an invalid email address")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram identity store is unavailable")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "appwrite:"+account.ID); err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram identity lock is unavailable")
	}
	err = tx.QueryRow(ctx, `
		UPDATE testagram_identity.appwrite_accounts
		SET email=$2,email_verified=$3,last_seen_at=now(),updated_at=now()
		WHERE appwrite_user_id=$1
		RETURNING canonical_user_id::text`, account.ID, email, account.EmailVerification).Scan(&canonicalID)
	if errors.Is(err, pgx.ErrNoRows) {
		username := appwriteDefaultUsername(email, account.ID)
		displayName := strings.TrimSpace(account.Name)
		if len(displayName) > 80 {
			displayName = displayName[:80]
		}
		if displayName == "" {
			displayName = username
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO testagram_local.accounts(user_id,username,display_name)
			VALUES(gen_random_uuid(),$1,$2)
			RETURNING user_id::text`, username, displayName).Scan(&canonicalID)
		if err == nil {
			_, err = tx.Exec(ctx, `
				INSERT INTO testagram_identity.appwrite_accounts
				  (appwrite_user_id,canonical_user_id,email,email_verified,linked_at,last_seen_at,updated_at)
				VALUES($1,$2::uuid,$3,$4,now(),now(),now())`, account.ID, canonicalID, email, account.EmailVerification)
		}
	}
	if err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Appwrite identity could not be provisioned as a Testagram account")
	}
	if err = tx.Commit(ctx); err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram account provisioning could not be committed")
	}
	if account.EmailVerification {
		// Promote a newly verified Appwrite identity from the pending-email
		// state only after the authenticated Account endpoint confirms it.
		if _, err := s.db.Exec(ctx, `
			UPDATE testagram_identity.identity_applications a
			SET status='verified',updated_at=now()
			FROM testagram_identity.registration_provisioning p
			WHERE p.application_id=a.id
			  AND p.appwrite_user_id=$1
			  AND p.status='completed'
			  AND a.status='verification_pending'
		`, account.ID); err != nil {
			return "", http.StatusServiceUnavailable, errors.New("verified Appwrite identity state could not be synchronized")
		}
	}
	return canonicalID, http.StatusOK, nil
}
