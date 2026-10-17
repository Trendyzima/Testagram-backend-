package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxMediaIntentBody = 16 << 10

type mediaUploadIntentRequest struct {
	ContentType string `json:"content_type"`
	Visibility  string `json:"visibility"`
	SizeBytes   int64  `json:"size_bytes"`
}

type privateMediaDownloadRequest struct {
	Key string `json:"key"`
}

type mediaCompleteRequest struct {
	Key string `json:"key"`
	Visibility string `json:"visibility"`
	ContentType string `json:"content_type"`
}

type mediaAsset struct {
	Key string `json:"key"`
	Visibility string `json:"visibility"`
	ContentType string `json:"content_type"`
	SizeBytes int64 `json:"size_bytes"`
	ETag string `json:"etag,omitempty"`
	Status string `json:"status"`
	CreatedAt time.Time `json:"created_at"`
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
	intent, err := s.media.CreateUploadIntent(userID, body.ContentType, body.Visibility, body.SizeBytes, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", err.Error(), id))
		return
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
		(object_key, owner_id, visibility, content_type, max_bytes, expires_at, status)
		values ($1,$2,$3,$4,$5,$6,'issued')`,
		intent.Key, userID, body.Visibility, body.ContentType, body.SizeBytes, intent.ExpiresAt)
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
	signedURL, err := s.media.PresignPrivateDownload(userID, body.Key, 5*time.Minute, time.Now())
	if err != nil {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_ACCESS_DENIED", "This private media object is not available to this account", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"url": signedURL, "expires_in_seconds": 300}, "error": nil, "request_id": id})
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
		if status >= 500 { code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable" }
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	var body mediaCompleteRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key, visibility and content type are required", id))
		return
	}
	maxBytes, ok := s.media.MaxBytesForContentType(body.ContentType)
	if !ok || (body.Visibility != "public" && body.Visibility != "private") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "Unsupported media type or visibility", id))
		return
	}
	body.ContentType = strings.ToLower(strings.TrimSpace(body.ContentType))
	var intentOwner, intentVisibility, intentType, intentStatus string
	var intentMaxBytes int64
	var intentExpires time.Time
	err = s.db.QueryRow(r.Context(), `select owner_id, visibility, content_type, max_bytes, expires_at, status
		from testagram_media.upload_intents where object_key=$1`, body.Key).
		Scan(&intentOwner, &intentVisibility, &intentType, &intentMaxBytes, &intentExpires, &intentStatus)
	if err != nil || intentOwner != userID || intentVisibility != body.Visibility || intentType != body.ContentType ||
		(time.Now().After(intentExpires) && intentStatus != "completed") || (intentStatus != "issued" && intentStatus != "completed") {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_INTENT_INVALID", "Upload intent is missing, expired, or does not match this account and object", id))
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
	if client == nil { client = &http.Client{Timeout: 4*time.Second} }
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
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_OBJECT_INVALID", "Uploaded object type or size does not satisfy media policy", id))
		return
	}
	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	var asset mediaAsset
	err = s.db.QueryRow(ctx, `insert into testagram_media.assets
		(object_key, owner_id, visibility, content_type, size_bytes, etag, status)
		values ($1,$2,$3,$4,$5,$6,'ready')
		on conflict (object_key) do nothing
		returning object_key, visibility, content_type, size_bytes, etag, status, created_at`,
		body.Key, userID, body.Visibility, actualType, size, etag).
		Scan(&asset.Key, &asset.Visibility, &asset.ContentType, &asset.SizeBytes, &asset.ETag, &asset.Status, &asset.CreatedAt)
	if err != nil {
		var ownerID string
		err = s.db.QueryRow(ctx, `select object_key, owner_id, visibility, content_type, size_bytes, etag, status, created_at
			from testagram_media.assets where object_key=$1`, body.Key).
			Scan(&asset.Key, &ownerID, &asset.Visibility, &asset.ContentType, &asset.SizeBytes, &asset.ETag, &asset.Status, &asset.CreatedAt)
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
