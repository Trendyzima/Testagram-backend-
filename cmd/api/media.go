package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/datacentreclient"
	"github.com/Trendyzima/Testagram-backend-/internal/media"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
	"github.com/jackc/pgx/v5/pgconn"
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

type cdnMediaDownloadRequest struct {
	Key string `json:"key"`
}

type mediaCompleteRequest struct {
	Key            string `json:"key"`
	Visibility     string `json:"visibility"`
	ContentType    string `json:"content_type"`
	ChecksumSHA256 string `json:"checksum_sha256"`
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
	MediaType    string    `json:"media_type,omitempty"`
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
	if (s.media == nil && s.virtualStorage == nil && s.dataCentreMedia == nil) || (s.freshMediaEnabled && (s.dataCentreMedia == nil || s.mediaIntentFactory == nil)) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
		return
	}
	var body mediaUploadIntentRequest
	if err := decodeMediaJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "Request must contain valid JSON media metadata", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	allowed, retryAfter, rateErr := s.checkCapabilityRateLimit(ctx, "media-upload:"+userID, 10)
	cancel()
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_RATE_LIMIT_UNAVAILABLE", "Media upload protection is temporarily unavailable", id))
		return
	}
	if !allowed {
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconvFormatInt(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, errorBody("MEDIA_RATE_LIMITED", "Too many upload intents; try again later", id))
		return
	}
	if body.Visibility == "public" && body.SizeBytes > maxProductPostMediaBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("MEDIA_SIZE_NOT_SUPPORTED", "Public product media must not exceed 20 MiB", id))
		return
	}
	body.Name, err = sanitizeOriginalName(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "File name must be 255 characters or fewer and contain no line breaks", id))
		return
	}
	var intent media.UploadIntent
	if s.freshMediaEnabled {
		intent, err = s.mediaIntentFactory.CreateUploadIntent(userID, body.ContentType, body.Visibility, body.SizeBytes, time.Now())
	} else {
		intent, err = s.media.CreateUploadIntent(userID, body.ContentType, body.Visibility, body.SizeBytes, time.Now())
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", err.Error(), id))
		return
	}
	storageNamespace, storageProvider := "legacy-media", "default"
	if s.freshMediaEnabled {
		storageNamespace, storageProvider = intent.Bucket, "data-centre"
	} else if s.virtualStorage != nil {
		storageNamespace = "media-private"
		if body.Visibility == "public" {
			storageNamespace = "media-public"
		}
		storageProvider = "s3-default"
		// Keep the provider URL server-side. Clients upload through the bounded
		// API gateway, which enforces the intent size while bytes are arriving.
		_, err = s.virtualStorage.UploadURL(r.Context(), userID, storageNamespace, intent.Key, body.ContentType, body.SizeBytes)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("VIRTUAL_STORAGE_UPLOAD_URL_FAILED", "Could not create a tenant-scoped upload URL", id))
			return
		}
	}
	// Both legacy and virtual-storage intents use the bounded API upload route.
	// The server selects the configured storage adapter; browser code never needs
	// to handle or trust a provider URL.
	intent.URL = "/api/media/upload?key=" + url.QueryEscape(intent.Key)
	if body.Visibility == "public" {
		intent.PublicURL = publicMediaURLForStorage(intent.Key, userID, storageNamespace, storageProvider)
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
		coalesce((select sum(max_bytes) from testagram_media.upload_intents where owner_id=$1 and status in ('issued','uploading','uploaded') and expires_at > now()),0)`, userID).Scan(&usedBytes)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_QUOTA_CHECK_FAILED", "Could not calculate media quota usage", id))
		return
	}
	if usedBytes >= quotaBytes || body.SizeBytes > quotaBytes-usedBytes {
		writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_QUOTA_EXCEEDED", "This upload would exceed the account media storage quota", id))
		return
	}
	_, err = tx.Exec(r.Context(), `insert into testagram_media.upload_intents
		(object_key, owner_id, visibility, content_type, original_name, max_bytes, expires_at, status, storage_namespace, storage_provider)
		values ($1,$2,$3,$4,$5,$6,$7,'issued',$8,$9)`,
		intent.Key, userID, body.Visibility, body.ContentType, body.Name, body.SizeBytes, intent.ExpiresAt, storageNamespace, storageProvider)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "53100" {
			switch pgErr.Message {
			case "TESTAGRAM_GLOBAL_STORAGE_LIMIT":
				writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_GLOBAL_STORAGE_LIMIT", "Testagram's shared media storage pool is full; try again after capacity is added", id))
			case "TESTAGRAM_USER_STORAGE_LIMIT":
				writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_QUOTA_EXCEEDED", "This upload would exceed the account media storage quota", id))
			default:
				writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_STORAGE_LIMIT", "The media storage capacity limit was reached", id))
			}
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_INTENT_PERSIST_FAILED", "Could not safely register the upload intent", id))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_INTENT_PERSIST_FAILED", "Could not commit the upload reservation", id))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": intent, "error": nil, "request_id": id})
}

// copyBoundedUpload enforces a hard byte cap during reads, including
// requests that omit Content-Length and arrive with chunked transfer encoding.
func copyBoundedUpload(w http.ResponseWriter, src io.ReadCloser, maxBytes int64, dst io.Writer) (int64, error) {
	if maxBytes < 1 {
		return 0, errors.New("upload byte limit must be positive")
	}
	return io.Copy(dst, http.MaxBytesReader(w, src, maxBytes))
}

// mediaUploadGateway enforces the intent's byte limit while bytes arrive at
// this API, then forwards the bounded spool to the private S3-compatible URL.
// This is intentionally a gateway path, not a redirect to an unrestricted PUT.
func (s *service) mediaUploadGateway(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if r.Method == http.MethodOptions {
		s.mediaPreflight(w, r)
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Use PUT to upload media bytes", id))
		return
	}
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil || (s.virtualStorage == nil && s.media == nil && s.dataCentreMedia == nil) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Bounded upload gateway is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" || len(key) > 1024 || strings.ContainsAny(key, "\\\x00\r\n") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_KEY", "A valid upload intent key is required", id))
		return
	}
	var ownerID, contentType, namespace, visibility, intentStatus, storageProvider string
	var maxBytes int64
	var expiresAt time.Time
	err = s.db.QueryRow(r.Context(), `select owner_id, content_type, storage_namespace, visibility, max_bytes, status, expires_at, storage_provider
		from testagram_media.upload_intents where object_key=$1`, key).
		Scan(&ownerID, &contentType, &namespace, &visibility, &maxBytes, &intentStatus, &expiresAt, &storageProvider)
	if err != nil || ownerID != userID || intentStatus != "issued" || !time.Now().Before(expiresAt) || maxBytes < 1 ||
		(namespace == "legacy-media" && s.media == nil) || (storageProvider == "data-centre" && s.dataCentreMedia == nil) || (namespace != "legacy-media" && storageProvider != "data-centre" && s.virtualStorage == nil) {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_INTENT_INVALID", "Upload intent is missing, expired, or does not belong to this account", id))
		return
	}
	if got := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); got != contentType {
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody("MEDIA_CONTENT_TYPE_MISMATCH", "Content-Type must match the upload intent", id))
		return
	}
	if r.ContentLength > maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("MEDIA_TOO_LARGE", "Upload exceeds the reserved byte limit", id))
		return
	}
	// Atomically claim before reading or spooling any request bytes. Only one
	// concurrent/replayed PUT can transition an intent from issued to uploading.
	claim, claimErr := s.db.Exec(r.Context(), "update testagram_media.upload_intents set status='uploading', updated_at=now() where object_key=$1 and owner_id=$2 and status='issued' and expires_at > now() and storage_namespace=$3 and max_bytes=$4", key, userID, namespace, maxBytes)
	if claimErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_INTENT_CLAIM_FAILED", "Could not safely claim upload intent", id))
		return
	}
	if claim.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_INTENT_ALREADY_USED", "Upload intent is expired, already in use, or already consumed", id))
		return
	}
	tmp, err := os.CreateTemp("", "testagram-upload-*")
	if err != nil {
		writeJSON(w, http.StatusInsufficientStorage, errorBody("MEDIA_SPOOL_UNAVAILABLE", "Could not allocate bounded upload spool", id))
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()
	hasher := sha256.New()
	n, copyErr := copyBoundedUpload(w, r.Body, maxBytes, io.MultiWriter(tmp, hasher))
	if copyErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("MEDIA_TOO_LARGE", "Upload exceeded the reserved byte limit while streaming", id))
			return
		}
		writeJSON(w, http.StatusBadRequest, errorBody("MEDIA_UPLOAD_INCOMPLETE", "Upload stream did not complete", id))
		return
	}
	if n != maxBytes {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_SIZE_MISMATCH", "Uploaded byte count must exactly match the reserved size", id))
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("MEDIA_SPOOL_FAILED", "Could not rewind bounded upload", id))
		return
	}
	var target string
	if namespace == "legacy-media" {
		target, err = s.media.PresignObjectPut(userID, key, visibility, contentType, 10*time.Minute, time.Now())
	} else if storageProvider == "data-centre" {
		var grant datacentreclient.UploadGrant
		grant, err = s.dataCentreMedia.CreateUploadURL(r.Context(), userID, namespace, key, contentType, maxBytes)
		target = grant.UploadURL
	} else {
		target, err = s.virtualStorage.UploadURL(r.Context(), userID, namespace, key, contentType, maxBytes)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_STORAGE_SIGN_FAILED", "Could not authorize storage upload", id))
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPut, target, tmp)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_STORAGE_REQUEST_FAILED", "Could not prepare storage upload", id))
		return
	}
	req.ContentLength = n
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 40 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_STORAGE_UPLOAD_FAILED", "Object storage could not accept the upload", id))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_STORAGE_UPLOAD_FAILED", "Object storage rejected the bounded upload", id))
		return
	}
	checksumSHA256 := hex.EncodeToString(hasher.Sum(nil))
	completed, stateErr := s.db.Exec(r.Context(), "update testagram_media.upload_intents set status='uploaded', checksum_sha256=$3, updated_at=now() where object_key=$1 and owner_id=$2 and status='uploading' and expires_at > now()", key, userID, checksumSHA256)
	if stateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_INTENT_STATE_FAILED", "Object uploaded but intent state could not be saved; stale-upload cleanup will reconcile it", id))
		return
	}
	if completed.RowsAffected() != 1 {
		_, _ = s.db.Exec(r.Context(), "update testagram_media.upload_intents set status='cleanup_pending', expires_at=now(), updated_at=now() where object_key=$1 and owner_id=$2 and status='uploading'", key, userID)
		_, _ = s.db.Exec(r.Context(), "insert into testagram_media.storage_deletion_jobs (object_key, owner_id, visibility, storage_namespace, storage_provider, reason, status) select object_key, owner_id, visibility, storage_namespace, storage_provider, 'expired_upload', 'pending' from testagram_media.upload_intents where object_key=$1 and owner_id=$2 on conflict do nothing", key, userID)
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_INTENT_EXPIRED", "Upload completed after its intent expired; cleanup was queued", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mediaCDNDownloadURL issues a short-lived assertion for one ready private asset.

const maxCDNOriginObjectBytes int64 = 1 << 30

func cdnOriginObjectSize(resp *http.Response) (int64, error) {
 if value:=strings.TrimSpace(resp.Header.Get("Content-Range"));value!="" {
  slash:=strings.LastIndex(value,"/")
  if slash<0 { return -1,errors.New("malformed origin Content-Range") }
  size,err:=strconv.ParseInt(value[slash+1:],10,64)
  if err!=nil||size<0 { return -1,errors.New("malformed origin Content-Range size") }
  return size,nil
 }
 if resp.ContentLength>=0 { return resp.ContentLength,nil }
 return -1,errors.New("origin omitted a trustworthy media object size")
}

func (s *service) mediaCDNOrigin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Testagram-Visibility", "private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cdnMediaAssertions == nil || len(s.cdnOriginAuthToken) < 32 || s.db == nil {
		http.Error(w, "trusted media origin is not configured", http.StatusServiceUnavailable)
		return
	}
	serviceToken := strings.TrimSpace(r.Header.Get("X-Testagram-Origin-Auth"))
	if serviceToken == "" || strings.ContainsAny(serviceToken, " \t\r\n") || subtle.ConstantTimeCompare([]byte(serviceToken), []byte(s.cdnOriginAuthToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	key := r.PathValue("key")
	var ownerID, visibility, assetStatus, storageNamespace, storageProvider string
	err := s.db.QueryRow(r.Context(), "select owner_id, visibility, status, storage_namespace, storage_provider from testagram_media.assets where object_key=$1", key).Scan(&ownerID, &visibility, &assetStatus, &storageNamespace, &storageProvider)
	if err != nil || assetStatus != "ready" {
		http.Error(w, "media object unavailable", http.StatusNotFound)
		return
	}
	viewerHeader := strings.TrimSpace(r.Header.Get("X-Testagram-Viewer-Authorization"))
	requesterID := ownerID
	if !strings.HasPrefix(key, "users/"+ownerID+"/") {
		http.Error(w, "media object owner mismatch", http.StatusNotFound)
		return
	}
	if viewerHeader != "" {
		if !strings.HasPrefix(viewerHeader, "Bearer ") || len(viewerHeader) > 8192 || strings.ContainsAny(viewerHeader, "\r\n") {
			http.Error(w, "invalid viewer assertion", http.StatusUnauthorized)
			return
		}
		claims, verifyErr := s.cdnMediaAssertions.VerifyForKey(strings.TrimSpace(strings.TrimPrefix(viewerHeader, "Bearer ")), key, time.Now())
		if verifyErr != nil {
			http.Error(w, "media authorization denied", http.StatusForbidden)
			return
		}
		claimOwner := claims.Owner
		if claimOwner == "" { claimOwner = claims.Subject }
		if visibility != "private" || claimOwner != ownerID {
			http.Error(w, "media authorization denied", http.StatusNotFound)
			return
		}
	} else if visibility != "public" {
		// No viewer assertion is accepted only for a canonical ready public asset.
		http.Error(w, "viewer assertion required", http.StatusUnauthorized)
		return
	}
	var signedURL string
	if storageProvider == "data-centre" {
		if s.dataCentreMedia == nil { http.Error(w, "Data Centre media service unavailable", http.StatusServiceUnavailable); return }
		var grant datacentreclient.DownloadGrant
		if r.Method == http.MethodHead {
			grant, err = s.dataCentreMedia.HeadURL(r.Context(), requesterID, ownerID, storageNamespace, key)
		} else {
			grant, err = s.dataCentreMedia.DownloadURL(r.Context(), requesterID, ownerID, storageNamespace, key)
		}
		signedURL = grant.URL
	} else if storageNamespace != "legacy-media" && s.virtualStorage != nil {
		if r.Method == http.MethodHead {
			signedURL, err = s.virtualStorage.HeadURL(r.Context(), ownerID, ownerID, storageNamespace, key)
		} else {
			signedURL, err = s.virtualStorage.DownloadURL(r.Context(), ownerID, ownerID, storageNamespace, key)
		}
	} else if storageNamespace == "legacy-media" && s.media != nil {
		if r.Method == http.MethodHead {
			signedURL, err = s.media.PresignObjectHead(ownerID, key, visibility, time.Minute, time.Now())
		} else {
			signedURL, err = s.media.PresignObjectGet(ownerID, key, visibility, time.Minute, time.Now())
		}
	} else {
		err = errors.New("media storage adapter unavailable")
	}
	if err != nil || signedURL == "" {
		http.Error(w, "media origin unavailable", http.StatusBadGateway)
		return
	}
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, signedURL, nil)
	if err != nil {
		http.Error(w, "media origin unavailable", http.StatusBadGateway)
		return
	}
	copyCDNOriginRequestHeaders(r, upstreamReq)
	client := &http.Client{Transport: s.upstream.Transport, Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		http.Error(w, "media origin unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
	case http.StatusNotFound, http.StatusGone:
		http.Error(w, "media object unavailable", http.StatusNotFound)
		return
	default:
		http.Error(w, "media origin unavailable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusNotModified {
		size, sizeErr := cdnOriginObjectSize(resp)
		if sizeErr != nil { http.Error(w, "media origin returned incomplete size metadata", http.StatusBadGateway); return }
		if size > maxCDNOriginObjectBytes { http.Error(w, "media object exceeds the CDN delivery limit", http.StatusRequestEntityTooLarge); return }
		if (resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusRequestedRangeNotSatisfiable) && strings.TrimSpace(resp.Header.Get("Content-Range")) == "" {
			http.Error(w, "media origin omitted range metadata", http.StatusBadGateway)
			return
		}
	}
	writeCDNOriginResponse(w, r, resp, key, visibility)
}
func copyCDNOriginRequestHeaders(source,destination *http.Request) {
 for _,name:=range []string{"Range","If-Range","If-None-Match","If-Modified-Since"} {
  if value:=strings.TrimSpace(source.Header.Get(name));value!="" { destination.Header.Set(name,value) }
 }
}

func writeCDNOriginResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, objectKey, visibility string) {
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(name); value != "" { w.Header().Set(name, value) }
	}
	if visibility == "public" {
		w.Header().Set("Cache-Control", "public, max-age=0, s-maxage=300")
		w.Header().Set("X-Testagram-Visibility", "public")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Testagram-Visibility", "private")
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNotModified { return }
	if n, copyErr := io.Copy(w, resp.Body); copyErr != nil { log.Printf("CDN media stream interrupted after %d bytes for %s: %v", n, objectKey, copyErr) }
}

func (s *service) mediaCDNDownloadURL(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	w.Header().Set("Cache-Control", "no-store")
	if !s.cors(w, r) { writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id)); return }
	if s.cdnMediaAssertions == nil || s.cdnMediaBaseURL == "" { writeJSON(w, http.StatusServiceUnavailable, errorBody("CDN_MEDIA_NOT_CONFIGURED", "CDN media delivery is not configured", id)); return }
	if s.db == nil { writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Media metadata database is unavailable", id)); return }
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil { code,message:="AUTH_REQUIRED","A valid authenticated session is required";if status>=500{code,message="AUTH_BACKEND_UNAVAILABLE","Authentication service is temporarily unavailable"};writeJSON(w,status,errorBody(code,message,id));return }
	var body cdnMediaDownloadRequest
	if err:=decodeMediaJSON(w,r,&body);err!=nil||strings.TrimSpace(body.Key)==""{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MEDIA_REQUEST","A valid media key is required",id));return}
	var visibility,assetStatus,storageProvider string
	err=s.db.QueryRow(r.Context(),"select visibility, status, storage_provider from testagram_media.assets where object_key=$1 and owner_id=$2",body.Key,userID).Scan(&visibility,&assetStatus,&storageProvider)
	if err!=nil||visibility!="private"||assetStatus!="ready"||storageProvider=="fresh-b2-r2"{writeJSON(w,http.StatusNotFound,errorBody("MEDIA_NOT_FOUND","Private media was not found for this account",id));return}
	assertion,expiresAt,err:=s.cdnMediaAssertions.Issue(body.Key,userID,time.Now())
	if err!=nil{writeJSON(w,http.StatusForbidden,errorBody("MEDIA_ACCESS_DENIED","This private media object cannot be authorized",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"url":s.cdnMediaBaseURL+"/v1/"+body.Key,"authorization":"Bearer "+assertion,"expires_at":expiresAt,"expires_in_seconds":int64(time.Until(expiresAt).Seconds()),"visibility":"private"},"error":nil,"request_id":id})
}

func (s *service) mediaPrivateDownloadURL(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.media == nil && s.dataCentreMedia == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage is not configured on this server", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
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
	var visibility, assetStatus, storageNamespace, storageProvider string
	if err := s.db.QueryRow(r.Context(), `select visibility, status, storage_namespace, storage_provider from testagram_media.assets where object_key=$1 and owner_id=$2`, body.Key, userID).Scan(&visibility, &assetStatus, &storageNamespace, &storageProvider); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Private media was not found for this account", id))
		return
	}
	if visibility != "private" || assetStatus != "ready" {
		writeJSON(w, http.StatusNotFound, errorBody("MEDIA_NOT_FOUND", "Private media was not found for this account", id))
		return
	}
	var signedURL string
	if storageProvider == "data-centre" {
		if s.dataCentreMedia == nil { writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Data Centre media service is unavailable", id)); return }
		var grant datacentreclient.DownloadGrant
		grant, err = s.dataCentreMedia.DownloadURL(r.Context(), userID, userID, storageNamespace, body.Key)
		signedURL = grant.URL
	} else if storageNamespace != "legacy-media" {
		if s.virtualStorage == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("VIRTUAL_STORAGE_NOT_CONFIGURED", "This media object requires the virtual storage provider", id))
			return
		}
		signedURL, err = s.virtualStorage.DownloadURL(r.Context(), userID, userID, storageNamespace, body.Key)
	} else {
		signedURL, err = s.media.PresignPrivateDownload(userID, body.Key, 5*time.Minute, time.Now())
	}
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
	if (s.media == nil && s.dataCentreMedia == nil) || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_UNAVAILABLE", "Media storage or metadata database is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
		return
	}
	rows, err := s.db.Query(r.Context(), `select object_key, visibility, content_type, original_name, size_bytes, created_at, storage_namespace, storage_provider
		from testagram_media.assets where owner_id=$1 and status='ready'
		order by created_at desc limit 100`, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not list media assets", id))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var key, visibility, contentType, originalName, storageNamespace, storageProvider string
		var sizeBytes int64
		var createdAt time.Time
		if err := rows.Scan(&key, &visibility, &contentType, &originalName, &sizeBytes, &createdAt, &storageNamespace, &storageProvider); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not read media assets", id))
			return
		}
		mediaURL := ""
		if storageProvider == "data-centre" && s.dataCentreMedia == nil || storageProvider != "data-centre" && storageNamespace != "legacy-media" && s.virtualStorage == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("VIRTUAL_STORAGE_NOT_CONFIGURED", "Some media objects require the virtual storage provider", id))
			return
		}
		if visibility == "public" {
			mediaURL = publicMediaURLForStorage(key, userID, storageNamespace, storageProvider)
		} else if visibility == "private" {
			if storageProvider == "data-centre" {
				var grant datacentreclient.DownloadGrant
				grant, err = s.dataCentreMedia.DownloadURL(r.Context(), userID, userID, storageNamespace, key)
				mediaURL = grant.URL
			} else if storageNamespace != "legacy-media" {
				mediaURL, err = s.virtualStorage.DownloadURL(r.Context(), userID, userID, storageNamespace, key)
			} else {
				mediaURL, err = s.media.PresignPrivateDownload(userID, key, 5*time.Minute, time.Now())
			}
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
	if (s.media == nil && s.dataCentreMedia == nil) || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage or metadata database is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
		return
	}
	var body mediaCompleteRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key is required", id))
		return
	}
	var intentOwner, intentVisibility, intentType, intentName, intentStatus, storageNamespace, storageProvider, intentChecksum string
	var intentMaxBytes int64
	var intentExpires time.Time
	err = s.db.QueryRow(r.Context(), `select owner_id, visibility, content_type, original_name, max_bytes, expires_at, status, storage_namespace, storage_provider, coalesce(checksum_sha256, '')
		from testagram_media.upload_intents where object_key=$1`, body.Key).
		Scan(&intentOwner, &intentVisibility, &intentType, &intentName, &intentMaxBytes, &intentExpires, &intentStatus, &storageNamespace, &storageProvider, &intentChecksum)
	if err != nil || intentOwner != userID ||
		(time.Now().After(intentExpires) && intentStatus != "completed") ||
		(storageNamespace == "legacy-media" && intentStatus != "issued" && intentStatus != "uploaded" && intentStatus != "completed") ||
		(storageNamespace != "legacy-media" && intentStatus != "uploaded" && intentStatus != "completed") {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_INTENT_INVALID", "Upload intent is missing, expired, or does not belong to this account", id))
		return
	}
	if storageNamespace != "legacy-media" && storageProvider != "data-centre" && s.mediaReplicator == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_REPLICATION_NOT_CONFIGURED", "Three-provider media verification is not configured; this asset cannot be published", id))
		return
	}
	if strings.TrimSpace(body.Visibility) == "" {
		body.Visibility = intentVisibility
	}
	if strings.TrimSpace(body.ContentType) == "" {
		body.ContentType = intentType
	}
	body.ContentType = strings.ToLower(strings.TrimSpace(body.ContentType))
body.ChecksumSHA256 = strings.ToLower(strings.TrimSpace(body.ChecksumSHA256))
intentChecksum = strings.ToLower(strings.TrimSpace(intentChecksum))
if body.ChecksumSHA256 == "" { body.ChecksumSHA256 = intentChecksum } else if intentChecksum != "" && body.ChecksumSHA256 != intentChecksum {
		writeJSON(w, http.StatusConflict, errorBody("MEDIA_CHECKSUM_MISMATCH", "Client checksum does not match the server-computed upload checksum", id)); return
	}
if body.ChecksumSHA256 == "" && storageNamespace != "legacy-media" { writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_CHECKSUM_MISSING", "The upload gateway has not recorded a server-computed checksum", id)); return }
	if intentVisibility != body.Visibility || intentType != body.ContentType {
		writeJSON(w, http.StatusForbidden, errorBody("MEDIA_INTENT_INVALID", "Completion metadata does not match the stored upload intent", id))
		return
	}
	var maxBytes int64
	var ok bool
	if storageProvider == "data-centre" { maxBytes, ok = s.mediaIntentFactory.MaxBytesForContentType(body.ContentType) } else { maxBytes, ok = s.media.MaxBytesForContentType(body.ContentType) }
	if !ok || (body.Visibility != "public" && body.Visibility != "private") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "Unsupported media type or visibility", id))
		return
	}
	if intentMaxBytes < 1 || intentMaxBytes > maxBytes {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_INTENT_INVALID", "Stored upload limit is invalid", id))
		return
	}
	verifyTimeout := 8 * time.Second
	if storageProvider == "data-centre" { verifyTimeout = 25 * time.Second }
	ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()
	var actualType, etag string
	var size int64
	if storageProvider == "data-centre" && s.dataCentreMedia == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Data Centre media service is unavailable", id))
		return
	}
	if storageProvider != "data-centre" && storageNamespace != "legacy-media" && s.virtualStorage == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("VIRTUAL_STORAGE_NOT_CONFIGURED", "This upload intent requires the virtual storage provider", id))
		return
	}
	if storageProvider == "data-centre" {
		if _, verifyErr := s.dataCentreMedia.Verify(ctx, userID, storageNamespace, body.Key, body.ContentType, body.ChecksumSHA256, intentMaxBytes); verifyErr != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_VERIFY_FAILED", "Data Centre could not queue media verification and replica recovery", id))
			return
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			mediaState, statusErr := s.dataCentreMedia.Status(ctx, userID, storageNamespace, body.Key)
			if statusErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_REPLICA_STATUS_UNAVAILABLE", "Data Centre media verification status is temporarily unavailable", id))
				return
			}
			if mediaState.Status == "protected" {
				if subtle.ConstantTimeCompare([]byte(strings.ToLower(mediaState.SHA256)), []byte(body.ChecksumSHA256)) != 1 {
					writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_REPLICA_DIGEST_MISMATCH", "Data Centre protected digest does not match the Backend upload digest", id))
					return
				}
				actualType, size, etag = mediaState.ContentType, mediaState.SizeBytes, mediaState.SHA256
				break
			}
			if mediaState.Status == "replication_failed" {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_REPLICA_FAILED", "Media is uploaded but its durable replica is not ready; retry completion", id))
				return
			}
			if mediaState.Status == "deleted" || mediaState.Status == "delete_pending" {
				writeJSON(w, http.StatusConflict, errorBody("MEDIA_OBJECT_DELETING", "Media object is being deleted and cannot be completed", id))
				return
			}
			if !time.Now().Before(deadline) || ctx.Err() != nil {
				writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "replication_pending", "retry_after_ms": 1000}, "error": nil, "request_id": id})
				return
			}
			select {
			case <-ctx.Done():
				writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "replication_pending", "retry_after_ms": 1000}, "error": nil, "request_id": id})
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	} else if storageNamespace != "legacy-media" {
		info, verifyErr := s.virtualStorage.VerifyUpload(ctx, userID, storageNamespace, body.Key, body.ContentType, intentMaxBytes)
		if verifyErr != nil {
			if errors.Is(verifyErr, virtualstorage.ErrNotFound) {
				writeJSON(w, http.StatusConflict, errorBody("MEDIA_UPLOAD_NOT_FOUND", "Upload is not present in object storage yet", id))
				return
			}
			if errors.Is(verifyErr, virtualstorage.ErrObjectMismatch) || errors.Is(verifyErr, virtualstorage.ErrTooLarge) || errors.Is(verifyErr, virtualstorage.ErrForbidden) {
				_, _ = s.db.Exec(ctx, `update testagram_media.upload_intents set status='cleanup_pending', expires_at=now(), updated_at=now()
					where object_key=$1 and owner_id=$2 and status in ('issued','uploaded','cleanup_pending')`, body.Key, userID)
				_, _ = s.db.Exec(ctx, `insert into testagram_media.storage_deletion_jobs
					(object_key, owner_id, visibility, storage_namespace, storage_provider, reason, status)
					values ($1,$2,$3,$4,$5,'expired_upload','pending') on conflict do nothing`,
					body.Key, userID, body.Visibility, storageNamespace, storageProvider)
				writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_OBJECT_INVALID", "Uploaded object failed storage policy verification; cleanup was queued", id))
				return
			}
			writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_VERIFY_FAILED", "Virtual storage could not verify the uploaded object; retry completion", id))
			return
		}
		if checksumErr := s.virtualStorage.VerifyUploadChecksum(ctx, userID, storageNamespace, body.Key, intentMaxBytes, body.ChecksumSHA256); checksumErr != nil {
			if errors.Is(checksumErr, virtualstorage.ErrChecksumMismatch) || errors.Is(checksumErr, virtualstorage.ErrTooLarge) {
				_, _ = s.db.Exec(ctx, `update testagram_media.upload_intents set status='cleanup_pending', expires_at=now(), updated_at=now()
					where object_key=$1 and owner_id=$2 and status in ('issued','uploaded','cleanup_pending')`, body.Key, userID)
				_, _ = s.db.Exec(ctx, `insert into testagram_media.storage_deletion_jobs
					(object_key, owner_id, visibility, storage_namespace, storage_provider, reason, status)
					values ($1,$2,$3,$4,$5,'expired_upload','pending') on conflict do nothing`,
					body.Key, userID, body.Visibility, storageNamespace, storageProvider)
				writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_CHECKSUM_MISMATCH", "Uploaded bytes do not match checksum_sha256; cleanup was queued", id))
				return
			}
			if errors.Is(checksumErr, virtualstorage.ErrNotFound) {
				writeJSON(w, http.StatusConflict, errorBody("MEDIA_UPLOAD_NOT_FOUND", "Upload is not present in object storage yet", id))
				return
			}
			if errors.Is(checksumErr, virtualstorage.ErrChecksumUnavailable) {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_CHECKSUM_UNAVAILABLE", "The configured storage provider cannot verify SHA-256", id))
				return
			}
			writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_CHECKSUM_FAILED", "Could not verify uploaded bytes; retry completion", id))
			return
		}
		actualType, size, etag = info.ContentType, info.Size, info.ETag
	} else {
		headURL, signErr := s.media.PresignObjectHead(userID, body.Key, body.Visibility, time.Minute, time.Now())
		if signErr != nil {
			writeJSON(w, http.StatusForbidden, errorBody("MEDIA_ACCESS_DENIED", "This media key is not owned by the authenticated account", id))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
		if reqErr != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("MEDIA_VERIFY_FAILED", "Could not verify uploaded media", id))
			return
		}
		client := s.upstream
		if client == nil {
			client = &http.Client{Timeout: 4 * time.Second}
		}
		resp, requestErr := client.Do(req)
		if requestErr != nil {
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
		actualType = strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
		size = resp.ContentLength
		etag = strings.Trim(resp.Header.Get("ETag"), "\"")
	}
	if actualType != body.ContentType || size < 1 || size > intentMaxBytes || size > maxBytes {
		_, _ = s.db.Exec(r.Context(), `update testagram_media.upload_intents
			set status='cleanup_pending', expires_at=now(), updated_at=now()
			where object_key=$1 and owner_id=$2 and status in ('issued','uploaded','cleanup_pending')`, body.Key, userID)
		if storageNamespace == "legacy-media" || (s.virtualStorage == nil && s.dataCentreMedia == nil) {
			if deleteErr := s.deleteMediaObject(r.Context(), userID, body.Key, body.Visibility); deleteErr == nil {
				_, _ = s.db.Exec(r.Context(), `update testagram_media.upload_intents
					set status='expired', updated_at=now() where object_key=$1 and owner_id=$2 and status='cleanup_pending'`, body.Key, userID)
			}
		}
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("MEDIA_OBJECT_INVALID", "Uploaded object type or size does not satisfy media policy; cleanup was scheduled", id))
		return
	}
	var asset mediaAsset
	checksumForMetadata := ""
	if storageNamespace != "legacy-media" {
		// The checksum has already been verified against the stored object above.
		// Persist only the digest, never a signed URL or provider credentials.
		checksumForMetadata = strings.ToLower(strings.TrimSpace(body.ChecksumSHA256))
	}
	assetInitialStatus := "ready"
	if storageNamespace != "legacy-media" && storageProvider != "data-centre" {
		// The database outbox starts replication, but the asset is not publishable
		// until the worker has read back and verified all three independent copies.
		assetInitialStatus = "replication_pending"
	}
	err = s.db.QueryRow(r.Context(), `insert into testagram_media.assets
		(object_key, owner_id, visibility, content_type, original_name, size_bytes, etag, status, storage_namespace, storage_provider, checksum_sha256)
		values ($1,$2,$3,$4,$5,$6,$7,$11,$8,$9,$10)
		on conflict (object_key) do nothing
		returning object_key, visibility, content_type, original_name, size_bytes, etag, status, created_at`,
		body.Key, userID, body.Visibility, actualType, intentName, size, etag, storageNamespace, storageProvider, checksumForMetadata, assetInitialStatus).
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
		if ownerID != userID || (asset.Status != "ready" && asset.Status != "replication_pending") ||
			asset.Visibility != body.Visibility || asset.ContentType != actualType || asset.SizeBytes != size {
			writeJSON(w, http.StatusConflict, errorBody("MEDIA_KEY_CONFLICT", "Media key already exists with different ownership or metadata", id))
			return
		}
	}
	asset.Key = body.Key
	if strings.HasPrefix(body.ContentType, "image/") {
		asset.MediaType = "image"
	} else if strings.HasPrefix(body.ContentType, "video/") {
		asset.MediaType = "video"
	}
	if body.Visibility == "public" {
		asset.PublicURL = publicMediaURLForStorage(body.Key, userID, storageNamespace, storageProvider)
		if asset.PublicURL == "" {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_PUBLIC_URL_INVALID", "Public media delivery URL is not configured", id))
			return
		}
	}
	if asset.Status == "replication_pending" {
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{
			"key": body.Key, "status": "replication_pending", "retry_after_ms": 1000,
		}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.upload_intents set status='completed', updated_at=now()
		where object_key=$1 and owner_id=$2 and status in ('issued','uploaded','completed')`, body.Key, userID); err != nil {
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
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 8 {
		return "", http.StatusUnauthorized, errors.New("missing bearer token")
	}
	return s.authenticatedAppwriteUser(r, parts[1])
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
	if (s.media == nil && s.dataCentreMedia == nil) || s.db == nil {
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
	var ownerID, visibility, currentStatus, storageNamespace, storageProvider string
	err = tx.QueryRow(r.Context(), `select owner_id, visibility, status, storage_namespace, storage_provider from testagram_media.assets where object_key=$1 for update`, body.Key).Scan(&ownerID, &visibility, &currentStatus, &storageNamespace, &storageProvider)
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
	if _, err := tx.Exec(r.Context(), `insert into testagram_media.storage_deletion_jobs
		(object_key, owner_id, visibility, storage_namespace, storage_provider, reason, status)
		values ($1,$2,$3,$4,$5,'moderation','pending') on conflict do nothing`,
		body.Key, ownerID, visibility, storageNamespace, storageProvider); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_DELETE_QUEUE_FAILED", "Could not durably schedule moderated media deletion", id))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not commit media quarantine", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.deleteMediaObjectWithNamespace(ctx, ownerID, body.Key, visibility, storageNamespace, storageProvider); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantine_pending"}, "error": nil, "request_id": id})
		return
	}
	if err := s.detachMediaFromPosts(ctx, body.Key); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantine_pending"}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.assets set status='quarantined', updated_at=now()
		where object_key=$1 and status='quarantine_pending'`, body.Key); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantine_pending"}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.storage_deletion_jobs set status='completed', completed_at=now(), locked_at=null, updated_at=now()
		where object_key=$1 and reason='moderation' and status in ('pending','processing','retry')`, body.Key); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantined"}, "error": nil, "request_id": id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "quarantined"}, "error": nil, "request_id": id})
}

type mediaAttachRequest struct {
	Key    string `json:"key"`
	PostID string `json:"post_id"`
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
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
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
	// Hold a short row lock while attaching the local object key to the post.
	// Delete/quarantine transitions cannot race through this transaction.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not begin media attachment", id))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var visibility, contentType, originalName, assetStatus, storageNamespace, storageProvider string
	var sizeBytes int64
	err = tx.QueryRow(ctx, `select visibility, content_type, original_name, size_bytes, status, storage_namespace, storage_provider
		from testagram_media.assets where object_key=$1 and owner_id=$2 for update`, body.Key, userID).
		Scan(&visibility, &contentType, &originalName, &sizeBytes, &assetStatus, &storageNamespace, &storageProvider)
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
	publicURL := publicMediaURLForStorage(body.Key, userID, storageNamespace, storageProvider)
	if publicURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_PUBLIC_URL_INVALID", "Public media URL is not configured", id))
		return
	}
	var postOwnerID string
	if err := tx.QueryRow(ctx, `SELECT author_id::text FROM testagram_local.posts
		WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, body.PostID).Scan(&postOwnerID); err != nil || postOwnerID != userID {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "Post was not found for this account", id))
		return
	}
	tag, err := tx.Exec(ctx, `UPDATE testagram_local.posts
		SET media_ids=CASE
			WHEN media_ids @> jsonb_build_array($2::text) THEN media_ids
			ELSE media_ids || jsonb_build_array($2::text)
		END, updated_at=now()
		WHERE id=$1 AND author_id=$3 AND deleted_at IS NULL`, body.PostID, body.Key, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_POST_LINK_FAILED", "Could not attach media to the post", id))
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "Post was not found for this account", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_METADATA_FAILED", "Could not commit media attachment", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
		"key": body.Key, "post_id": body.PostID, "public_url": publicURL,
		"size_bytes": sizeBytes, "content_type": contentType, "media_type": mediaType,
		"status": "uploaded",
	}, "error": nil, "request_id": id})
}

const maxProductPostMediaBytes int64 = 20 << 20

// productPostMediaType maps upload MIME types to values accepted by the
// native post-media validation. Unsupported document types never enter media_ids.
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

func publicMediaURLForStorage(key, ownerID, storageNamespace, storageProvider string) string {
	if storageNamespace == "legacy-media" || storageNamespace == "" || storageProvider == "data-centre" {
		return publicMediaURL(key)
	}
	// Legacy virtual-storage objects are physically stored below the tenant prefix.
	return publicMediaURL("tenants/" + ownerID + "/" + strings.TrimLeft(key, "/"))
}

func publicMediaURL(key string) string {
	base, err := url.Parse(strings.TrimRight(env("MEDIA_PUBLIC_BASE_URL", "https://media.testagram.site/v1"), "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.RawQuery != "" || base.Fragment != "" {
		return ""
	}
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.Contains(key, "..") || strings.ContainsAny(key, "\r\n\x00") {
		return ""
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + key
	base.RawPath = ""
	return base.String()
}

func (s *service) detachMediaFromPosts(ctx context.Context, objectKey string) error {
	_, err := s.db.Exec(ctx, `
UPDATE testagram_local.posts p
SET media_ids=COALESCE((
  SELECT jsonb_agg(item.value ORDER BY item.ordinality)
  FROM jsonb_array_elements(p.media_ids) WITH ORDINALITY AS item(value,ordinality)
  WHERE item.value #>> '{}' <> $1
),'[]'::jsonb), updated_at=now()
WHERE p.media_ids @> jsonb_build_array($1::text)`, objectKey)
	return err
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
	if (s.media == nil && s.dataCentreMedia == nil && s.virtualStorage == nil) || s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_STORAGE_NOT_CONFIGURED", "Media storage or metadata database is unavailable", id))
		return
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		writeAppwriteAuthFailure(w, status, err, id)
		return
	}
	var body mediaDeleteRequest
	if err := decodeMediaJSON(w, r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEDIA_REQUEST", "A valid media key is required", id))
		return
	}
	var visibility, currentStatus, storageNamespace, storageProvider string
	err = s.db.QueryRow(r.Context(), `select visibility, status, storage_namespace, storage_provider from testagram_media.assets where object_key=$1 and owner_id=$2`, body.Key, userID).Scan(&visibility, &currentStatus, &storageNamespace, &storageProvider)
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
	// Persist the cleanup job before touching object storage. If this process
	// exits after enqueueing, the background worker resumes the deletion.
	if _, err := s.db.Exec(r.Context(), `insert into testagram_media.storage_deletion_jobs
		(object_key, owner_id, visibility, storage_namespace, storage_provider, reason, status)
		values ($1,$2,$3,$4,$5,'user_delete','pending')
		on conflict do nothing`, body.Key, userID, visibility, storageNamespace, storageProvider); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("MEDIA_DELETE_QUEUE_FAILED", "Could not durably schedule media deletion", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.deleteMediaObjectWithNamespace(ctx, userID, body.Key, visibility, storageNamespace, storageProvider); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	if err := s.detachMediaFromPosts(ctx, body.Key); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.assets set status='deleted', updated_at=now()
		where object_key=$1 and owner_id=$2 and status='deletion_pending'`, body.Key, userID); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	if _, err := s.db.Exec(ctx, `update testagram_media.storage_deletion_jobs
		set status='completed', completed_at=now(), updated_at=now(), locked_at=null
		where object_key=$1 and reason='user_delete' and status in ('pending','processing','retry')`, body.Key); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deletion_pending"}, "error": nil, "request_id": id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"key": body.Key, "status": "deleted"}, "error": nil, "request_id": id})
}


func (s *service) deleteMediaObject(ctx context.Context, userID, key, visibility string) error {
	return s.deleteMediaObjectWithNamespace(ctx, userID, key, visibility, "legacy-media", "default")
}

func (s *service) deleteMediaObjectWithNamespace(ctx context.Context, userID, key, visibility, storageNamespace, storageProvider string) error {
	// The asset is already in deletion_pending/quarantine_pending when this
	// method is called. Purge first so a failed invalidation leaves the durable
	// storage_deletion_jobs row retryable and never deletes bytes behind a stale
	// shared-cache entry.
	if s.cdnPurger != nil {
		if err := s.cdnPurger.Purge(ctx, key); err != nil {
			return err
		}
	}
	if storageProvider == "data-centre" {
		if s.dataCentreMedia == nil { return errors.New("Data Centre media service is unavailable") }
		status, err := s.dataCentreMedia.Delete(ctx, userID, userID, storageNamespace, key)
		if err != nil { return err }
		if status != "deleted" { return errors.New("Data Centre media deletion remains pending") }
		return nil
	}
	if storageNamespace != "" && storageNamespace != "legacy-media" {
		if s.virtualStorage == nil {
			return errors.New("virtual storage provider is required for this namespace")
		}
		return s.virtualStorage.Delete(ctx, userID, userID, storageNamespace, key)
	}
	if s.media == nil {
		return errors.New("legacy media storage signer is unavailable")
	}
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
	key, ownerID, visibility, namespace, provider string
}

func (s *service) cleanupMediaBatch(ctx context.Context) error {
	if s.db == nil || (s.media == nil && s.dataCentreMedia == nil && s.virtualStorage == nil) {
		return nil
	}
	batchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	intentRows, err := s.db.Query(batchCtx, `select object_key, owner_id, visibility, storage_namespace, storage_provider
		from testagram_media.upload_intents
		where (expires_at <= now() and status in ('issued','uploaded','cleanup_pending')) or (status='uploading' and updated_at <= now() - interval '2 hours')
		order by expires_at asc limit 50`)
	if err != nil {
		return err
	}
	var intents []mediaCleanupObject
	for intentRows.Next() {
		var item mediaCleanupObject
		if err := intentRows.Scan(&item.key, &item.ownerID, &item.visibility, &item.namespace, &item.provider); err != nil {
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
			where object_key=$1 and status in ('issued','uploading','uploaded','cleanup_pending')`, item.key)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Completion/cancellation may have won the race after selection.
			continue
		}
		if err := s.deleteMediaObjectWithNamespace(batchCtx, item.ownerID, item.key, item.visibility, item.namespace, item.provider); err != nil {
			continue
		}
		if _, err := s.db.Exec(batchCtx, `update testagram_media.upload_intents set status='expired', updated_at=now()
			where object_key=$1 and status='cleanup_pending'`, item.key); err != nil {
			return err
		}
	}

	assetRows, err := s.db.Query(batchCtx, `select object_key, owner_id, visibility, status, storage_namespace, storage_provider
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
		if err := assetRows.Scan(&item.key, &item.ownerID, &item.visibility, &item.status, &item.namespace, &item.provider); err != nil {
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
		if err := s.deleteMediaObjectWithNamespace(batchCtx, item.ownerID, item.key, item.visibility, item.namespace, item.provider); err != nil {
			continue
		}
		if err := s.detachMediaFromPosts(batchCtx, item.key); err != nil {
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
