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
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	account, status, err := fetchAppwriteAccount(ctx, s.upstream, endpoint, projectID, jwt)
	if err != nil {
		return "", status, err
	}
	if !account.EmailVerification {
		return "", http.StatusForbidden, errors.New("email verification is required before accessing Testagram")
	}
	if s.db == nil {
		return "", http.StatusServiceUnavailable, errors.New("identity database is unavailable")
	}
	email, err := authEmail(account.Email)
	if err != nil || !validAppwriteUserID(account.ID) {
		return "", http.StatusBadGateway, errors.New("Appwrite returned an invalid account identity")
	}

	// Appwrite proves credentials, but it is not the authority for Testagram
	// account creation. Only the canonical mapping created by the approved
	// two-person identity-provisioning transaction may unlock product APIs.
	// In particular, never auto-create a canonical account from a first login.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram identity store is unavailable")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var canonicalID, applicationID, applicationStatus string
	err = tx.QueryRow(ctx, `
		SELECT a.canonical_user_id::text,p.application_id::text,ia.status
		FROM testagram_identity.appwrite_accounts a
		JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
		JOIN testagram_identity.registration_provisioning p
		  ON p.appwrite_user_id=a.appwrite_user_id
		 AND p.canonical_user_id=a.canonical_user_id
		 AND p.status='completed'
		JOIN testagram_identity.identity_applications ia ON ia.id=p.application_id
		WHERE a.appwrite_user_id=$1 AND lower(a.email)=lower($2)
		  AND ia.status IN ('verification_pending','verified')
		FOR UPDATE OF a,p,ia
	`, account.ID, email).Scan(&canonicalID, &applicationID, &applicationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", http.StatusForbidden, errors.New("identity review and two-person account approval are required before accessing Testagram")
	}
	if err != nil {
		return "", http.StatusServiceUnavailable, errors.New("approved Testagram account mapping could not be verified")
	}

	if _, err = tx.Exec(ctx, `
		UPDATE testagram_identity.appwrite_accounts
		SET email_verified=true,last_seen_at=now(),updated_at=now()
		WHERE appwrite_user_id=$1 AND canonical_user_id=$2::uuid
	`, account.ID, canonicalID); err != nil {
		return "", http.StatusServiceUnavailable, errors.New("verified Appwrite account state could not be synchronized")
	}
	if applicationStatus == "verification_pending" {
		if _, err = tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET status='verified',updated_at=now()
			WHERE id=$1::uuid AND status='verification_pending'
		`, applicationID); err != nil {
			return "", http.StatusServiceUnavailable, errors.New("identity verification state could not be synchronized")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", http.StatusServiceUnavailable, errors.New("Testagram account authorization could not be committed")
	}
	return canonicalID, http.StatusOK, nil
}

type appwriteRegistrationConsentRequest struct {
	AgeConfirmed    bool   `json:"age_confirmed"`
	TermsAccepted   bool   `json:"terms_accepted"`
	PrivacyAccepted bool   `json:"privacy_accepted"`
	TermsVersion    string `json:"terms_version"`
	PrivacyVersion  string `json:"privacy_version"`
}

func (s *service) appwriteRegistrationConsent(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", id))
		return
	}
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 8 || len(parts[1]) > 8192 {
		writeJSON(w, http.StatusUnauthorized, errorBody("AUTH_REQUIRED", "A valid Appwrite session is required", id))
		return
	}
	var input appwriteRegistrationConsentRequest
	if err := decodeMediaJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REGISTRATION_CONSENT", "Registration consent payload is invalid", id))
		return
	}
	termsVersion := strings.TrimSpace(env("TESTAGRAM_TERMS_VERSION", "2026-11-11"))
	privacyVersion := strings.TrimSpace(env("TESTAGRAM_PRIVACY_VERSION", "2026-11-11"))
	if !input.AgeConfirmed || !input.TermsAccepted || !input.PrivacyAccepted ||
		input.TermsVersion != termsVersion || input.PrivacyVersion != privacyVersion ||
		termsVersion == "" || privacyVersion == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("REGISTRATION_CONSENT_REQUIRED", "Confirm your age and accept the current Terms and Privacy Notice", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	ipAllowed, err := s.authRateAllowed(ctx, "registration-consent-ip", authClientIP(r), 300)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Registration is temporarily unavailable", id))
		return
	}
	if !ipAllowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many registration attempts. Try again later.", id))
		return
	}
	account, status, err := fetchAppwriteAccount(ctx, s.upstream,
		strings.TrimSpace(env("APPWRITE_ENDPOINT", "")),
		strings.TrimSpace(env("APPWRITE_PROJECT_ID", "")), parts[1])
	if err != nil {
		writeJSON(w, status, errorBody("APPWRITE_SESSION_INVALID", "Appwrite could not verify this registration session", id))
		return
	}
	email, err := authEmail(account.Email)
	if err != nil || !validAppwriteUserID(account.ID) {
		writeJSON(w, http.StatusBadGateway, errorBody("APPWRITE_ACCOUNT_INVALID", "Appwrite returned an invalid account identity", id))
		return
	}
	userAllowed, err := s.authRateAllowed(ctx, "registration-consent-user", account.ID, 12)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Registration is temporarily unavailable", id))
		return
	}
	if !userAllowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many registration attempts for this account. Try again later.", id))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_STORE_UNAVAILABLE", "Registration consent could not be saved", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, consent := range []struct{ kind, version string }{
		{"age_confirmation", "age-v1"},
		{"terms", termsVersion},
		{"privacy", privacyVersion},
	} {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_identity.appwrite_registration_consents
			  (appwrite_user_id,email,consent_type,policy_version,accepted_at,request_id)
			VALUES($1,$2,$3,$4,now(),$5)
			ON CONFLICT (appwrite_user_id,consent_type,policy_version,email) DO NOTHING
		`, account.ID, email, consent.kind, consent.version, id)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_STORE_UNAVAILABLE", "Registration consent could not be saved", id))
			return
		}
	}
	var count int
	err = tx.QueryRow(ctx, `
		SELECT count(DISTINCT consent_type)
		FROM testagram_identity.appwrite_registration_consents
		WHERE appwrite_user_id=$1 AND email=$2
		  AND (consent_type='age_confirmation' AND policy_version='age-v1'
		    OR consent_type='terms' AND policy_version=$3
		    OR consent_type='privacy' AND policy_version=$4)
	`, account.ID, email, termsVersion, privacyVersion).Scan(&count)
	if err != nil || count != 3 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_STORE_UNAVAILABLE", "Registration consent could not be verified", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_STORE_UNAVAILABLE", "Registration consent could not be committed", id))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"recorded": true, "email_verified": account.EmailVerification},
		"error": nil,
		"request_id": id,
	})
}
