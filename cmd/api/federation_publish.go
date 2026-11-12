package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const activityStreamsPublic = "https://www.w3.org/ns/activitystreams#Public"

func normalizeFederationPublicBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("FEDERATION_PUBLIC_BASE_URL must be an HTTPS origin without path, query or fragment")
	}
	return raw, nil
}

func buildFederationNote(baseURL, postID, handle, displayName, content string, createdAt time.Time) (map[string]any, string, error) {
	baseURL, err := normalizeFederationPublicBaseURL(baseURL)
	if err != nil {
		return nil, "", err
	}
	postID = strings.TrimSpace(postID)
	handle = strings.TrimSpace(handle)
	if !socialUUIDPattern.MatchString(strings.ToLower(postID)) || handle == "" || len(handle) > 30 {
		return nil, "", errors.New("invalid canonical post or actor identity")
	}
	actorURI := baseURL + "/users/" + url.PathEscape(handle)
	objectURI := baseURL + "/objects/" + postID
	safeContent := strings.ReplaceAll(html.EscapeString(content), "\n", "<br />")
	note := map[string]any{
		"@context":    "https://www.w3.org/ns/activitystreams",
		"id":          objectURI,
		"type":        "Note",
		"attributedTo": actorURI,
		"content":     safeContent,
		"mediaType":   "text/html",
		"published":   createdAt.UTC().Format(time.RFC3339Nano),
		"to":          []string{activityStreamsPublic},
	}
	if strings.TrimSpace(displayName) != "" {
		note["name"] = strings.TrimSpace(displayName)
	}
	return note, actorURI, nil
}

func buildFederationCreateActivity(baseURL, postID, handle, displayName, content string, createdAt time.Time) (string, []byte, error) {
	note, actorURI, err := buildFederationNote(baseURL, postID, handle, displayName, content, createdAt)
	if err != nil {
		return "", nil, err
	}
	baseURL, err = normalizeFederationPublicBaseURL(baseURL)
	if err != nil {
		return "", nil, err
	}
	activityID := baseURL + "/activities/" + postID
	activity := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       activityID,
		"type":     "Create",
		"actor":    actorURI,
		"published": createdAt.UTC().Format(time.RFC3339Nano),
		"to":       []string{activityStreamsPublic},
		"object":   note,
	}
	payload, err := json.Marshal(activity)
	if err != nil {
		return "", nil, err
	}
	return activityID, payload, nil
}

func (s *service) enqueueFederationCreateTx(ctx context.Context, tx pgx.Tx, userID, postID, content string, createdAt time.Time) error {
	var handle, displayName, accountStatus string
	var enabled, discoverable, protected bool
	if err := tx.QueryRow(ctx, `SELECT username,display_name,federation_enabled,
		discoverable_by_username,protected_account,account_status
		FROM testagram_local.accounts WHERE user_id=$1::uuid`, userID).
		Scan(&handle, &displayName, &enabled, &discoverable, &protected, &accountStatus); err != nil {
		return errors.New("canonical federation account eligibility lookup failed: " + err.Error())
	}
	if !enabled || !discoverable || protected || accountStatus != "active" {
		return nil
	}
	if s.federationOutbox == nil {
		return errors.New("federation publishing is not configured for an opted-in account")
	}
	activityID, payload, err := buildFederationCreateActivity(s.federationPublicBaseURL, postID, handle, displayName, content, createdAt)
	if err != nil {
		return err
	}
	return s.federationOutbox.EnqueueTx(ctx, tx, userID, activityID, payload)
}

func (s *service) federationObject(w http.ResponseWriter, r *http.Request) {
	if !s.federationInternalAuth(w, r) {
		return
	}
	if s.federationOutbox == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_PUBLISHER_UNAVAILABLE", "The transactional Federation publisher is not configured", requestID(r)))
		return
	}
	postID := strings.ToLower(strings.TrimSpace(r.PathValue("post_id")))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FEDERATION_OBJECT_ID", "A valid canonical post ID is required", requestID(r)))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_OBJECT_UNAVAILABLE", "Canonical post storage is unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var handle, displayName, content string
	var createdAt time.Time
	err := s.db.QueryRow(ctx, `SELECT a.username,a.display_name,p.text,p.created_at
		FROM testagram_local.posts p
		JOIN testagram_local.accounts a ON a.user_id=p.author_id
		WHERE p.id=$1::uuid AND p.visibility='public' AND p.deleted_at IS NULL
		  AND a.account_status='active' AND a.discoverable_by_username=TRUE
		  AND a.protected_account=FALSE AND a.federation_enabled=TRUE`, postID).
		Scan(&handle, &displayName, &content, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("FEDERATION_OBJECT_NOT_FOUND", "The public federated object is unavailable", requestID(r)))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_OBJECT_LOOKUP_FAILED", "The canonical public object could not be loaded", requestID(r)))
		return
	}
	note, _, err := buildFederationNote(s.federationPublicBaseURL, postID, handle, displayName, content, createdAt)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_OBJECT_BUILD_FAILED", "The public ActivityPub object could not be prepared", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": note, "error": nil, "request_id": requestID(r),
	})
}

