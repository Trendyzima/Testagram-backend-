package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/federationclient"
	"github.com/jackc/pgx/v5"
)

type remoteAuthorSubscriptionRequest struct {
	Protocol  string `json:"protocol"`
	Identity  string `json:"identity"`
	Handle    string `json:"handle,omitempty"`
}

type remoteAuthorVerificationRequest struct {
	LocalUserID string `json:"local_user_id"`
	Protocol    string `json:"protocol"`
	Identity    string `json:"identity"`
	Handle      string `json:"handle,omitempty"`
	Verified    bool   `json:"verified"`
	ErrorCode   string `json:"error_code,omitempty"`
}

func validRemoteAuthorIdentity(protocol, identity string) bool {
	identity = strings.TrimSpace(identity)
	switch protocol {
	case "activitypub":
		return validFederationRemoteURI(identity)
	case "bluesky":
		return validHomeFeedDID(identity)
	default:
		return false
	}
}

// remoteAuthorSubscriptions stores remote identities as subscriptions only.
// They are never inserted into testagram_local.accounts.
func (s *service) remoteAuthorSubscriptions(w http.ResponseWriter, r *http.Request) {
	userID, requestIDValue, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_STORE_UNAVAILABLE", "Remote subscriptions are unavailable", requestIDValue))
		return
	}
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		rows, err := s.db.Query(ctx, `SELECT id::text,protocol,canonical_identity,display_handle,
			verification_status,subscription_status,created_at,updated_at
			FROM testagram_local.remote_author_subscriptions
			WHERE local_user_id=$1::uuid AND subscription_status='active'
			ORDER BY created_at DESC LIMIT 500`, userID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_SUBSCRIPTIONS_READ_FAILED", "Remote subscriptions could not be loaded", requestIDValue))
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, protocol, identity, handle, verification, status string
			var createdAt, updatedAt time.Time
			if err := rows.Scan(&id, &protocol, &identity, &handle, &verification, &status, &createdAt, &updatedAt); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_SUBSCRIPTIONS_READ_FAILED", "Remote subscriptions could not be loaded", requestIDValue))
				return
			}
			items = append(items, map[string]any{
				"id": id, "protocol": protocol, "identity": identity, "handle": handle,
				"verification_status": verification, "status": status,
				"created_at": createdAt, "updated_at": updatedAt,
			})
		}
		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_SUBSCRIPTIONS_READ_FAILED", "Remote subscriptions could not be loaded", requestIDValue))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"subscriptions": items}, "error": nil, "request_id": requestIDValue})
	case http.MethodPost:
		if !s.socialWriteAllowed(w, r, userID, requestIDValue, "remote-author-subscription", 30) {
			return
		}
		var input remoteAuthorSubscriptionRequest
		if err := decodeMediaJSON(w, r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_SUBSCRIPTION", "A valid protocol and canonical remote identity are required", requestIDValue))
			return
		}
		input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
		input.Identity = strings.TrimSpace(input.Identity)
		input.Handle = strings.TrimSpace(input.Handle)
		if !validRemoteAuthorIdentity(input.Protocol, input.Identity) || len(input.Handle) > 256 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_SUBSCRIPTION", "Use an HTTPS ActivityPub actor URI or a valid Bluesky DID", requestIDValue))
			return
		}
		if input.Protocol == "bluesky" && input.Handle != "" && (len(input.Handle) > 253 || strings.ContainsAny(input.Handle, " /?#@")) {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_HANDLE", "The Bluesky handle is invalid", requestIDValue))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		var id, verification string
		err := s.db.QueryRow(ctx, `INSERT INTO testagram_local.remote_author_subscriptions
			(local_user_id,protocol,canonical_identity,display_handle)
			VALUES($1::uuid,$2,$3,$4)
			ON CONFLICT(local_user_id,protocol,canonical_identity) DO UPDATE SET
				display_handle=EXCLUDED.display_handle,
				subscription_status='active',
				updated_at=now()
			RETURNING id::text,verification_status`, userID, input.Protocol, input.Identity, input.Handle).Scan(&id, &verification)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_SUBSCRIPTION_WRITE_FAILED", "Remote subscription could not be saved", requestIDValue))
			return
		}
		resolvedHandle := input.Handle
		if s.federationClient != nil {
			resolveCtx, resolveCancel := context.WithTimeout(r.Context(), 18*time.Second)
			resolved, resolveErr := s.federationClient.ResolveRemoteAuthor(resolveCtx, federationclient.ResolveRemoteAuthorRequest{
				LocalUserID: userID, Protocol: input.Protocol, Identity: input.Identity,
			})
			resolveCancel()
			if resolveErr == nil {
				verification = resolved.VerificationStatus
				resolvedHandle = resolved.Handle
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "data": map[string]any{"id": id, "protocol": input.Protocol,
				"identity": input.Identity, "handle": resolvedHandle,
				"verification_status": verification, "status": "active"},
			"error": nil, "request_id": requestIDValue,
		})
	default:
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use GET or POST", requestIDValue))
	}
}

func (s *service) removeRemoteAuthorSubscription(w http.ResponseWriter, r *http.Request) {
	userID, requestIDValue, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestIDValue, "remote-author-unsubscription", 30) {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("subscription_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_SUBSCRIPTION_ID", "A valid subscription ID is required", requestIDValue))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_STORE_UNAVAILABLE", "Remote subscriptions are unavailable", requestIDValue))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	tag, err := s.db.Exec(ctx, `UPDATE testagram_local.remote_author_subscriptions
		SET subscription_status='removed',updated_at=now()
		WHERE id=$1::uuid AND local_user_id=$2::uuid AND subscription_status='active'`, id, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_UNSUBSCRIPTION_FAILED", "Remote subscription could not be removed", requestIDValue))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"removed": tag.RowsAffected() > 0}, "error": nil, "request_id": requestIDValue})
}

// Only the private Federation service may promote a pending remote identity
// after resolving its canonical actor/DID and verifying protocol provenance.
func (s *service) verifyRemoteAuthorSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	var input remoteAuthorVerificationRequest
	if err := decodeMediaJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_VERIFICATION", "A valid remote identity verification result is required", requestID(r)))
		return
	}
	input.LocalUserID = strings.ToLower(strings.TrimSpace(input.LocalUserID))
	input.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	input.Identity = strings.TrimSpace(input.Identity)
	input.Handle = strings.TrimSpace(input.Handle)
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if !socialUUIDPattern.MatchString(input.LocalUserID) || !validRemoteAuthorIdentity(input.Protocol, input.Identity) ||
		len(input.Handle) > 256 || len(input.ErrorCode) > 96 || (input.Verified && input.ErrorCode != "") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REMOTE_VERIFICATION", "The verified local owner, protocol and canonical identity are required", requestID(r)))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_STORE_UNAVAILABLE", "Remote subscriptions are unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var status string
	err := s.db.QueryRow(ctx, `UPDATE testagram_local.remote_author_subscriptions
		SET verification_status=$4,
			verified_at=CASE WHEN $4='verified' THEN now() ELSE NULL END,
			display_handle=CASE WHEN $5='' THEN display_handle ELSE $5 END,
			last_error_code=$6,updated_at=now()
		WHERE local_user_id=$1::uuid AND protocol=$2 AND canonical_identity=$3
		  AND subscription_status='active' AND verification_status='pending'
		RETURNING verification_status`,
		input.LocalUserID, input.Protocol, input.Identity,
		map[bool]string{true: "verified", false: "rejected"}[input.Verified],
		input.Handle, input.ErrorCode).Scan(&status)
	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusConflict, errorBody("REMOTE_SUBSCRIPTION_NOT_PENDING", "No active pending subscription matches this verified identity", requestID(r)))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REMOTE_VERIFICATION_WRITE_FAILED", "Remote identity verification could not be saved", requestID(r)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]string{"verification_status": status}, "error": nil, "request_id": requestID(r)})
}
