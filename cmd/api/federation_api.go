package main

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"
)

type federationPreferenceRequest struct {
	Enabled *bool `json:"enabled"`
}

type federationActorProfile struct {
	UserID            string `json:"user_id"`
	Handle             string `json:"handle"`
	DisplayName        string `json:"display_name"`
	Summary            string `json:"summary"`
	FederationEnabled  bool   `json:"federation_enabled"`
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
		var discoverable bool
		if err := s.db.QueryRow(r.Context(), "SELECT discoverable_by_username FROM testagram_local.accounts WHERE user_id=$1", userID).Scan(&discoverable); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_PREFERENCE_READ_FAILED", "Account discovery settings could not be verified", requestIDValue))
			return
		}
		if !discoverable {
			writeJSON(w, http.StatusConflict, errorBody("FEDERATION_REQUIRES_DISCOVERABLE_ACCOUNT", "Enable username discovery before enabling federation", requestIDValue))
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

var localAccountUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{3,30}$`)
