package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const maxFreshMediaBridgeBody = 16 << 10

var freshMediaObjectKeyPattern = regexp.MustCompile(`^users/([A-Za-z0-9._-]{1,128})/([a-f0-9]{32})$`)
var freshMediaOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var freshMediaSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const maxFreshMediaBridgeBody = 16 << 10

var freshMediaObjectKeyPattern = regexp.MustCompile(`^users/([A-Za-z0-9._-]{1,128})/([a-f0-9]{32})$`)
var freshMediaOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)
var freshMediaBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const maxFreshMediaBridgeBody = 16 << 10

var freshMediaObjectKeyPattern = regexp.MustCompile(`^users/([A-Za-z0-9._-]{1,128})/([a-f0-9]{32})$`)
var freshMediaOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

type freshMediaBridgeRequest struct {
	OwnerID     string `json:"owner_id"`
	Key         string `json:"key"`
	R2Bucket    string `json:"r2_bucket"`
	B2Bucket    string `json:"b2_bucket"`
	Visibility  string `json:"visibility"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

func (s *service) authorizeFreshMediaBridge(r *http.Request) bool {
	provided := strings.TrimSpace(r.Header.Get("X-Testagram-Fresh-Media-Token"))
	return len(s.freshMediaInternalToken) >= 32 && provided != "" &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(s.freshMediaInternalToken)) == 1
}

func decodeFreshMediaBridgeRequest(w http.ResponseWriter, r *http.Request, out *freshMediaBridgeRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFreshMediaBridgeBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	out.OwnerID = strings.TrimSpace(out.OwnerID)
	out.Key = strings.TrimSpace(out.Key)
	out.R2Bucket = strings.TrimSpace(out.R2Bucket)
	out.B2Bucket = strings.TrimSpace(out.B2Bucket)
	out.Visibility = strings.TrimSpace(out.Visibility)
	out.ContentType = strings.ToLower(strings.TrimSpace(out.ContentType))
	out.SHA256 = strings.ToLower(strings.TrimSpace(out.SHA256))
	return nil
}

func validFreshMediaBridgeRequest(in freshMediaBridgeRequest) bool {
	match := freshMediaObjectKeyPattern.FindStringSubmatch(in.Key)
	if !freshMediaOwnerPattern.MatchString(in.ViewerID) || !freshMediaOwnerPattern.MatchString(in.OwnerID) || len(match) != 3 || match[1] != in.OwnerID {
		return false
	}
	if in.Visibility != "private" || in.SizeBytes < 1 || in.SizeBytes > 500<<20 || !freshMediaSHA256Pattern.MatchString(in.SHA256) || !freshMediaBucketPattern.MatchString(in.R2Bucket) || !freshMediaBucketPattern.MatchString(in.B2Bucket) {
		return false
	}
	switch in.ContentType {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/avif",
		"video/mp4", "video/webm", "audio/mpeg", "audio/mp4", "audio/ogg", "audio/webm":
		return true
	default:
		return false
	}
}

// registerFreshMediaAsset mirrors an already verified Appwrite/B2/R2 media
// object into the Backend's canonical authorization ledger. The Fresh API has
// already HEAD-checked and SHA-256-verified both cloud copies before calling it.
func (s *service) registerFreshMediaAsset(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeFreshMediaBridge(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || s.db == nil {
		http.Error(w, "media ledger unavailable", http.StatusServiceUnavailable)
		return
	}
	var in freshMediaBridgeRequest
	if err := decodeFreshMediaBridgeRequest(w, r, &in); err != nil || !validFreshMediaBridgeRequest(in) {
		http.Error(w, "invalid media registration", http.StatusBadRequest)
		return
	}
	_, err := s.db.Exec(r.Context(), `
		INSERT INTO testagram_media.assets
		  (object_key, owner_id, visibility, content_type, size_bytes, etag,
		   status, storage_namespace, storage_provider, checksum_sha256)
		VALUES ($1,$2,'private',$3,$4,'','ready','legacy-media','fresh-b2-r2',$5)
		ON CONFLICT (object_key) DO NOTHING
	`, in.Key, in.OwnerID, in.ContentType, in.SizeBytes, in.SHA256)
	if err != nil {
		http.Error(w, "media ledger write failed", http.StatusServiceUnavailable)
		return
	}
	var ownerID, visibility, contentType, status, namespace, provider, checksum string
	var size int64
	err = s.db.QueryRow(r.Context(), `
		SELECT owner_id, visibility, content_type, status, storage_namespace,
		       storage_provider, checksum_sha256, size_bytes
		FROM testagram_media.assets WHERE object_key=$1
	`, in.Key).Scan(&ownerID, &visibility, &contentType, &status, &namespace, &provider, &checksum, &size)
	if err != nil {
		http.Error(w, "media ledger read failed", http.StatusServiceUnavailable)
		return
	}
	if ownerID != in.OwnerID || visibility != "private" || contentType != in.ContentType ||
		status != "ready" || namespace != "legacy-media" || provider != "fresh-b2-r2" ||
		checksum != in.SHA256 || size != in.SizeBytes {
		http.Error(w, "media ledger conflict", http.StatusConflict)
		return
	}
	for _, placement := range []struct{ provider, bucket string }{{"cloudflare-r2", in.R2Bucket}, {"backblaze-b2", in.B2Bucket}} {
		_, err = s.db.Exec(r.Context(), `
			INSERT INTO testagram_media.storage_replicas
			  (object_key,provider,bucket,object_size_bytes,checksum_sha256,state,verified_at,last_error_code,attempts,updated_at)
			VALUES ($1,$2,$3,$4,$5,'verified',now(),'',$6,now())
			ON CONFLICT (object_key,provider) DO UPDATE SET
			  bucket=EXCLUDED.bucket,object_size_bytes=EXCLUDED.object_size_bytes,
			  checksum_sha256=EXCLUDED.checksum_sha256,state='verified',verified_at=now(),
			  last_error_code='',attempts=testagram_media.storage_replicas.attempts+1,updated_at=now()
		`, in.Key, placement.provider, placement.bucket, in.SizeBytes, in.SHA256, 1)
		if err != nil { http.Error(w, "replica ledger write failed", http.StatusServiceUnavailable); return }
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": in.Key, "status": "ready"})
}

func (s *service) revokeFreshMediaAsset(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeFreshMediaBridge(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || s.db == nil {
		http.Error(w, "media ledger unavailable", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		ViewerID string `json:"viewer_id"`
		OwnerID  string `json:"owner_id"`
		Key      string `json:"key"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFreshMediaBridgeBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid media revocation", http.StatusBadRequest)
		return
	}
	in.ViewerID = strings.TrimSpace(in.ViewerID)
	in.OwnerID = strings.TrimSpace(in.OwnerID)
	in.Key = strings.TrimSpace(in.Key)
	match := freshMediaObjectKeyPattern.FindStringSubmatch(in.Key)
	if !freshMediaOwnerPattern.MatchString(in.OwnerID) || len(match) != 3 || match[1] != in.OwnerID {
		http.Error(w, "invalid media revocation", http.StatusBadRequest)
		return
	}
	_, err := s.db.Exec(r.Context(), `
		UPDATE testagram_media.assets
		SET status='deleted', updated_at=now()
		WHERE object_key=$1 AND owner_id=$2 AND storage_provider='fresh-b2-r2'
	`, in.Key, in.OwnerID)
	if err != nil {
		http.Error(w, "media ledger update failed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": in.Key, "status": "deleted"})
}

func (s *service) freshMediaCDNDownloadURL(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeFreshMediaBridge(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || s.db == nil || s.cdnMediaAssertions == nil || s.cdnMediaBaseURL == "" {
		http.Error(w, "CDN media authorization unavailable", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		ViewerID string `json:"viewer_id"`
		OwnerID  string `json:"owner_id"`
		Key      string `json:"key"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFreshMediaBridgeBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid media authorization request", http.StatusBadRequest)
		return
	}
	in.ViewerID = strings.TrimSpace(in.ViewerID)
	in.OwnerID = strings.TrimSpace(in.OwnerID)
	in.Key = strings.TrimSpace(in.Key)
	match := freshMediaObjectKeyPattern.FindStringSubmatch(in.Key)
	if !freshMediaOwnerPattern.MatchString(in.ViewerID) || !freshMediaOwnerPattern.MatchString(in.OwnerID) || len(match) != 3 || match[1] != in.OwnerID {
		http.Error(w, "invalid media authorization request", http.StatusBadRequest)
		return
	}
	var visibility, status, provider string
	err := s.db.QueryRow(r.Context(), `
		SELECT visibility, status, storage_provider
		FROM testagram_media.assets WHERE object_key=$1 AND owner_id=$2
	`, in.Key, in.OwnerID).Scan(&visibility, &status, &provider)
	if err != nil || visibility != "private" || status != "ready" || provider != "fresh-b2-r2" {
		http.Error(w, "media authorization denied", http.StatusNotFound)
		return
	}
	assertion, expiresAt, err := s.cdnMediaAssertions.IssueForViewer(in.Key, in.OwnerID, in.ViewerID, time.Now())
	if err != nil {
		http.Error(w, "media authorization unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url": s.cdnMediaBaseURL + "/v1/" + in.Key,
		"authorization": "Bearer " + assertion,
		"expires_at": expiresAt,
		"expires_in_seconds": int64(time.Until(expiresAt).Seconds()),
		"visibility": "private",
	})
}
