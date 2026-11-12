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
	Endpoint    string
	SigningHost string
	AccessKey   string
	SecretKey   string
	Region      string
	Client      *http.Client
}

// S3Provider implements Provider with S3-compatible SigV4 presigned requests.
// Large uploads/downloads go directly between clients and object storage.
type S3Provider struct {
	endpoint                                  *url.URL
	endpointPathPrefix                        string
	signingHost, accessKey, secretKey, region string
	client                                    *http.Client
}

func (p *S3Provider) EndpointHost() string {
	if p == nil || p.endpoint == nil {
		return ""
	}
	return strings.ToLower(p.endpoint.Host)
}

func isLocalS3Host(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func NewS3Provider(cfg S3Config) (*S3Provider, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLocalS3Host(u.Hostname()))) ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (strings.TrimRight(u.Path, "/") != "" && strings.TrimRight(u.Path, "/") != "/v1/s3") {
		return nil, errors.New("S3 endpoint must be an HTTPS origin or the Appwrite /v1/s3 endpoint, without credentials, query, or fragment")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("S3 access and secret keys are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if len(cfg.Region) > 64 || strings.TrimSpace(cfg.Region) != cfg.Region || strings.ContainsAny(cfg.Region, "\r\n/\\ ") {
		return nil, errors.New("S3 signing region is invalid")
	}
	if cfg.SigningHost == "" {
		cfg.SigningHost = u.Host
	}
	if strings.ContainsAny(cfg.SigningHost, "/?#\r\n@ ") {
		return nil, errors.New("S3 signing host is invalid")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	prefix := strings.TrimRight(u.Path, "/")
	u.Path = ""
	u.RawPath = ""
	return &S3Provider{endpoint: u, endpointPathPrefix: prefix, signingHost: cfg.SigningHost, accessKey: cfg.AccessKey, secretKey: cfg.SecretKey, region: cfg.Region, client: client}, nil
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

func (p *S3Provider) doLong(req *http.Request) (*http.Response, error) {
	client := *p.client
	client.Timeout = 60 * time.Minute
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

func (p *S3Provider) PresignPut(ctx context.Context, bucket, key, contentType string, maxBytes int64, ttl time.Duration) (string, error) {
	if maxBytes < 1 {
		return "", errors.New("maximum upload size must be positive")
	}
	if strings.TrimSpace(contentType) == "" {
		return "", errors.New("content type is required")
	}
	return p.presign(ctx, http.MethodPut, bucket, key, url.Values{}, map[string]string{"content-type": strings.ToLower(strings.TrimSpace(contentType))}, ttl)
}

// SHA256 streams the stored object through a bounded reader. It deliberately
// does not use ETag: multipart uploads and provider implementations do not
// guarantee that an ETag is a content checksum.
func (p *S3Provider) SHA256(ctx context.Context, bucket, key string, maxBytes int64) (string, error) {
	if maxBytes < 1 {
		return "", errors.New("maximum checksum size must be positive")
	}
	u, err := p.presign(ctx, http.MethodGet, bucket, key, url.Values{}, nil, time.Hour)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.doLong(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("S3 GET for checksum failed with status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return "", ErrTooLarge
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxBytes {
		return "", ErrTooLarge
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return "", errors.New("S3 object body length differs from Content-Length")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *S3Provider) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	return p.presign(ctx, http.MethodGet, bucket, key, url.Values{}, nil, ttl)
}

// CopyFrom streams a bounded object between two S3-compatible providers without
// buffering the media in memory. It hashes the source stream and independently
// reads back and hashes the destination; ETags are never treated as checksums.
// CopyFrom streams a bounded object between two S3-compatible providers without
// buffering the media in memory. It hashes the source stream and independently
// reads back and hashes the destination; ETags are never treated as checksums.
// CopyFromProvider adapts the generic replica contract to this S3 provider.
// Only another S3Provider is accepted so the source request can preserve its
// own signing credentials and redirect policy.
func (p *S3Provider) CopyFromProvider(ctx context.Context, source Provider, sourceBucket, sourceKey, destinationBucket, destinationKey string, expectedBytes int64, contentType, expectedSHA256 string) error {
	sourceS3, ok := source.(*S3Provider)
	if !ok {
		return errors.New("S3 replica copy requires an S3-compatible source provider")
	}
	return p.CopyFrom(ctx, sourceS3, sourceBucket, sourceKey, destinationBucket, destinationKey, expectedBytes, contentType, expectedSHA256)
}

func (p *S3Provider) CopyFrom(ctx context.Context, source *S3Provider, sourceBucket, sourceKey, destinationBucket, destinationKey string, expectedBytes int64, contentType, expectedSHA256 string) error {
	if source == nil || expectedBytes < 1 || strings.TrimSpace(contentType) == "" || len(expectedSHA256) != 64 {
		return errors.New("invalid object replication request")
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return errors.New("invalid expected SHA-256")
	}
	info, err := source.Head(ctx, sourceBucket, sourceKey)
	if err != nil {
		return fmt.Errorf("read source object metadata: %w", err)
	}
	if info.Size != expectedBytes || strings.EqualFold(info.ContentType, contentType) == false {
		return ErrObjectMismatch
	}
	getURL, err := source.PresignGet(ctx, sourceBucket, sourceKey, time.Hour)
	if err != nil {
		return err
	}
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	getClient := *source.client
	getClient.Timeout = 60 * time.Minute
	getClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	getResp, err := getClient.Do(getReq)
	if err != nil {
		return fmt.Errorf("download source replica: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode < 200 || getResp.StatusCode >= 300 {
		return fmt.Errorf("source GET failed with status %d", getResp.StatusCode)
	}
	if getResp.ContentLength >= 0 && getResp.ContentLength != expectedBytes {
		return ErrObjectMismatch
	}
	putURL, err := p.PresignPut(ctx, destinationBucket, destinationKey, contentType, expectedBytes, time.Hour)
	if err != nil {
		return err
	}
	h := sha256.New()
	limited := io.LimitReader(getResp.Body, expectedBytes+1)
	tee := io.TeeReader(limited, h)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, tee)
	if err != nil {
		return err
	}
	putReq.ContentLength = expectedBytes
	putReq.Header.Set("Content-Type", contentType)
	putClient := *p.client
	putClient.Timeout = 60 * time.Minute
	putClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	putResp, err := putClient.Do(putReq)
	if err != nil {
		return fmt.Errorf("upload destination replica: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(putResp.Body, 64<<10))
	putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return fmt.Errorf("destination PUT failed with status %d", putResp.StatusCode)
	}
	if getResp.ContentLength >= 0 && getResp.ContentLength != expectedBytes {
		return ErrObjectMismatch
	}
	// The hash is valid only if the entire declared stream was consumed.
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(expectedSHA256) {
		return ErrChecksumMismatch
	}
	destInfo, err := p.Head(ctx, destinationBucket, destinationKey)
	if err != nil {
		return fmt.Errorf("verify destination metadata: %w", err)
	}
	if destInfo.Size != expectedBytes || !strings.EqualFold(destInfo.ContentType, contentType) {
		return ErrObjectMismatch
	}
	destHash, err := p.SHA256(ctx, destinationBucket, destinationKey, expectedBytes)
	if err != nil {
		return fmt.Errorf("verify destination checksum: %w", err)
	}
	if destHash != strings.ToLower(expectedSHA256) {
		return ErrChecksumMismatch
	}
	return nil
}
func (p *S3Provider) Head(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	u, err := p.presign(ctx, http.MethodHead, bucket, key, url.Values{}, nil, time.Minute)
	if err != nil {
		return ObjectInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	resp, err := p.do(req)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ObjectInfo{}, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ObjectInfo{}, fmt.Errorf("S3 HEAD failed with status %d", resp.StatusCode)
	}
	return ObjectInfo{Key: key, Size: resp.ContentLength, ContentType: strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]), ETag: strings.Trim(resp.Header.Get("ETag"), "\""), ModifiedAt: parseHTTPTime(resp.Header.Get("Last-Modified"))}, nil
}
func (p *S3Provider) Delete(ctx context.Context, bucket, key string) error {
	u, err := p.presign(ctx, http.MethodDelete, bucket, key, url.Values{}, nil, time.Minute)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := p.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("S3 DELETE failed with status %d", resp.StatusCode)
	}
	return nil
}
func (p *S3Provider) List(ctx context.Context, bucket, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if limit < 1 || limit > 1000 {
		return nil, "", errors.New("S3 list limit must be between 1 and 1000")
	}
	q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("continuation-token", cursor)
	}
	u, err := p.presign(ctx, http.MethodGet, bucket, "", q, nil, time.Minute)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := p.do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("S3 LIST failed with status %d", resp.StatusCode)
	}
	var result struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			ETag         string `xml:"ETag"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return nil, "", errors.New("invalid S3 LIST response")
	}
	items := make([]ObjectInfo, 0, len(result.Contents))
	for _, item := range result.Contents {
		items = append(items, ObjectInfo{Key: item.Key, Size: item.Size, ETag: strings.Trim(item.ETag, "\""), ModifiedAt: parseHTTPTime(item.LastModified)})
	}
	next := ""
	if result.IsTruncated {
		next = result.NextContinuationToken
	}
	return items, next, nil
}

func (p *S3Provider) presign(ctx context.Context, method, bucket, key string, extra url.Values, headers map[string]string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validBucketName(bucket) || ttl < time.Minute || ttl > time.Hour {
		return "", errors.New("invalid S3 bucket or presign lifetime")
	}
	if key != "" && (strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\x00\r\n\\") || path.Clean(key) != key || path.Clean(key) == "." || strings.Contains(key, "../")) {
		return "", ErrInvalidKey
	}
	now := time.Now().UTC()
	date, amzDate := now.Format("20060102"), now.Format("20060102T150405Z")
	scope := date + "/" + p.region + "/s3/aws4_request"
	u := *p.endpoint
	objectPath := p.endpointPathPrefix + "/" + bucket
	if key != "" {
		objectPath += "/" + key
	}
	u.Path = objectPath
	u.RawPath = escapeS3Path(objectPath)
	q := u.Query()
	for k, values := range extra {
		for _, value := range values {
			q.Add(k, value)
		}
	}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", p.accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	headerNames := make([]string, 0, len(headers)+1)
	canonicalHeaders := ""
	for name, value := range headers {
		name = strings.ToLower(name)
		headers[name] = strings.TrimSpace(value)
		headerNames = append(headerNames, name)
	}
	headerNames = append(headerNames, "host")
	sort.Strings(headerNames)
	for _, name := range headerNames {
		value := p.signingHost
		if name != "host" {
			value = headers[name]
		}
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
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}
func awsEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%7E", "~")
}
func escapeS3Path(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func s3SigningKey(secret, date, region string) []byte {
	dateKey := s3HMAC([]byte("AWS4"+secret), date)
	regionKey := s3HMAC(dateKey, region)
	serviceKey := s3HMAC(regionKey, "s3")
	return s3HMAC(serviceKey, "aws4_request")
}
func s3HMAC(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}
func parseHTTPTime(value string) time.Time {
	if t, err := http.ParseTime(value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}
func validBucketName(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || strings.HasPrefix(bucket, ".") || strings.HasSuffix(bucket, ".") ||
		strings.HasPrefix(bucket, "-") || strings.HasSuffix(bucket, "-") || strings.Contains(bucket, "..") ||
		strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") || strings.ContainsAny(bucket, "/\\?#\r\n") {
		return false
	}
	for _, c := range bucket {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
