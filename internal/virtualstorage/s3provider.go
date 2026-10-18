package virtualstorage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3Config contains server-side credentials only. Endpoint must be an origin,
// not a bucket URL or URL with a path/query. SigningHost is normally empty;
// set it to the Host header forwarded by a trusted reverse proxy when that differs
// from the public URL host.
type S3Config struct {
	Endpoint string
	SigningHost string
	AccessKey string
	SecretKey string
	Region string
	Client *http.Client
}

// S3Provider implements Provider with S3-compatible SigV4 presigned requests.
// Large uploads/downloads go directly between clients and object storage.
type S3Provider struct {
	endpoint *url.URL
	signingHost, accessKey, secretKey, region string
	client *http.Client
}

func NewS3Provider(cfg S3Config) (*S3Provider, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("S3 endpoint must be an absolute HTTP(S) origin without credentials, path, query, or fragment")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("S3 access and secret keys are required")
	}
	if cfg.Region == "" { cfg.Region = "us-east-1" }
	if cfg.SigningHost == "" { cfg.SigningHost = u.Host }
	if strings.ContainsAny(cfg.SigningHost, "/?#\r\n@ ") {
		return nil, errors.New("S3 signing host is invalid")
	}
		client := cfg.Client
	if client == nil { client = &http.Client{Timeout: 8 * time.Second} }
	return &S3Provider{endpoint:u, signingHost:cfg.SigningHost, accessKey:cfg.AccessKey, secretKey:cfg.SecretKey, region:cfg.Region, client:client}, nil
}

// do disables redirects so signed query credentials are never forwarded to a
// redirect target. A redirect is returned to the caller as a non-success status.
func (p *S3Provider) do(req *http.Request) (*http.Response, error) {
	client := *p.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client.Do(req)
}

func (p *S3Provider) PresignPut(ctx context.Context, bucket, key, contentType string, maxBytes int64, ttl time.Duration) (string, error) {
	if maxBytes < 1 { return "", errors.New("maximum upload size must be positive") }
	if strings.TrimSpace(contentType) == "" { return "", errors.New("content type is required") }
	return p.presign(ctx, http.MethodPut, bucket, key, url.Values{}, map[string]string{"content-type":strings.ToLower(strings.TrimSpace(contentType))}, ttl)
}
func (p *S3Provider) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	return p.presign(ctx, http.MethodGet, bucket, key, url.Values{}, nil, ttl)
}
func (p *S3Provider) Head(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	u, err := p.presign(ctx, http.MethodHead, bucket, key, url.Values{}, nil, time.Minute)
	if err != nil { return ObjectInfo{}, err }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil { return ObjectInfo{}, err }
	resp, err := p.do(req)
	if err != nil { return ObjectInfo{}, err }
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound { return ObjectInfo{}, ErrNotFound }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return ObjectInfo{}, fmt.Errorf("S3 HEAD failed with status %d", resp.StatusCode) }
	return ObjectInfo{Key:key, Size:resp.ContentLength, ContentType:strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]), ETag:strings.Trim(resp.Header.Get("ETag"), "\""), ModifiedAt:parseHTTPTime(resp.Header.Get("Last-Modified"))}, nil
}
func (p *S3Provider) Delete(ctx context.Context, bucket, key string) error {
	u, err := p.presign(ctx, http.MethodDelete, bucket, key, url.Values{}, nil, time.Minute)
	if err != nil { return err }
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil { return err }
	resp, err := p.do(req)
	if err != nil { return err }
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusNotFound { return nil }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return fmt.Errorf("S3 DELETE failed with status %d", resp.StatusCode) }
	return nil
}
func (p *S3Provider) List(ctx context.Context, bucket, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if limit < 1 || limit > 1000 { return nil, "", errors.New("S3 list limit must be between 1 and 1000") }
	q := url.Values{"list-type":{"2"}, "prefix":{prefix}, "max-keys":{strconv.Itoa(limit)}}
	if cursor != "" { q.Set("continuation-token", cursor) }
	u, err := p.presign(ctx, http.MethodGet, bucket, "", q, nil, time.Minute)
	if err != nil { return nil, "", err }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil { return nil, "", err }
	resp, err := p.do(req)
	if err != nil { return nil, "", err }
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return nil, "", fmt.Errorf("S3 LIST failed with status %d", resp.StatusCode) }
	var result struct {
		Contents []struct {
			Key string `xml:"Key"`
			Size int64 `xml:"Size"`
			ETag string `xml:"ETag"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		IsTruncated bool `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil { return nil, "", errors.New("invalid S3 LIST response") }
	items := make([]ObjectInfo, 0, len(result.Contents))
	for _, item := range result.Contents {
		items = append(items, ObjectInfo{Key:item.Key, Size:item.Size, ETag:strings.Trim(item.ETag, "\""), ModifiedAt:parseHTTPTime(item.LastModified)})
	}
	next := ""
	if result.IsTruncated { next = result.NextContinuationToken }
	return items, next, nil
}

func (p *S3Provider) presign(ctx context.Context, method, bucket, key string, extra url.Values, headers map[string]string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil { return "", err }
	if !validBucketName(bucket) || ttl < time.Minute || ttl > time.Hour { return "", errors.New("invalid S3 bucket or presign lifetime") }
	if key != "" && (strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\x00\r\n\\") || path.Clean(key) != key || path.Clean(key) == "." || strings.Contains(key, "../")) {
		return "", ErrInvalidKey
	}
	now := time.Now().UTC()
	date, amzDate := now.Format("20060102"), now.Format("20060102T150405Z")
	scope := date + "/" + p.region + "/s3/aws4_request"
	u := *p.endpoint
	objectPath := "/" + bucket
	if key != "" { objectPath += "/" + key }
	u.Path = objectPath
	u.RawPath = escapeS3Path(objectPath)
	q := u.Query()
	for k, values := range extra { for _, value := range values { q.Add(k, value) } }
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", p.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	headerNames := make([]string, 0, len(headers)+1)
	canonicalHeaders := ""
	for name, value := range headers { name = strings.ToLower(name); headers[name] = strings.TrimSpace(value); headerNames = append(headerNames, name) }
	headerNames = append(headerNames, "host")
	sort.Strings(headerNames)
	for _, name := range headerNames {
		value := p.signingHost
		if name != "host" { value = headers[name] }
		canonicalHeaders += name + ":" + value + "\n"
	}
	signedHeaders := strings.Join(headerNames, ";")
	q.Set("X-Amz-SignedHeaders", signedHeaders)
	u.RawQuery = q.Encode()
	canonicalQuery := awsCanonicalQuery(q)
	canonicalRequest := strings.Join([]string{method, u.EscapedPath(), canonicalQuery, canonicalHeaders, signedHeaders, "UNSIGNED-PAYLOAD"}, "\n")
	hash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	signature := hex.EncodeToString(s3HMAC(s3SigningKey(p.secretKey, date, p.region), stringToSign))
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func awsCanonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q { keys = append(keys, k) }
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values { parts = append(parts, awsEscape(k)+"="+awsEscape(v)) }
	}
	return strings.Join(parts, "&")
}
func awsEscape(s string) string { return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%7E", "~") }
func escapeS3Path(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts { parts[i] = url.PathEscape(parts[i]) }
	return strings.Join(parts, "/")
}
func s3SigningKey(secret, date, region string) []byte {
	dateKey := s3HMAC([]byte("AWS4"+secret), date)
	regionKey := s3HMAC(dateKey, region)
	serviceKey := s3HMAC(regionKey, "s3")
	return s3HMAC(serviceKey, "aws4_request")
}
func s3HMAC(key []byte, value string) []byte { h := hmac.New(sha256.New, key); _, _ = h.Write([]byte(value)); return h.Sum(nil) }
func parseHTTPTime(value string) time.Time {
	if t, err := http.ParseTime(value); err == nil { return t }
	if t, err := time.Parse(time.RFC3339, value); err == nil { return t }
	return time.Time{}
}
func validBucketName(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || strings.HasPrefix(bucket, ".") || strings.HasSuffix(bucket, ".") ||
		strings.HasPrefix(bucket, "-") || strings.HasSuffix(bucket, "-") || strings.Contains(bucket, "..") ||
		strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") || strings.ContainsAny(bucket, "/\\?#\r\n") {
		return false
	}
	for _, c := range bucket {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') { return false }
	}
	return true
}
