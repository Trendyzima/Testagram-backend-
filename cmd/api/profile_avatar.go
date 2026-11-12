package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

const maxAvatarImageBytes int64 = 2 << 20

func avatarImageType(data []byte) (string, error) {
	switch {
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", nil
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", nil
	default:
		return "", errors.New("avatar must be a valid JPEG, PNG or WebP image")
	}
}

func newAvatarObjectKey(userID string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate avatar object key: %w", err)
	}
	return path.Join("users", userID, hex.EncodeToString(raw[:])), nil
}

func (s *service) uploadAvatarToInstabox(ctx context.Context, userID, key, contentType string, data []byte) error {
	if s.virtualStorage == nil {
		return errors.New("Instabox VPS object storage is not configured")
	}
	target, err := s.virtualStorage.UploadURL(ctx, userID, "media-private", key, contentType, int64(len(data)))
	if err != nil {
		return fmt.Errorf("create Instabox avatar upload URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload avatar to Instabox: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Instabox avatar upload returned HTTP %d", resp.StatusCode)
	}
	if _, err := s.virtualStorage.VerifyUpload(ctx, userID, "media-private", key, contentType, int64(len(data))); err != nil {
		return fmt.Errorf("verify Instabox avatar upload: %w", err)
	}
	sum := sha256.Sum256(data)
	if err := s.virtualStorage.VerifyUploadChecksum(ctx, userID, "media-private", key, maxAvatarImageBytes, hex.EncodeToString(sum[:])); err != nil {
		return fmt.Errorf("verify Instabox avatar checksum: %w", err)
	}
	return nil
}

func queueAvatarCleanup(ctx context.Context, s *service, userID, key string) {
	if s == nil || s.db == nil || key == "" {
		return
	}
	_, _ = s.db.Exec(ctx, "update testagram_media.upload_intents set status='cleanup_pending',expires_at=now(),updated_at=now() where object_key=$1 and owner_id=$2 and status in ('issued','uploading','uploaded','cleanup_pending')", key, userID)
	_, _ = s.db.Exec(ctx, `insert into testagram_media.storage_deletion_jobs
		(object_key,owner_id,visibility,storage_namespace,storage_provider,reason,status)
		values ($1,$2,'private','media-private','instabox-vps','expired_upload','pending') on conflict do nothing`, key, userID)
}

func (s *service) profileAvatarUpload(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	userID, _, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if s.db == nil || s.virtualStorage == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_STORAGE_UNAVAILABLE", "Instabox profile storage is not configured", id))
		return
	}
	if r.ContentLength > maxAvatarImageBytes+(256<<10) {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("AVATAR_TOO_LARGE", "Profile pictures must be 2 MiB or smaller", id))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarImageBytes+(256<<10))
	if err := r.ParseMultipartForm(maxAvatarImageBytes + (256 << 10)); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_AVATAR_UPLOAD", "Upload a JPEG, PNG or WebP image no larger than 2 MiB", id))
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_AVATAR_UPLOAD", "The avatar image is required", id))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarImageBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maxAvatarImageBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("AVATAR_TOO_LARGE", "Profile pictures must be between 1 byte and 2 MiB", id))
		return
	}
	contentType, err := avatarImageType(data)
	if err != nil {
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody("AVATAR_TYPE_NOT_SUPPORTED", err.Error(), id))
		return
	}
	key, err := newAvatarObjectKey(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("AVATAR_ID_FAILED", "Could not allocate an avatar identifier", id))
		return
	}
	checksum := sha256.Sum256(data)
	checksumHex := hex.EncodeToString(checksum[:])
	filename := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if filename == "." || filename == "/" || len(filename) > 255 {
		filename = "avatar"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Could not reserve avatar storage", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext($1))", userID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CHECK_FAILED", "Could not reserve avatar storage", id))
		return
	}
	quotaBytes, parseErr := strconv.ParseInt(env("MEDIA_USER_QUOTA_BYTES", "2147483648"), 10, 64)
	if parseErr != nil || quotaBytes < maxAvatarImageBytes {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CONFIG_INVALID", "Profile media quota configuration is invalid", id))
		return
	}
	var usedBytes int64
	if err := tx.QueryRow(ctx, `select
		coalesce((select sum(size_bytes) from testagram_media.assets where owner_id=$1 and status in ('ready','deletion_pending')),0) +
		coalesce((select sum(max_bytes) from testagram_media.upload_intents where owner_id=$1 and status in ('issued','uploading','uploaded') and expires_at>now()),0)`, userID).Scan(&usedBytes); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CHECK_FAILED", "Could not calculate profile storage usage", id))
		return
	}
	if usedBytes >= quotaBytes || int64(len(data)) > quotaBytes-usedBytes {
		writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_QUOTA_EXCEEDED", "Profile media storage quota has been reached", id))
		return
	}
	_, err = tx.Exec(ctx, `insert into testagram_media.upload_intents
		(object_key, owner_id, visibility, content_type, original_name, max_bytes, expires_at, status, storage_namespace, storage_provider)
		values ($1,$2,'private',$3,$4,$5,now()+interval '10 minutes','issued','media-private','instabox-vps')`,
		key, userID, contentType, filename, int64(len(data)))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_RESERVATION_FAILED", "Could not record the avatar upload reservation", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_RESERVATION_FAILED", "Could not commit the avatar upload reservation", id))
		return
	}

	if err := s.uploadAvatarToInstabox(ctx, userID, key, contentType, data); err != nil {
		queueAvatarCleanup(context.Background(), s, userID, key)
		writeJSON(w, http.StatusBadGateway, errorBody("AVATAR_STORAGE_FAILED", "Instabox could not store the avatar", id))
		return
	}

	tx, err = s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_PROFILE_UPDATE_FAILED", "Avatar stored but profile update could not start", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var oldKey, oldProvider string
	err = tx.QueryRow(ctx, `select coalesce(avatar_key,''),coalesce(avatar_storage_provider,'legacy-media')
		from testagram_local.accounts where user_id=$1 and account_status='active' for update`, userID).Scan(&oldKey, &oldProvider)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("PROFILE_NOT_FOUND", "Active profile was not found", id))
		return
	}
	_, err = tx.Exec(ctx, `insert into testagram_media.assets
		(object_key,owner_id,visibility,content_type,original_name,size_bytes,etag,status,storage_namespace,storage_provider,checksum_sha256)
		values ($1,$2,'private',$3,$4,$5,'','ready','media-private','instabox-vps',$6)`,
		key, userID, contentType, filename, int64(len(data)), checksumHex)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_METADATA_FAILED", "Could not record the stored avatar", id))
		return
	}
	if _, err := tx.Exec(ctx, "update testagram_media.upload_intents set status='completed',updated_at=now() where object_key=$1 and owner_id=$2 and status='issued'", key, userID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_METADATA_FAILED", "Could not complete the avatar reservation", id))
		return
	}
	if _, err := tx.Exec(ctx, "update testagram_local.accounts set avatar_key=$2,avatar_storage_provider='instabox-vps' where user_id=$1 and account_status='active'", userID, key); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_PROFILE_UPDATE_FAILED", "Could not update the profile avatar", id))
		return
	}
	if oldKey != "" && oldKey != key && oldProvider == "instabox-vps" {
		_, _ = tx.Exec(ctx, `update testagram_media.assets set status='deletion_pending',updated_at=now()
			where object_key=$1 and owner_id=$2 and status='ready'`, oldKey, userID)
		_, _ = tx.Exec(ctx, `insert into testagram_media.storage_deletion_jobs
			(object_key,owner_id,visibility,storage_namespace,storage_provider,reason,status)
			values ($1,$2,'private','media-private','instabox-vps','user_delete','pending') on conflict do nothing`, oldKey, userID)
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_PROFILE_UPDATE_FAILED", "Could not commit the profile avatar update", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{
			"avatar_key": key, "avatar_storage_provider": "instabox-vps", "avatar_url": profileAvatarURL(userID),
		}, "error": nil, "request_id": id,
	})
}

func profileAvatarURL(userID string) string {
	base, err := url.Parse(strings.TrimRight(env("API_PUBLIC_BASE_URL", "https://api.testagram.site"), "/"))
	if err != nil || base.Host == "" || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && base.Scheme != "http") {
		return ""
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/social/profiles/" + url.PathEscape(userID) + "/avatar"
	return base.String()
}

// profileAvatarRead authorizes every request and streams private bytes through
// the API edge. It never returns an Instabox URL or allows shared CDN caching.
func (s *service) profileAvatarRead(w http.ResponseWriter, r *http.Request) {
	requesterID, id, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	ownerID := strings.ToLower(strings.TrimSpace(r.PathValue("user_id")))
	if !socialUUIDPattern.MatchString(ownerID) || s.db == nil || s.virtualStorage == nil {
		writeJSON(w, http.StatusNotFound, errorBody("AVATAR_NOT_FOUND", "Avatar is unavailable", id))
		return
	}
	var key string
	err := s.db.QueryRow(r.Context(), `select a.avatar_key from testagram_local.accounts a
		where a.user_id=$2 and a.account_status='active'
		and (a.user_id=$1 or (not a.protected_account and a.discoverable_by_username)
			or exists(select 1 from testagram_local.follows f where f.follower_id=$1 and f.following_id=a.user_id and f.status='accepted'))
		and not exists(select 1 from testagram_local.blocks b where (b.blocker_id=$1 and b.blocked_id=a.user_id) or (b.blocker_id=a.user_id and b.blocked_id=$1))`, requesterID, ownerID).Scan(&key)
	if err != nil || key == "" {
		writeJSON(w, http.StatusNotFound, errorBody("AVATAR_NOT_FOUND", "Avatar is unavailable", id))
		return
	}
	var namespace, contentType, storageProvider string
	var sizeBytes int64
	if err := s.db.QueryRow(r.Context(), `select storage_namespace,storage_provider,content_type,size_bytes from testagram_media.assets
		where object_key=$1 and owner_id=$2 and visibility='private' and status='ready'`, key, ownerID).Scan(&namespace, &storageProvider, &contentType, &sizeBytes); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("AVATAR_NOT_FOUND", "Avatar is unavailable", id))
		return
	}
	if namespace != "media-private" || storageProvider != "instabox-vps" || sizeBytes < 1 || sizeBytes > maxAvatarImageBytes {
		writeJSON(w, http.StatusNotFound, errorBody("AVATAR_NOT_FOUND", "Avatar is unavailable", id))
		return
	}
	signedURL, err := s.virtualStorage.DownloadURL(r.Context(), ownerID, ownerID, namespace, key)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AVATAR_DELIVERY_UNAVAILABLE", "Avatar delivery is temporarily unavailable", id))
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, signedURL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("AVATAR_DELIVERY_FAILED", "Could not prepare avatar delivery", id))
		return
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(request)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("AVATAR_DELIVERY_FAILED", "Instabox avatar storage is temporarily unavailable", id))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.ContentLength > maxAvatarImageBytes {
		writeJSON(w, http.StatusBadGateway, errorBody("AVATAR_DELIVERY_FAILED", "Instabox could not deliver this avatar", id))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Vary", "Authorization, Cookie")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxAvatarImageBytes+1))
}
