// Package media creates short-lived S3-compatible upload and download URLs for MinIO.
// It intentionally uses only the standard library so the backend does not need to
// proxy large media bodies or add an SDK dependency to the request path.
package media

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRegion = "us-east-1"
	defaultUploadTTL = 10 * time.Minute
)

var (
	userIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	objectKeyPattern = regexp.MustCompile(`^users/[A-Za-z0-9-]{1,128}/[a-f0-9]{32}$`)
	allowedTypes = map[string]string{
		"image/jpeg": "image", "image/png": "image", "image/webp": "image", "image/gif": "image", "image/avif": "image",
		"video/mp4": "video", "video/webm": "video",
	}
)

type Config struct {
	Endpoint string
	SigningHost string
	AccessKey string
	SecretKey string
	Region string
	PrivateBucket string
	PublicBucket string
	MaxImageBytes int64
	MaxVideoBytes int64
	UploadTTL time.Duration
}

type Signer struct {
	endpoint *url.URL
	signingHost string
	accessKey string
	secretKey string
	region string
	privateBucket string
	publicBucket string
	maxImageBytes int64
	maxVideoBytes int64
	uploadTTL time.Duration
}

type UploadIntent struct {
	Bucket string `json:"bucket"`
	Key string `json:"key"`
	Method string `json:"method"`
	URL string `json:"url"`
	Fields map[string]string `json:"fields"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	PublicURL string `json:"public_url,omitempty"`
}

func NewSigner(cfg Config) (*Signer, error) {
	endpoint, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil || strings.Trim(endpoint.Path, "/") != "" {
		return nil, errors.New("media endpoint must be an absolute HTTP(S) URL without query or fragment")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("media signing credentials are required")
	}
	if strings.TrimSpace(cfg.SigningHost) == "" { cfg.SigningHost = endpoint.Host }
	if strings.ContainsAny(cfg.SigningHost, "/?#\r\n@ ") {
		return nil, errors.New("media signing host is invalid")
	}
	if cfg.Region == "" { cfg.Region = defaultRegion }
	if cfg.PrivateBucket == "" { cfg.PrivateBucket = "testagram-private-media" }
	if cfg.PublicBucket == "" { cfg.PublicBucket = "testagram-public-media" }
	if !validBucket(cfg.PrivateBucket) || !validBucket(cfg.PublicBucket) || cfg.PrivateBucket == cfg.PublicBucket {
		return nil, errors.New("private and public media buckets must be valid and distinct")
	}
	if cfg.MaxImageBytes == 0 { cfg.MaxImageBytes = 25 << 20 }
	if cfg.MaxVideoBytes == 0 { cfg.MaxVideoBytes = 500 << 20 }
	if cfg.MaxImageBytes < 1 || cfg.MaxVideoBytes < 1 || cfg.MaxImageBytes > 5<<30 || cfg.MaxVideoBytes > 5<<30 {
		return nil, errors.New("media size limits must be between 1 byte and 5 GiB")
	}
	if cfg.UploadTTL == 0 { cfg.UploadTTL = defaultUploadTTL }
	if cfg.UploadTTL < time.Minute || cfg.UploadTTL > time.Hour {
		return nil, errors.New("media upload URL lifetime must be between 1 minute and 1 hour")
	}
	return &Signer{
		endpoint: endpoint, signingHost: cfg.SigningHost, accessKey: cfg.AccessKey, secretKey: cfg.SecretKey,
		region: cfg.Region, privateBucket: cfg.PrivateBucket, publicBucket: cfg.PublicBucket,
		maxImageBytes: cfg.MaxImageBytes, maxVideoBytes: cfg.MaxVideoBytes, uploadTTL: cfg.UploadTTL,
	}, nil
}

// CreateUploadIntent returns a short-lived signed PUT URL compatible with the
// existing browser uploader. Object size is verified against the persisted intent
// on completion; invalid objects are deleted or queued for durable cleanup.
func (s *Signer) CreateUploadIntent(userID, contentType, visibility string, requestedBytes int64, now time.Time) (UploadIntent, error) {
	if !userIDPattern.MatchString(userID) { return UploadIntent{}, errors.New("invalid authenticated user ID") }
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	kind, ok := allowedTypes[contentType]
	if !ok { return UploadIntent{}, errors.New("unsupported media content type") }
	limit := s.maxImageBytes
	if kind == "video" { limit = s.maxVideoBytes }
	if requestedBytes < 1 || requestedBytes > limit {
		return UploadIntent{}, fmt.Errorf("upload size must be between 1 and %d bytes for %s", limit, kind)
	}
	if visibility != "private" && visibility != "public" {
		return UploadIntent{}, errors.New("visibility must be private or public")
	}
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil { return UploadIntent{}, errors.New("could not allocate media object key") }
	key := "users/" + userID + "/" + hex.EncodeToString(randomID)
	bucket := s.privateBucket
	if visibility == "public" { bucket = s.publicBucket }
	now = now.UTC()
	expiresAt := now.Add(s.uploadTTL)
	uploadURL, err := s.presignObjectPut(userID, key, visibility, contentType, s.uploadTTL, now)
	if err != nil { return UploadIntent{}, err }
	result := UploadIntent{Bucket: bucket, Key: key, Method: "PUT", URL: uploadURL, Fields: map[string]string{}, ExpiresAt: expiresAt, MaxUploadBytes: requestedBytes}
	return result, nil
}

func (s *Signer) presignObjectPut(userID, key, visibility, contentType string, lifetime time.Duration, now time.Time) (string, error) {
	if !userIDPattern.MatchString(userID) || !objectKeyPattern.MatchString(key) || !strings.HasPrefix(key, "users/"+userID+"/") {
		return "", errors.New("media object does not belong to authenticated user")
	}
	if visibility != "private" && visibility != "public" { return "", errors.New("invalid media visibility") }
	if lifetime < time.Minute || lifetime > time.Hour { return "", errors.New("upload URL lifetime must be between 1 minute and 1 hour") }
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if _, ok := allowedTypes[contentType]; !ok { return "", errors.New("unsupported media content type") }
	bucket := s.privateBucket
	if visibility == "public" { bucket = s.publicBucket }
	now = now.UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + s.region + "/s3/aws4_request"
	u := s.objectURL(bucket, key)
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(lifetime/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "content-type;host")
	u.RawQuery = q.Encode()
	canonicalHeaders := "content-type:" + contentType + "\n" + "host:" + s.signingHost
	canonicalRequest := strings.Join([]string{"PUT", u.EscapedPath(), u.RawQuery, canonicalHeaders, "content-type;host", "UNSIGNED-PAYLOAD"}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.secretKey, date, s.region), []byte(stringToSign)))
	q = u.Query()
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// MaxBytesForContentType returns the configured maximum for an accepted MIME type.
func (s *Signer) MaxBytesForContentType(contentType string) (int64, bool) {
	kind, ok := allowedTypes[strings.ToLower(strings.TrimSpace(contentType))]
	if !ok { return 0, false }
	if kind == "video" { return s.maxVideoBytes, true }
	return s.maxImageBytes, true
}

// PresignObjectHead creates a short-lived HEAD URL used to verify an uploaded object.
func (s *Signer) PresignObjectHead(userID, key, visibility string, lifetime time.Duration, now time.Time) (string, error) {
	if !userIDPattern.MatchString(userID) || !objectKeyPattern.MatchString(key) || !strings.HasPrefix(key, "users/"+userID+"/") {
		return "", errors.New("media object does not belong to authenticated user")
	}
	if visibility != "private" && visibility != "public" { return "", errors.New("invalid media visibility") }
	if lifetime < time.Minute || lifetime > time.Hour { return "", errors.New("verification URL lifetime must be between 1 minute and 1 hour") }
	bucket := s.privateBucket
	if visibility == "public" { bucket = s.publicBucket }
	now = now.UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + s.region + "/s3/aws4_request"
	u := s.objectURL(bucket, key)
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(lifetime/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()
	canonicalRequest := strings.Join([]string{"HEAD", u.EscapedPath(), u.RawQuery, "host:"+s.signingHost, "host", "UNSIGNED-PAYLOAD"}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.secretKey, date, s.region), []byte(stringToSign)))
	q = u.Query()
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// PresignPrivateDownload only signs keys under the authenticated user's prefix.
// A guessed key belonging to another account is rejected before a URL is issued.
func (s *Signer) PresignPrivateDownload(userID, key string, lifetime time.Duration, now time.Time) (string, error) {
	if !userIDPattern.MatchString(userID) || !objectKeyPattern.MatchString(key) || !strings.HasPrefix(key, "users/"+userID+"/") {
		return "", errors.New("media object does not belong to authenticated user")
	}
	if lifetime < time.Minute || lifetime > time.Hour { return "", errors.New("download URL lifetime must be between 1 minute and 1 hour") }
	now = now.UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + s.region + "/s3/aws4_request"
	u := s.objectURL(s.privateBucket, key)
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(lifetime/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()
	canonicalRequest := strings.Join([]string{"GET", u.EscapedPath(), u.RawQuery, "host:"+s.signingHost, "host", "UNSIGNED-PAYLOAD"}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.secretKey, date, s.region), []byte(stringToSign)))
	q = u.Query()
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Signer) objectURL(bucket, key string) *url.URL {
	u := *s.endpoint
	joined := strings.TrimRight(u.Path, "/") + "/" + bucket
	if key != "" { joined += "/" + path.Clean("/"+key)[1:] }
	u.Path = joined
	u.RawPath = ""
	return &u
}

func validBucket(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || strings.HasPrefix(bucket, ".") || strings.HasSuffix(bucket, ".") || strings.HasPrefix(bucket, "-") || strings.HasSuffix(bucket, "-") || strings.Contains(bucket, "..") || strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") { return false }
	for _, c := range bucket {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') { return false }
	}
	return true
}

func signingKey(secret, date, region string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	regionKey := hmacSHA256(dateKey, []byte(region))
	serviceKey := hmacSHA256(regionKey, []byte("s3"))
	return hmacSHA256(serviceKey, []byte("aws4_request"))
}

func hmacSHA256(key, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}


// PresignObjectDelete creates a short-lived DELETE URL for a caller-owned object.
// The URL is only a capability: never return the signing credentials to clients.
func (s *Signer) PresignObjectDelete(userID, key, visibility string, lifetime time.Duration, now time.Time) (string, error) {
	if !userIDPattern.MatchString(userID) || !objectKeyPattern.MatchString(key) || !strings.HasPrefix(key, "users/"+userID+"/") {
		return "", errors.New("media object does not belong to authenticated user")
	}
	if visibility != "private" && visibility != "public" {
		return "", errors.New("invalid media visibility")
	}
	if lifetime < time.Minute || lifetime > time.Hour {
		return "", errors.New("delete URL lifetime must be between 1 minute and 1 hour")
	}
	bucket := s.privateBucket
	if visibility == "public" {
		bucket = s.publicBucket
	}
	now = now.UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + s.region + "/s3/aws4_request"
	u := s.objectURL(bucket, key)
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(lifetime/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()
	canonicalRequest := strings.Join([]string{"DELETE", u.EscapedPath(), u.RawQuery, "host:"+s.signingHost, "host", "UNSIGNED-PAYLOAD"}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.secretKey, date, s.region), []byte(stringToSign)))
	q = u.Query()
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
