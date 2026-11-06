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
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var canonicalUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type legacySupabaseUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func bearerToken(value string) (string, bool) {
	if strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 8 || len(parts[1]) > 8192 || strings.ContainsAny(parts[1], "\r\n") {
		return "", false
	}
	return parts[1], true
}

func fetchLegacySupabaseUser(ctx context.Context, client *http.Client, baseURL, apiKey, token string) (legacySupabaseUser, int, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey, token = strings.TrimSpace(apiKey), strings.TrimSpace(token)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return legacySupabaseUser{}, http.StatusServiceUnavailable, errors.New("legacy authentication endpoint must use HTTPS")
	}
	if apiKey == "" || token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n") {
		return legacySupabaseUser{}, http.StatusUnauthorized, errors.New("legacy session proof is missing or invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/auth/v1/user", nil)
	if err != nil {
		return legacySupabaseUser{}, http.StatusServiceUnavailable, errors.New("could not construct legacy identity request")
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return legacySupabaseUser{}, http.StatusServiceUnavailable, errors.New("legacy identity provider is unavailable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return legacySupabaseUser{}, http.StatusUnauthorized, errors.New("legacy session proof was rejected")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return legacySupabaseUser{}, http.StatusServiceUnavailable, errors.New("legacy identity provider is temporarily unavailable")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return legacySupabaseUser{}, http.StatusBadGateway, fmt.Errorf("legacy identity provider returned HTTP %d", resp.StatusCode)
	}
	var user legacySupabaseUser
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&user); err != nil || !canonicalUUIDPattern.MatchString(user.ID) {
		return legacySupabaseUser{}, http.StatusBadGateway, errors.New("legacy identity provider returned an invalid user identity")
	}
	user.ID = strings.ToLower(user.ID)
	return user, http.StatusOK, nil
}

// linkAppwriteIdentity requires simultaneous proof of both identities. It only
// links an already-migrated account whose canonical UUID is unchanged.
func (s *service) linkAppwriteIdentity(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	appwriteJWT, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody("APPWRITE_SESSION_REQUIRED", "A valid Appwrite session is required", id))
		return
	}
	legacyJWT, ok := bearerToken(r.Header.Get("X-Testagram-Legacy-Authorization"))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody("LEGACY_SESSION_REQUIRED", "Sign in to the existing Testagram account to prove ownership", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_DATABASE_UNAVAILABLE", "Identity database is unavailable", id))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	endpoint, project := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	if endpoint == "" || project == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("APPWRITE_NOT_CONFIGURED", "Appwrite identity verification is not configured", id))
		return
	}
	appwriteUser, status, err := fetchAppwriteAccount(ctx, s.upstream, endpoint, project, appwriteJWT)
	if err != nil {
		writeJSON(w, status, errorBody("APPWRITE_SESSION_INVALID", "Appwrite session could not be verified", id))
		return
	}
	if !appwriteUser.EmailVerification {
		writeJSON(w, http.StatusForbidden, errorBody("APPWRITE_EMAIL_UNVERIFIED", "Verify the Appwrite account email before linking", id))
		return
	}
	if appwriteUser.Email == "" || len(appwriteUser.Email) > 254 || strings.ContainsAny(appwriteUser.Email, "\r\n") {
		writeJSON(w, http.StatusBadGateway, errorBody("APPWRITE_EMAIL_INVALID", "Appwrite returned an invalid verified email address", id))
		return
	}

	digest := sha256.Sum256([]byte(appwriteUser.ID))
	var attempts int
	err = s.db.QueryRow(ctx, `INSERT INTO testagram_auth.rate_limits(action,bucket_hash,window_start,attempts)
 VALUES('identity-link',$1,date_trunc('minute',now()),1)
 ON CONFLICT(action,bucket_hash,window_start) DO UPDATE SET attempts=testagram_auth.rate_limits.attempts+1
 RETURNING attempts`, hex.EncodeToString(digest[:])).Scan(&attempts)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_RATE_LIMIT_UNAVAILABLE", "Identity protection is temporarily unavailable", id))
		return
	}
	if attempts > 5 {
		writeJSON(w, http.StatusTooManyRequests, errorBody("IDENTITY_LINK_RATE_LIMITED", "Too many linking attempts; wait a minute and try again", id))
		return
	}
	if attempts == 1 {
		// Keep this narrowly scoped rate-limit bucket bounded without touching
		// the other independent-auth rate-limit actions.
		_, _ = s.db.Exec(ctx, "DELETE FROM testagram_auth.rate_limits WHERE action='identity-link' AND window_start < now() - interval '1 day'")
	}

	legacyURL := strings.TrimSpace(os.Getenv("SUPABASE_URL"))
	legacyKey := strings.TrimSpace(env("SUPABASE_PUBLISHABLE_KEY", env("SUPABASE_ANON_KEY", "")))
	if legacyURL == "" || legacyKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LEGACY_AUTH_NOT_CONFIGURED", "Existing-account verification is temporarily unavailable", id))
		return
	}
	legacyUser, status, err := fetchLegacySupabaseUser(ctx, s.upstream, legacyURL, legacyKey, legacyJWT)
	if err != nil {
		writeJSON(w, status, errorBody("LEGACY_SESSION_INVALID", "Existing Testagram session could not be verified", id))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_DATABASE_UNAVAILABLE", "Identity database is unavailable", id))
		return
	}
	defer tx.Rollback(ctx)
	var canonicalID string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM testagram_local.accounts WHERE user_id=$1 FOR UPDATE`, legacyUser.ID).Scan(&canonicalID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, errorBody("LEGACY_ACCOUNT_NOT_MIGRATED", "Your existing account data has not yet been migrated to the independent Testagram database. No account was linked.", id))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CANONICAL_ACCOUNT_LOOKUP_FAILED", "Canonical account verification is unavailable", id))
		return
	}
	if canonicalID != legacyUser.ID {
		writeJSON(w, http.StatusConflict, errorBody("CANONICAL_ID_MISMATCH", "The existing account does not have a stable canonical identity yet", id))
		return
	}

	rows, err := tx.Query(ctx, `SELECT appwrite_user_id,canonical_user_id::text,COALESCE(legacy_user_id::text,'')
 FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1 OR legacy_user_id=$2 FOR UPDATE`, appwriteUser.ID, legacyUser.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_MAPPING_LOOKUP_FAILED", "Identity mapping is temporarily unavailable", id))
		return
	}
	found := false
	for rows.Next() {
		var mappedAppwrite, mappedCanonical, mappedLegacy string
		if err := rows.Scan(&mappedAppwrite, &mappedCanonical, &mappedLegacy); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_MAPPING_LOOKUP_FAILED", "Identity mapping is temporarily unavailable", id))
			return
		}
		if mappedAppwrite != appwriteUser.ID || mappedCanonical != legacyUser.ID || mappedLegacy != legacyUser.ID {
			rows.Close()
			writeJSON(w, http.StatusConflict, errorBody("IDENTITY_ALREADY_LINKED", "One of these identities is already linked to a different Testagram account", id))
			return
		}
		found = true
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_MAPPING_LOOKUP_FAILED", "Identity mapping is temporarily unavailable", id))
		return
	}
	if !found {
		_, err = tx.Exec(ctx, `INSERT INTO testagram_identity.appwrite_accounts(appwrite_user_id,canonical_user_id,legacy_user_id,email,email_verified,linked_at)
  VALUES($1,$2,$2,$3,TRUE,now())`, appwriteUser.ID, legacyUser.ID, strings.ToLower(strings.TrimSpace(appwriteUser.Email)))
		if err != nil {
			var pgErr interface{ SQLState() string }
			if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
				writeJSON(w, http.StatusConflict, errorBody("IDENTITY_ALREADY_LINKED", "One of these identities is already linked to a different Testagram account", id))
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LINK_FAILED", "The identity link could not be saved", id))
			return
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE testagram_identity.appwrite_accounts SET email=$2,email_verified=TRUE,last_seen_at=now(),updated_at=now() WHERE appwrite_user_id=$1`, appwriteUser.ID, strings.ToLower(strings.TrimSpace(appwriteUser.Email)))
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LINK_FAILED", "The identity link could not be refreshed", id))
			return
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_identity.link_audit(appwrite_user_id,canonical_user_id,legacy_user_id,event_type,request_id)
 VALUES($1,$2,$2,$3,$4)`, appwriteUser.ID, legacyUser.ID, map[bool]string{true: "already_linked", false: "linked"}[found], id); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "The identity link could not be audited; no change was committed", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_LINK_FAILED", "The identity link could not be committed", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"canonical_user_id": legacyUser.ID, "linked": true, "already_linked": found}, "error": nil, "request_id": id})
}
