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

type appwriteSession struct {
	ID           string   `json:"$id"`
	UserID       string   `json:"userId"`
	Current      bool     `json:"current"`
	Factors      []string `json:"factors"`
	MFAUpdatedAt string   `json:"mfaUpdatedAt"`
	Expire       string   `json:"expire"`
}

func fetchAppwriteSession(ctx context.Context, client *http.Client, endpoint, projectID, jwt string) (appwriteSession, int, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	projectID = strings.TrimSpace(projectID)
	jwt = strings.TrimSpace(jwt)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return appwriteSession{}, http.StatusServiceUnavailable, errors.New("Appwrite endpoint must be HTTPS (HTTP is allowed only for localhost)")
	}
	if projectID == "" || len(projectID) > 128 || strings.ContainsAny(projectID, "\r\n") || jwt == "" || len(jwt) > 8192 || strings.ContainsAny(jwt, "\r\n") {
		return appwriteSession{}, http.StatusServiceUnavailable, errors.New("Appwrite project or session configuration is invalid")
	}
	if !strings.HasSuffix(parsed.Path, "/v1") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1"
	}
	parsed.Path += "/account/sessions/current"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return appwriteSession{}, http.StatusServiceUnavailable, errors.New("could not construct Appwrite session request")
	}
	req.Header.Set("X-Appwrite-Project", projectID)
	req.Header.Set("X-Appwrite-JWT", jwt)
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return appwriteSession{}, http.StatusServiceUnavailable, errors.New("Appwrite session service is unavailable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return appwriteSession{}, http.StatusUnauthorized, errors.New("Appwrite session was rejected")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return appwriteSession{}, http.StatusServiceUnavailable, errors.New("Appwrite session service is temporarily unavailable")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return appwriteSession{}, http.StatusBadGateway, fmt.Errorf("Appwrite session endpoint returned HTTP %d", resp.StatusCode)
	}
	var session appwriteSession
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAppwriteAccountResponse))
	if err := decoder.Decode(&session); err != nil || session.ID == "" || !validAppwriteUserID(session.UserID) {
		return appwriteSession{}, http.StatusBadGateway, errors.New("Appwrite returned an invalid session response")
	}
	return session, http.StatusOK, nil
}

func appwriteSessionHasRecentMFA(session appwriteSession, now time.Time) bool {
	if !session.Current || len(session.Factors) < 2 || strings.TrimSpace(session.MFAUpdatedAt) == "" {
		return false
	}
	hasSecondFactor := false
	for _, factor := range session.Factors {
		switch strings.ToLower(strings.TrimSpace(factor)) {
		case "totp", "email", "phone", "recoverycode", "custom", "passkey":
			hasSecondFactor = true
		}
	}
	if !hasSecondFactor {
		return false
	}
	verifiedAt, err := time.Parse(time.RFC3339Nano, session.MFAUpdatedAt)
	if err != nil {
		return false
	}
	age := now.Sub(verifiedAt)
	return age >= -30*time.Second && age <= 5*time.Minute
}

func (s *service) authenticatedAppwritePrivilegedUser(r *http.Request, jwt string) (string, int, error) {
	canonicalID, status, err := s.authenticatedAppwriteUser(r, jwt)
	if err != nil {
		return "", status, err
	}
	endpoint := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT"))
	projectID := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	session, status, err := fetchAppwriteSession(ctx, s.upstream, endpoint, projectID, jwt)
	if err != nil {
		return "", status, err
	}
	if !appwriteSessionHasRecentMFA(session, time.Now().UTC()) {
		return "", http.StatusForbidden, errors.New("a recent Appwrite MFA verification is required for this action")
	}
	return canonicalID, http.StatusOK, nil
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
	err = s.db.QueryRow(ctx, `
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
	return canonicalID, http.StatusOK, nil
}
