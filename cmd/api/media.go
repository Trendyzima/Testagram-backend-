package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxMediaIntentBody = 16 << 10

type mediaUploadIntentRequest struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Visibility  string `json:"visibility"`
	SizeBytes   int64  `json:"size_bytes"`
}

type privateMediaDownloadRequest struct {
	Key string `json:"key"`
}

type mediaCompleteRequest struct {
	Key         string `json:"key"`
	Visibility  string `json:"visibility"`
	ContentType string `json:"content_type"`
}

type mediaAsset struct {
	Key          string    `json:"key"`
	Visibility   string    `json:"visibility"`
	ContentType  string    `json:"content_type"`
	OriginalName string    `json:"original_name,omitempty"`
	SizeBytes    int64     `json:"size_bytes"`
	ETag         string    `json:"etag,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	PublicURL    string    `json:"public_url,omitempty"`
}

func sanitizeOriginalName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "upload", nil
	}
	name = path.Base(name)
	if name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") {
		return "", errors.New("invalid original filename")
	}
	return name, nil
}

func (s *service) mediaPreflight(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) mediaUploadIntent(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body mediaUploadIntentRequest
	if err := decodeMediaJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "Request must contain valid JSON media metadata", id))
		return
	}
	if s.platform != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		result, err := s.platform.Check(ctx, "media-upload:"+userID, 10, time.Minute)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_RATE_LIMIT_UNAVAILABLE", "Media upload protection is temporarily unavailable", id))
			return
		}
		if !result.Allowed {
			retryAfter := result.RetryAfterSeconds
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconvFormatInt(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, errorBody("MEDIA_RATE_LIMITED", "Too many upload intents; try again later", id))
			return
		}
	}
	body.ContentType = strings.ToLower(strings.TrimSpace(body.ContentType))
	if body.Visibility == "public" && body.SizeBytes > maxProductPostMediaBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("MEDIA_SIZE_NOT_SUPPORTED", "Public product media must not exceed 20 MiB", id))
		return
	}
	body.Name, err = sanitizeOriginalName(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "File name must be 255 characters or fewer and contain no line breaks", id))
		return
	}
	intent, err := s.media.CreateUploadIntent(userID, body.ContentType, body.Visibility, body.SizeBytes, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", err.Error(), id))
		return
	}
	if body.Visibility == "public" {
		intent.PublicURL = publicMediaURL(intent.Key)
		if intent.PublicURL == "" {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_PUBLIC_URL_INVALID", "Public media delivery URL is not configured", id))
			return
		}
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Media metadata database is unavailable", id))
		return
	}
	quotaBytes, parseErr := strconv.ParseInt(env("MEDIA_USER_QUOTA_BYTES", "2147483648"), 10, 64)
	if parseErr != nil || quotaBytes < 1 || quotaBytes > 1<<50 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CONFIG_INVALID", "Media quota configuration is invalid", id))
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Could not start media metadata transaction", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Serialize quota reservations per owner so parallel upload intents cannot
	// over-reserve the same account's storage allowance.
	if _, err := tx.Exec(r.Context(), `select pg_advisory_xact_lock(hashtext($1))`, userID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CHECK_FAILED", "Could not reserve media quota", id))
		return
	}
	var usedBytes int64
	err = tx.QueryRow(r.Context(), `select
		coalesce((select sum(size_bytes) from testagram_media.assets where owner_id=$1 and status='ready'),0) +
		coalesce((select sum(max_bytes) from testagram_media.upload_intents where owner_id=$1 and status='issued' and expires_at > now()),0)`, userID).Scan(&usedBytes)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CHECK_FAILED", "Could not calculate media quota usage", id))
		return
	}
	if usedBytes >= quotaBytes || body.SizeBytes > quotaBytes-usedBytes {
		writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_QUOTA_EXCEEDED", "This upload would exceed the account media storage quota", id))
		return
	}
	_, err = tx.Exec(r.Context(), `insert into testagram_media.upload_intents
		(object_key, owner_id, visibility, content_type, original_name, max_bytes, expires_at, status)
		values ($1,$2,$3,$4,$5,$6,$7,'issued')`,
		intent.Key, userID, body.Visibility, body.ContentType, body.Name, body.SizeBytes, intent.ExpiresAt)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_INTENT_PERSIST_FAILED", "Could not safely register the upload intent", id))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_INTENT_PERSIST_FAILED", "Could not commit the upload reservation", id))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": intent, "error": nil, "request_id": id})
}

func (s *service) mediaPrivateDownloadURL(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body privateMediaDownloadRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key is required", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Media metadata database is unavailable", id))
		return
	}
	var visibility, assetStatus string
	if err := s.db.QueryRow(r.Context(), `select visibility, status from testagram_media.assets where object_key=$1 and owner_id=$2`, body.Key, userID).Scan(&visibility, &assetStatus); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Private media was not found for this account", id))
		return
	}
	if visibility != "private" || assetStatus != "ready" {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Private media was not found for this account", id))
		return
	}
	signedURL, err := s.media.PresignPrivateDownload(userID, body.Key, 5*time.Minute, time.Now())
	if err != nil {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_ACCESS_DENIED", "This private media object is not available to this account", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"url": signedURL, "expires_in_seconds": 300}, "error": nil, "request_id": id})
}

// mediaList returns only ready assets owned by the authenticated user. Private
// items receive short-lived, owner-scoped URLs; raw private object URLs are never
// exposed by the public CDN contract.
func (s *service) mediaList(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Media storage or metadata database is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	rows, err := s.db.Query(r.Context(), `select object_key, visibility, content_type, original_name, size_bytes, created_at
		from testagram_media.assets where owner_id=$1 and status='ready'
		order by created_at desc limit 100`, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not list media assets", id))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var key, visibility, contentType, originalName string
		var sizeBytes int64
		var createdAt time.Time
		if err := rows.Scan(&key, &visibility, &contentType, &originalName, &sizeBytes, &createdAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not read media assets", id))
			return
		}
		mediaURL := ""
		if visibility == "public" {
			mediaURL = publicMediaURL(key)
		} else if visibility == "private" {
			mediaURL, err = s.media.PresignPrivateDownload(userID, key, 5*time.Minute, time.Now())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_URL_FAILED", "Could not create private media URL", id))
				return
			}
		} else {
			continue
		}
		mediaType := "file"
		if strings.HasPrefix(contentType, "image/") {
			mediaType = "image"
		} else if strings.HasPrefix(contentType, "video/") {
			mediaType = "video"
		}
		items = append(items, map[string]any{
			"id": key, "name": originalName, "path": key, "url": mediaURL,
			"type": mediaType, "size": sizeBytes, "updatedAt": createdAt.UTC().Format(time.RFC3339),
			"visibility": visibility, "content_type": contentType,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not list media assets", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items}, "error": nil, "request_id": id})
}

// mediaComplete verifies the actual object through a signed HEAD request before
// persisting metadata. Client-supplied size and ETag are deliberately not trusted.
func (s *service) mediaComplete(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage or metadata database is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body mediaCompleteRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key is required", id))
		return
	}
	var intentOwner, intentVisibility, intentType, intentName, intentStatus string
	var intentMaxBytes int64
	var intentExpires time.Time
	err = s.db.QueryRow(r.Context(), `select owner_id, visibility, content_type, original_name, max_bytes, expires_at, status
		from testagram_media.upload_intents where object_key=$1`, body.Key).
		Scan(&intentOwner, &intentVisibility, &intentType, &intentName, &intentMaxBytes, &intentExpires, &intentStatus)
	if err != nil || intentOwner != userID ||
		(time.Now().After(intentExpires) && intentStatus != "completed") || (intentStatus != "issued" && intentStatus != "completed") {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_INTENT_INVALID", "Upload intent is missing, expired, or does not belong to this account", id))
		return
	}
	if strings.TrimSpace(body.Visibility) == "" {
		body.Visibility = intentVisibility
	}
	if strings.TrimSpace(body.ContentType) == "" {
		body.ContentType = intentType
	}
	body.ContentType = strings.ToLower(strings.TrimSpace(body.ContentType))
	if intentVisibility != body.Visibility || intentType != body.ContentType {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_INTENT_INVALID", "Completion metadata does not match the stored upload intent", id))
		return
	}
	maxBytes, ok := s.media.MaxBytesForContentType(body.ContentType)
	if !ok || (body.Visibility != "public" && body.Visibility != "private") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "Unsupported media type or visibility", id))
		return
	}
	if intentMaxBytes < 1 || intentMaxBytes > maxBytes {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_INTENT_INVALID", "Stored upload limit is invalid", id))
		return
	}
	headURL, err := s.media.PresignObjectHead(userID, body.Key, body.Visibility, time.Minute, time.Now())
	if err != nil {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_ACCESS_DENIED", "This media key is not owned by the authenticated account", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_VERIFY_FAILED", "Could not verify uploaded media", id))
		return
	}
	client := s.upstream
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_VERIFY_FAILED", "Media storage could not be reached", id))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_UPLOAD_NOT_FOUND", "Upload is not present in object storage yet", id))
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_VERIFY_FAILED", "Object storage rejected media verification", id))
		return
	}
	actualType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	size := resp.ContentLength
	if actualType != body.ContentType || size < 1 || size > intentMaxBytes || size > maxBytes {
		// Make invalid objects eligible for the janitor before attempting immediate
		// deletion. A storage timeout must not leave an untracked object indefinitely.
		_, _ = s.db.Exec(ctx, `update testagram_media.upload_intents
			set status='cleanup_pending', expires_at=now(), updated_at=now()
			where object_key=$1 and owner_id=$2 and status in ('issued','cleanup_pending')`, body.Key, userID)
		if deleteErr := s.deleteMediaObject(ctx, userID, body.Key, body.Visibility); deleteErr == nil {
			_, _ = s.db.Exec(ctx, `update testagram_media.upload_intents
				set status='expired', updated_at=now() where object_key=$1 and owner_id=$2 and status='cleanup_pending'`, body.Key, userID)
		}
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_OBJECT_INVALID", "Uploaded object type or size does not satisfy media policy; cleanup was scheduled", id))
		return
	}
	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	var asset mediaAsset
	err = s.db.QueryRow(ctx, `insert into testagram_media.assets
		(object_key, owner_id, visibility, content_type, original_name, size_bytes, etag, status)
		values ($1,$2,$3,$4,$5,$6,$7,'ready')
		on conflict (object_key) do nothing
		returning object_key, visibility, content_type, original_name, size_bytes, etag, status, created_at`,
		body.Key, userID, body.Visibility, actualType, intentName, size, etag).
		Scan(&asset.Key, &asset.Visibility, &asset.ContentType, &asset.OriginalName, &asset.SizeBytes, &asset.ETag, &asset.Status, &asset.CreatedAt)
	if err != nil {
		var ownerID string
		err = s.db.QueryRow(ctx, `select object_key, owner_id, visibility, content_type, original_name, size_bytes, etag, status, created_at
			from testagram_media.assets where object_key=$1`, body.Key).
			Scan(&asset.Key, &ownerID, &asset.Visibility, &asset.ContentType, &asset.OriginalName, &asset.SizeBytes, &asset.ETag, &asset.Status, &asset.CreatedAt)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not persist media metadata", id))
			return
		}
		if ownerID != userID || asset.Status != "ready" || asset.Visibility != body.Visibility || asset.ContentType != actualType || asset.SizeBytes != size {
			writeJSON(w, http.StatusConflict, errorBody("MEDIA_KEY_CONFLICT", "Media key already exists with different ownership or metadata", id))
			return
		}
	}
	asset.Key = body.Key
	if body.Visibility == "public" {
		asset.PublicURL = publicMediaURL(body.Key)
		if asset.PublicURL == "" {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_PUBLIC_URL_INVALID", "Public media delivery URL is not configured", id))
			return
		}
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.upload_intents set status='completed', updated_at=now()
		where object_key=$1 and owner_id=$2 and status in ('issued','completed')`, body.Key, userID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Media was verified but upload completion state could not be saved", id))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": asset, "error": nil, "request_id": id})
}

func decodeMediaJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxMediaIntentBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *service) authenticatedMediaUser(r *http.Request) (string, int, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") || len(strings.TrimSpace(authorization[7:])) < 8 {
		return "", http.StatusUnauthorized, errors.New("missing bearer token")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(env("SUPABASE_URL", "")), "/")
	apiKey := strings.TrimSpace(env("SUPABASE_PUBLISHABLE_KEY", ""))
	if baseURL == "" || apiKey == "" {
		return "", http.StatusServiceUnavailable, errors.New("authentication backend is not configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/auth/v1/user", nil)
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	client := s.upstream
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", http.StatusUnauthorized, errors.New("session rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", http.StatusBadGateway, errors.New("authentication service failed")
	}
	var user struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&user); err != nil || user.ID == "" {
		return "", http.StatusBadGateway, errors.New("authentication service returned invalid user")
	}
	return user.ID, http.StatusOK, nil
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

type mediaQuarantineRequest struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// mediaQuarantine is an operator-only takedown endpoint. It intentionally
// removes the object from storage rather than pretending a metadata flag alone
// makes a public URL inaccessible.
func (s *service) mediaQuarantine(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	expected := strings.TrimSpace(env("MEDIA_MODERATION_TOKEN", ""))
	provided := strings.TrimSpace(r.Header.Get("X-Testagram-Moderation-Token"))
	if expected == "" || provided == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		writeJSON(w, http.StatusUnauthorized, errorBody("MODERATION_UNAUTHORIZED", "A valid operator moderation token is required", id))
		return
	}
	if s.media == nil || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage or metadata database is unavailable", id))
		return
	}
	var body mediaQuarantineRequest
	if err := decodeMediaJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MODERATION_REQUEST", "A valid media key and reason are required", id))
		return
	}
	body.Key = strings.TrimSpace(body.Key)
	body.Reason = strings.TrimSpace(body.Reason)
	moderatorID := strings.TrimSpace(r.Header.Get("X-Testagram-Moderator-ID"))
	if body.Key == "" || body.Reason == "" || len(body.Reason) > 500 || moderatorID == "" || len(moderatorID) > 128 || strings.ContainsAny(moderatorID, "\r\n") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MODERATION_REQUEST", "A media key, reason (up to 500 characters), and moderator ID are required", id))
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not start moderation transaction", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var ownerID, visibility, currentStatus, externalID string
	err = tx.QueryRow(r.Context(), `select owner_id, visibility, status, coalesce(external_media_id::text,'') from testagram_media.assets where object_key=$1 for update`, body.Key).Scan(&ownerID, &visibility, &currentStatus, &externalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Media asset was not found", id))
		return
	}
	if currentStatus == "quarantined" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantined"}, "error": nil, "request_id": id})
		return
	}
	if currentStatus != "ready" && currentStatus != "quarantine_pending" {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_STATE_INVALID", "Media asset cannot be quarantined in its current state", id))
		return
	}
	if _, err := tx.Exec(r.Context(), `update testagram_media.assets set status='quarantine_pending',
		moderation_reason=$2, moderated_at=now(), moderated_by=$3, updated_at=now()
		where object_key=$1 and status in ('ready','quarantine_pending')`, body.Key, body.Reason, moderatorID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not record media quarantine", id))
		return
	}
	if _, err := tx.Exec(r.Context(), `insert into testagram_media.moderation_events
		(object_key, owner_id, moderator_id, action, reason) values ($1,$2,$3,'quarantine',$4)`,
		body.Key, ownerID, moderatorID, body.Reason); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_AUDIT_FAILED", "Could not record moderation audit event", id))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not commit media quarantine", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.deleteMediaObject(ctx, ownerID, body.Key, visibility); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantine_pending"}, "error": nil, "request_id": id})
		return
	}
	if err := s.deleteProductMedia(ctx, externalID); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantine_pending"}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.assets set status='quarantined', updated_at=now()
		where object_key=$1 and status='quarantine_pending'`, body.Key); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantine_pending"}, "error": nil, "request_id": id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantined"}, "error": nil, "request_id": id})
}

type mediaAttachRequest struct {
	Key    string `json:"key"`
	PostID string `json:"post_id"`
}

type productMediaRecord struct {
	ID      string  `json:"id"`
	OwnerID string  `json:"owner_id"`
	PostID  *string `json:"post_id"`
	Status  string  `json:"status"`
}

func (s *service) mediaAttach(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Media metadata database is unavailable", id))
		return
	}
	serviceKey := strings.TrimSpace(env("SUPABASE_SERVICE_ROLE_KEY", ""))
	publishableKey := strings.TrimSpace(env("SUPABASE_PUBLISHABLE_KEY", ""))
	if serviceKey == "" || publishableKey == "" || strings.TrimSpace(env("SUPABASE_URL", "")) == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_PRODUCT_BRIDGE_UNAVAILABLE", "Product media linking is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body mediaAttachRequest
	if err := decodeMediaJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A media key and post ID are required", id))
		return
	}
	body.Key = strings.TrimSpace(body.Key)
	body.PostID = strings.TrimSpace(body.PostID)
	if body.Key == "" || body.PostID == "" || len(body.PostID) > 128 || strings.ContainsAny(body.PostID, "\r\n") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A media key and valid post ID are required", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	owned, err := s.userOwnsProductPost(ctx, r.Header.Get("Authorization"), publishableKey, userID, body.PostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("POST_OWNERSHIP_CHECK_FAILED", "Could not verify post ownership", id))
		return
	}
	if !owned {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "Post was not found for this account", id))
		return
	}

	// Hold a short row lock while syncing the remote join. Delete/quarantine
	// transitions cannot race through the middle and resurrect a deleted join.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not begin media attachment", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var visibility, contentType, originalName, assetStatus, externalID string
	var sizeBytes int64
	err = tx.QueryRow(ctx, `select visibility, content_type, original_name, size_bytes, status, coalesce(external_media_id::text,'')
		from testagram_media.assets where object_key=$1 and owner_id=$2 for update`, body.Key, userID).
		Scan(&visibility, &contentType, &originalName, &sizeBytes, &assetStatus, &externalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Media asset was not found for this account", id))
		return
	}
	if visibility != "public" || assetStatus != "ready" {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_NOT_ATTACHABLE", "Only ready public media can be attached to a post", id))
		return
	}
	mediaType, supported := productPostMediaType(contentType)
	if !supported {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_TYPE_NOT_ATTACHABLE", "Posts currently support image and video media only", id))
		return
	}
	if sizeBytes < 1 || sizeBytes > maxProductPostMediaBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("MEDIA_SIZE_NOT_ATTACHABLE", "Post media must be between 1 byte and 20 MiB", id))
		return
	}
	publicURL := publicMediaURL(body.Key)
	if publicURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_PUBLIC_URL_INVALID", "Public media URL is not configured", id))
		return
	}
	externalID, err = s.ensureProductMediaRecord(ctx, serviceKey, userID, body.Key, originalName, contentType, sizeBytes, publicURL, externalID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_PRODUCT_SYNC_FAILED", "Could not synchronize media metadata with the product data plane", id))
		return
	}
	tag, err := tx.Exec(ctx, `update testagram_media.assets set external_media_id=$3, updated_at=now()
		where object_key=$1 and owner_id=$2 and status='ready'
		and (external_media_id is null or external_media_id=$3::uuid)`, body.Key, userID, externalID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not persist product media reference", id))
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_STATE_CHANGED", "Media state changed while it was being attached", id))
		return
	}
	if err := s.attachProductMediaToPost(ctx, serviceKey, userID, body.PostID, externalID, contentType, sizeBytes, publicURL); err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_POST_LINK_FAILED", "Media was registered but could not be linked to the post; retry is safe", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not commit media attachment", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
		"key": body.Key, "post_id": body.PostID, "public_url": publicURL,
		"size_bytes": sizeBytes, "content_type": contentType, "media_type": mediaType,
		"status": "uploaded", "external_media_id": externalID,
	}, "error": nil, "request_id": id})
}

const maxProductPostMediaBytes int64 = 20 << 20

// productPostMediaType maps upload MIME types to values accepted by the
// product database's post_media.media_type constraint. Files may be stored as
// private media, but unsupported document types must not reach the post bridge.
func productPostMediaType(contentType string) (string, bool) {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image", true
	case strings.HasPrefix(contentType, "video/"):
		return "video", true
	default:
		return "", false
	}
}

func publicMediaURL(key string) string {
	base, err := url.Parse(strings.TrimRight(env("MEDIA_PUBLIC_BASE_URL", "https://media.testagram.site/v1"), "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.RawQuery != "" || base.Fragment != "" {
		return ""
	}
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.Contains(key, "..") || strings.ContainsAny(key, "\\r\\n\\x00") {
		return ""
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + key
	base.RawPath = ""
	return base.String()
}

func (s *service) userOwnsProductPost(ctx context.Context, authorization, publishableKey, userID, postID string) (bool, error) {
	query := url.Values{}
	query.Set("id", "eq."+postID)
	query.Set("select", "id,author_id,user_id")
	query.Set("limit", "1")
	data, status, err := s.supabaseREST(ctx, http.MethodGet, "posts", query, authorization, publishableKey, nil, "")
	if err != nil {
		return false, err
	}
	if status < 200 || status >= 300 {
		return false, errors.New("post ownership query failed")
	}
	var posts []struct {
		ID       string `json:"id"`
		AuthorID string `json:"author_id"`
		UserID   string `json:"user_id"`
	}
	if err := json.Unmarshal(data, &posts); err != nil {
		return false, err
	}
	for _, post := range posts {
		if post.ID == postID && (post.AuthorID == userID || post.UserID == userID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *service) ensureProductMediaRecord(ctx context.Context, serviceKey, ownerID, key, originalName, contentType string, sizeBytes int64, publicURL, knownID string) (string, error) {
	query := url.Values{}
	query.Set("storage_key", "eq."+key)
	query.Set("select", "id,owner_id,post_id,status")
	query.Set("limit", "1")
	data, status, err := s.supabaseREST(ctx, http.MethodGet, "media_assets", query, serviceKey, serviceKey, nil, "")
	if err != nil || status < 200 || status >= 300 {
		return "", errors.New("product media lookup failed")
	}
	var records []productMediaRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return "", err
	}
	if len(records) > 0 {
		record := records[0]
		if record.OwnerID != ownerID || record.Status != "uploaded" {
			return "", errors.New("existing product media record has different owner or state")
		}
		if knownID != "" && record.ID != knownID {
			return "", errors.New("product media ID conflicts with local metadata")
		}
		return record.ID, nil
	}
	if knownID != "" {
		return "", errors.New("local product media reference is missing remotely")
	}
	mediaType, supported := productPostMediaType(contentType)
	if !supported {
		return "", errors.New("unsupported product post media type")
	}
	if sizeBytes < 1 || sizeBytes > maxProductPostMediaBytes {
		return "", errors.New("product post media size is outside the 1-byte to 20-MiB limit")
	}
	payload := map[string]any{
		"owner_id": ownerID, "post_id": nil, "thread_id": nil,
		"storage_key": key, "bucket": env("PUBLIC_MEDIA_BUCKET", "testagram-public-media"),
		"original_name": originalName, "mime_type": contentType, "media_type": mediaType,
		"byte_size": sizeBytes, "status": "uploaded", "media_url": publicURL,
	}
	query = url.Values{}
	query.Set("select", "id,owner_id,post_id,status")
	data, status, err = s.supabaseREST(ctx, http.MethodPost, "media_assets", query, serviceKey, serviceKey, payload, "return=representation")
	if err != nil || status < 200 || status >= 300 {
		return "", errors.New("product media create failed")
	}
	records = nil
	if err := json.Unmarshal(data, &records); err != nil || len(records) != 1 || records[0].ID == "" || records[0].OwnerID != ownerID {
		return "", errors.New("product media create returned an invalid record")
	}
	return records[0].ID, nil
}

func (s *service) attachProductMediaToPost(ctx context.Context, serviceKey, ownerID, postID, externalID, contentType string, sizeBytes int64, publicURL string) error {
	query := url.Values{}
	query.Set("id", "eq."+externalID)
	query.Set("owner_id", "eq."+ownerID)
	query.Set("status", "eq.uploaded")
	data, status, err := s.supabaseREST(ctx, http.MethodGet, "media_assets", query, serviceKey, serviceKey, nil, "")
	if err != nil || status < 200 || status >= 300 {
		return errors.New("product media lookup failed")
	}
	var records []productMediaRecord
	if err := json.Unmarshal(data, &records); err != nil || len(records) != 1 {
		return errors.New("product media record is missing")
	}
	if records[0].PostID != nil && *records[0].PostID != "" && *records[0].PostID != postID {
		return errors.New("media is already attached to a different post")
	}
	if records[0].PostID == nil || *records[0].PostID == "" {
		updateQuery := url.Values{}
		updateQuery.Set("id", "eq."+externalID)
		updateQuery.Set("owner_id", "eq."+ownerID)
		if _, status, err := s.supabaseREST(ctx, http.MethodPatch, "media_assets", updateQuery, serviceKey, serviceKey,
			map[string]any{"post_id": postID, "updated_at": time.Now().UTC()}, "return=minimal"); err != nil || status < 200 || status >= 300 {
			return errors.New("product media post assignment failed")
		}
	}
	joinQuery := url.Values{}
	joinQuery.Set("post_id", "eq."+postID)
	joinQuery.Set("media_asset_id", "eq."+externalID)
	joinQuery.Set("select", "id")
	joinQuery.Set("limit", "1")
	data, status, err = s.supabaseREST(ctx, http.MethodGet, "post_media", joinQuery, serviceKey, serviceKey, nil, "")
	if err != nil || status < 200 || status >= 300 {
		return errors.New("post media join lookup failed")
	}
	var joins []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &joins); err != nil {
		return err
	}
	if len(joins) > 0 {
		return nil
	}
	sortQuery := url.Values{}
	sortQuery.Set("post_id", "eq."+postID)
	sortQuery.Set("select", "sort_order")
	sortQuery.Set("order", "sort_order.desc")
	sortQuery.Set("limit", "1")
	data, status, err = s.supabaseREST(ctx, http.MethodGet, "post_media", sortQuery, serviceKey, serviceKey, nil, "")
	if err != nil || status < 200 || status >= 300 {
		return errors.New("post media sort lookup failed")
	}
	var last []struct {
		SortOrder int `json:"sort_order"`
	}
	if err := json.Unmarshal(data, &last); err != nil {
		return err
	}
	sortOrder := 0
	if len(last) > 0 {
		sortOrder = last[0].SortOrder + 1
	}
	mediaType, supported := productPostMediaType(contentType)
	if !supported || sizeBytes < 1 || sizeBytes > maxProductPostMediaBytes {
		return errors.New("unsupported product post media type or size")
	}
	join := map[string]any{
		"post_id": postID, "owner_id": ownerID, "media_url": publicURL,
		"media_type": mediaType, "mime_type": contentType, "byte_size": sizeBytes,
		"media_asset_id": externalID, "sort_order": sortOrder,
	}
	_, status, err = s.supabaseREST(ctx, http.MethodPost, "post_media", url.Values{}, serviceKey, serviceKey, join, "return=minimal")
	if err != nil || status < 200 || status >= 300 {
		return errors.New("post media join insert failed")
	}
	return nil
}

func (s *service) deleteProductMedia(ctx context.Context, externalID string) error {
	if strings.TrimSpace(externalID) == "" {
		return nil
	}
	serviceKey := strings.TrimSpace(env("SUPABASE_SERVICE_ROLE_KEY", ""))
	if serviceKey == "" {
		return errors.New("product media bridge is not configured")
	}
	joinQuery := url.Values{}
	joinQuery.Set("media_asset_id", "eq."+externalID)
	_, status, err := s.supabaseREST(ctx, http.MethodDelete, "post_media", joinQuery, serviceKey, serviceKey, nil, "return=minimal")
	if err != nil || status < 200 || status >= 300 {
		return errors.New("post media join deletion failed")
	}
	assetQuery := url.Values{}
	assetQuery.Set("id", "eq."+externalID)
	_, status, err = s.supabaseREST(ctx, http.MethodPatch, "media_assets", assetQuery, serviceKey, serviceKey,
		map[string]any{"status": "deleted", "updated_at": time.Now().UTC()}, "return=minimal")
	if err != nil || status < 200 || status >= 300 {
		return errors.New("product media deletion sync failed")
	}
	return nil
}

func (s *service) supabaseREST(ctx context.Context, method, resource string, query url.Values, authorization, apiKey string, payload any, prefer string) ([]byte, int, error) {
	base := strings.TrimRight(strings.TrimSpace(env("SUPABASE_URL", "")), "/")
	if base == "" || apiKey == "" || authorization == "" {
		return nil, 0, errors.New("Supabase REST is not configured")
	}
	target := base + "/rest/v1/" + resource
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, err
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(authorization)), "bearer ") {
		authorization = "Bearer " + authorization
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	client := s.upstream
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

type mediaDeleteRequest struct {
	Key string `json:"key"`
}

// mediaDelete starts a durable deletion before touching object storage. If the
// storage request fails, the janitor retries deletion_pending assets.
func (s *service) mediaDelete(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage or metadata database is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body mediaDeleteRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key is required", id))
		return
	}
	var visibility, currentStatus, externalID string
	err = s.db.QueryRow(r.Context(), `select visibility, status, coalesce(external_media_id::text,'') from testagram_media.assets where object_key=$1 and owner_id=$2`, body.Key, userID).Scan(&visibility, &currentStatus, &externalID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Media asset was not found for this account", id))
		return
	}
	if currentStatus == "deleted" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deleted"}, "error": nil, "request_id": id})
		return
	}
	if currentStatus == "quarantined" {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_QUARANTINED", "Quarantined media must be handled by a moderator", id))
		return
	}
	if currentStatus != "ready" && currentStatus != "deletion_pending" {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_STATE_INVALID", "Media asset cannot be deleted in its current state", id))
		return
	}
	if currentStatus == "ready" {
		tag, err := s.db.Exec(r.Context(), `update testagram_media.assets set status='deletion_pending', updated_at=now()
			where object_key=$1 and owner_id=$2 and status='ready'`, body.Key, userID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not schedule media deletion", id))
			return
		}
		if tag.RowsAffected() == 0 {
			writeJSON(w, http.StatusConflict, errorBody("MEDIA_STATE_CHANGED", "Media state changed; retry the request", id))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.deleteMediaObject(ctx, userID, body.Key, visibility); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	if err := s.deleteProductMedia(ctx, externalID); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.assets set status='deleted', updated_at=now()
		where object_key=$1 and owner_id=$2 and status='deletion_pending'`, body.Key, userID); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deleted"}, "error": nil, "request_id": id})
}

func (s *service) deleteMediaObject(ctx context.Context, userID, key, visibility string) error {
	deleteURL, err := s.media.PresignObjectDelete(userID, key, visibility, time.Minute, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return err
	}
	client := s.upstream
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	// Never forward a signed storage URL across redirects to another host.
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("object storage rejected deletion")
	}
	return nil
}

// runMediaJanitor expires unfinished upload reservations and retries durable
// deletions in bounded batches. It never expires a completed asset's intent as
// a reason to delete the completed object.
func (s *service) runMediaJanitor(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := s.cleanupMediaBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("media janitor pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type mediaCleanupObject struct {
	key, ownerID, visibility, externalID string
}

func (s *service) cleanupMediaBatch(ctx context.Context) error {
	if s.db == nil || s.media == nil {
		return nil
	}
	batchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	intentRows, err := s.db.Query(batchCtx, `select object_key, owner_id, visibility
		from testagram_media.upload_intents
		where expires_at <= now() and status in ('issued','cleanup_pending')
		order by expires_at asc limit 50`)
	if err != nil {
		return err
	}
	var intents []mediaCleanupObject
	for intentRows.Next() {
		var item mediaCleanupObject
		if err := intentRows.Scan(&item.key, &item.ownerID, &item.visibility); err != nil {
			intentRows.Close()
			return err
		}
		intents = append(intents, item)
	}
	if err := intentRows.Err(); err != nil {
		intentRows.Close()
		return err
	}
	intentRows.Close()
	for _, item := range intents {
		tag, err := s.db.Exec(batchCtx, `update testagram_media.upload_intents set status='cleanup_pending', updated_at=now()
			where object_key=$1 and status in ('issued','cleanup_pending')`, item.key)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Completion/cancellation may have won the race after selection.
			continue
		}
		if err := s.deleteMediaObject(batchCtx, item.ownerID, item.key, item.visibility); err != nil {
			continue
		}
		if _, err := s.db.Exec(batchCtx, `update testagram_media.upload_intents set status='expired', updated_at=now()
			where object_key=$1 and status='cleanup_pending'`, item.key); err != nil {
			return err
		}
	}

	assetRows, err := s.db.Query(batchCtx, `select object_key, owner_id, visibility, status, coalesce(external_media_id::text,'')
		from testagram_media.assets where status in ('deletion_pending','quarantine_pending')
		order by updated_at asc limit 50`)
	if err != nil {
		return err
	}
	type pendingMediaObject struct {
		mediaCleanupObject
		status string
	}
	var assets []pendingMediaObject
	for assetRows.Next() {
		var item pendingMediaObject
		if err := assetRows.Scan(&item.key, &item.ownerID, &item.visibility, &item.status, &item.externalID); err != nil {
			assetRows.Close()
			return err
		}
		assets = append(assets, item)
	}
	if err := assetRows.Err(); err != nil {
		assetRows.Close()
		return err
	}
	assetRows.Close()
	for _, item := range assets {
		if err := s.deleteMediaObject(batchCtx, item.ownerID, item.key, item.visibility); err != nil {
			continue
		}
		if err := s.deleteProductMedia(batchCtx, item.externalID); err != nil {
			continue
		}
		finalStatus := "deleted"
		if item.status == "quarantine_pending" {
			finalStatus = "quarantined"
		}
		if _, err := s.db.Exec(batchCtx, `update testagram_media.assets set status=$2, updated_at=now()
			where object_key=$1 and status=$3`, item.key, finalStatus, item.status); err != nil {
			return err
		}
	}
	return nil
}
