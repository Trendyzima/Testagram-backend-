package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type federationPreferenceRequest struct {
	Enabled *bool `json:"enabled"`
}

type federationRemoteFollowRequest struct {
	LocalUserID       string `json:"local_user_id"`
	RemoteActorURI    string `json:"remote_actor_uri"`
	ActivityID        string `json:"activity_id"`
	FollowActivityID  string `json:"follow_activity_id,omitempty"`
}

type federationActorProfile struct {
	UserID            string `json:"user_id"`
	Handle             string `json:"handle"`
	DisplayName        string `json:"display_name"`
	Summary            string `json:"summary"`
	FederationEnabled  bool   `json:"federation_enabled"`
}

func federationBackendTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("FEDERATION_BACKEND_INTERNAL_TOKEN"))
}

func federationTokenMatches(expected, authorization string) bool {
	expected = strings.TrimSpace(expected)
	parts := strings.Fields(strings.TrimSpace(authorization))
	if len(expected) < 32 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
}

func (s *service) federationInternalAuth(w http.ResponseWriter, r *http.Request) bool {
	requestID := requestID(r)
	if len(s.federationInternalToken) < 32 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_INTERNAL_AUTH_UNAVAILABLE", "Private Federation authentication is not configured", requestID))
		return false
	}
	if r.Header.Get("X-Testagram-API-Version") != "1" {
		writeJSON(w, http.StatusBadRequest, errorBody("API_VERSION_UNSUPPORTED", "A supported Testagram API version is required", requestID))
		return false
	}
	if !federationTokenMatches(s.federationInternalToken, r.Header.Get("Authorization")) {
		writeJSON(w, http.StatusUnauthorized, errorBody("INTERNAL_AUTH_REQUIRED", "A valid private Federation service credential is required", requestID))
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", requestID)
	return true
}

func (s *service) federationInternalReady(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_DATABASE_UNAVAILABLE", "Canonical account storage is unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_DATABASE_UNAVAILABLE", "Canonical account storage is unavailable", requestID(r)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"status": "ready"}, "error": nil, "request_id": requestID(r)})
}

func (s *service) federationActorByHandle(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	handle := strings.ToLower(strings.TrimSpace(r.PathValue("handle")))
	if len(handle) < 3 || len(handle) > 30 || !localAccountUsernamePattern.MatchString(handle) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FEDERATION_HANDLE", "A valid local account handle is required", requestID(r)))
		return
	}
	s.federationActorLookup(w, r, "lower(username)=$1", handle)
}

func (s *service) federationActorByID(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	userID := strings.ToLower(strings.TrimSpace(r.PathValue("user_id")))
	if !socialUUIDPattern.MatchString(userID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FEDERATION_USER_ID", "A valid local account ID is required", requestID(r)))
		return
	}
	s.federationActorLookup(w, r, "user_id=$1::uuid", userID)
}

func (s *service) federationActorLookup(w http.ResponseWriter, r *http.Request, predicate, value string) {
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_DATABASE_UNAVAILABLE", "Canonical account storage is unavailable", requestID(r)))
		return
	}
	query := `SELECT user_id::text,lower(username),display_name,bio
		FROM testagram_local.accounts
		WHERE ` + predicate + `
		  AND account_status='active'
		  AND discoverable_by_username=TRUE
		  AND protected_account=FALSE
		  AND federation_enabled=TRUE`
	var profile federationActorProfile
	err := s.db.QueryRow(r.Context(), query, value).Scan(
		&profile.UserID, &profile.Handle, &profile.DisplayName, &profile.Summary)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("FEDERATION_ACTOR_NOT_FOUND", "Local account was not found or has not enabled federation", requestID(r)))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_ACTOR_LOOKUP_FAILED", "Local actor eligibility could not be verified", requestID(r)))
		return
	}
	profile.FederationEnabled = true
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": profile, "error": nil, "request_id": requestID(r)})
}

func (s *service) federationPreference(w http.ResponseWriter, r *http.Request) {
	userID, requestIDValue, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		var enabled bool
		if err := s.db.QueryRow(r.Context(), "SELECT federation_enabled FROM testagram_local.accounts WHERE user_id=$1", userID).Scan(&enabled); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_PREFERENCE_READ_FAILED", "Federation preference could not be loaded", requestIDValue))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]bool{"enabled": enabled}, "error": nil, "request_id": requestIDValue})
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET or PUT", requestIDValue))
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestIDValue, "federation-preference", 10) {
		return
	}
	var input federationPreferenceRequest
	if err := decodeMediaJSON(w, r, &input); err != nil || input.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FEDERATION_PREFERENCE", "The enabled boolean is required", requestIDValue))
		return
	}
	if *input.Enabled {
		var discoverable, protected bool
		if err := s.db.QueryRow(r.Context(), "SELECT discoverable_by_username,protected_account FROM testagram_local.accounts WHERE user_id=$1", userID).Scan(&discoverable, &protected); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_PREFERENCE_READ_FAILED", "Account privacy settings could not be verified", requestIDValue))
			return
		}
		if code, message := federationPreferenceConflict(discoverable, protected); code != "" {
			writeJSON(w, http.StatusConflict, errorBody(code, message, requestIDValue))
			return
		}
	}
	var enabled bool
	if err := s.db.QueryRow(r.Context(), `UPDATE testagram_local.accounts SET federation_enabled=$2,updated_at=now()
		WHERE user_id=$1 AND account_status='active' RETURNING federation_enabled`, userID, *input.Enabled).Scan(&enabled); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_PREFERENCE_WRITE_FAILED", "Federation preference could not be saved", requestIDValue))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]bool{"enabled": enabled}, "error": nil, "request_id": requestIDValue})
}

func (s *service) federationRemoteFollow(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	var input federationRemoteFollowRequest
	if err := decodeMediaJSON(w, r, &input); err != nil ||
		!socialUUIDPattern.MatchString(strings.ToLower(strings.TrimSpace(input.LocalUserID))) ||
		!validFederationRemoteURI(input.RemoteActorURI) || !validFederationRemoteURI(input.ActivityID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_FOLLOW", "A valid local user ID, verified remote actor URI and HTTPS activity ID are required", requestID(r)))
		return
	}
	input.LocalUserID = strings.ToLower(strings.TrimSpace(input.LocalUserID))
	input.RemoteActorURI = strings.TrimSpace(input.RemoteActorURI)
	input.ActivityID = strings.TrimSpace(input.ActivityID)
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_DATABASE_UNAVAILABLE", "Canonical social graph storage is unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var eligible bool
	err := s.db.QueryRow(ctx, `SELECT account_status='active' AND discoverable_by_username=TRUE
		AND protected_account=FALSE AND federation_enabled=TRUE
		FROM testagram_local.accounts WHERE user_id=$1::uuid`, input.LocalUserID).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !eligible) {
		writeJSON(w, http.StatusForbidden, errorBody("LOCAL_ACTOR_NOT_ELIGIBLE", "Backend no longer authorizes remote follows for this local account", requestID(r)))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_FOLLOW_LOOKUP_FAILED", "Canonical account eligibility could not be verified", requestID(r)))
		return
	}
	_, err = s.db.Exec(ctx, `INSERT INTO testagram_local.remote_follows
		(local_user_id,remote_actor_uri,follow_activity_id,last_activity_id,status)
		VALUES($1::uuid,$2,$3,$3,'accepted')
		ON CONFLICT(local_user_id,remote_actor_uri) DO UPDATE SET
		follow_activity_id=EXCLUDED.follow_activity_id,
		last_activity_id=EXCLUDED.last_activity_id,
		status='accepted',updated_at=now()`,
		input.LocalUserID, input.RemoteActorURI, input.ActivityID)
	if err != nil {
		if federationUniqueConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("REMOTE_FOLLOW_CONFLICT", "The remote follow activity ID is already bound to another relationship", requestID(r)))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_FOLLOW_WRITE_FAILED", "Canonical remote follow state could not be persisted", requestID(r)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"status": "accepted"}, "error": nil, "request_id": requestID(r)})
}

func (s *service) federationRemoteUnfollow(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	var input federationRemoteFollowRequest
	if err := decodeMediaJSON(w, r, &input); err != nil ||
		!socialUUIDPattern.MatchString(strings.ToLower(strings.TrimSpace(input.LocalUserID))) ||
		!validFederationRemoteURI(input.RemoteActorURI) || !validFederationRemoteURI(input.ActivityID) ||
		!validFederationRemoteURI(input.FollowActivityID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_UNFOLLOW", "A valid local user ID, verified remote actor URI, HTTPS Undo activity ID and original Follow activity ID are required", requestID(r)))
		return
	}
	input.LocalUserID = strings.ToLower(strings.TrimSpace(input.LocalUserID))
	input.RemoteActorURI = strings.TrimSpace(input.RemoteActorURI)
	input.ActivityID = strings.TrimSpace(input.ActivityID)
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_DATABASE_UNAVAILABLE", "Canonical social graph storage is unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	tag, err := s.db.Exec(ctx, `UPDATE testagram_local.remote_follows
		SET status='removed',last_activity_id=$3,updated_at=now()
		WHERE local_user_id=$1::uuid AND remote_actor_uri=$2
		  AND follow_activity_id=$4 AND status='accepted'`,
		input.LocalUserID, input.RemoteActorURI, input.ActivityID, input.FollowActivityID)
	if err != nil {
		if federationUniqueConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("REMOTE_UNFOLLOW_CONFLICT", "The Undo activity ID is already bound to another relationship", requestID(r)))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_UNFOLLOW_FAILED", "Canonical remote follow state could not be updated", requestID(r)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"removed": tag.RowsAffected() > 0}, "error": nil, "request_id": requestID(r)})
}

func federationUniqueConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func validFederationRemoteURI(raw string) bool {
	if len(raw) < 12 || len(raw) > 2048 || strings.ContainsAny(raw, "\\r\\n\\t ") {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func federationPreferenceConflict(discoverable, protected bool) (string, string) {
	if protected {
		return "FEDERATION_REQUIRES_PUBLIC_ACCOUNT", "Protected accounts cannot enable public federation"
	}
	if !discoverable {
		return "FEDERATION_REQUIRES_DISCOVERABLE_ACCOUNT", "Enable username discovery before enabling federation"
	}
	return "", ""
}

var localAccountUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{3,30}$`)
