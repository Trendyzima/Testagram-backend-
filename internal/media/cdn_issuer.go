package media

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	cdnAssertionIssuer = "testagram-backend"
	cdnAssertionAudience = "testagram-cdn"
)

type cdnMediaAssertion struct {
	Issuer string `json:"iss"`
	Audience string `json:"aud"`
	ObjectKey string `json:"object_key"`
	ViewerID string `json:"viewer_id"`
	Visibility string `json:"visibility"`
	IssuedAt int64 `json:"iat"`
	ExpiresAt int64 `json:"exp"`
	Nonce string `json:"jti"`
	Signature string `json:"signature"`
}

type cdnMediaURLResponse struct {
	URL string `json:"url"`
	Path string `json:"path"`
	ExpiresAt int64 `json:"expires_at"`
	Visibility string `json:"visibility"`
	ViewerID string `json:"viewer_id"`
}

// CDNIssuer exchanges a Backend-authorized media decision for a short-lived
// CDN playback capability. Its issuer secret is distinct from the CDN's
// PLAYBACK_SECRET; this package never receives the playback signing key.
type CDNIssuer struct {
	endpoint *url.URL
	secret []byte
	client *http.Client
}

func NewCDNIssuer(baseURL, issuerSecret string, client *http.Client) (*CDNIssuer, error) {
	endpoint, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.Trim(endpoint.Path, "/") != "" {
		return nil, errors.New("CDN media base URL must be an HTTPS origin without credentials, path, query or fragment")
	}
	if len(issuerSecret) < 32 {
		return nil, errors.New("CDN media issuer secret must contain at least 32 characters")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	} else {
		copy := *client
		if copy.Timeout <= 0 {
			copy.Timeout = 4 * time.Second
		}
		client = &copy
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &CDNIssuer{endpoint: endpoint, secret: []byte(issuerSecret), client: client}, nil
}

// Issue creates a one-use, object-scoped Backend assertion and asks the CDN
// to mint a viewer/visibility-scoped playback URL. The caller must authorize
// the viewer against canonical media metadata before calling this method.
func (i *CDNIssuer) Issue(ctx context.Context, objectKey, viewerID, visibility string, lifetime time.Duration, now time.Time) (string, error) {
	if i == nil || i.endpoint == nil || len(i.secret) < 32 || i.client == nil {
		return "", errors.New("CDN media issuer is not configured")
	}
	if !objectKeyPattern.MatchString(objectKey) {
		return "", errors.New("media object key is invalid")
	}
	if visibility != "private" && visibility != "public" {
		return "", errors.New("media visibility is invalid")
	}
	if visibility == "public" {
		if viewerID != "public" {
			return "", errors.New("public media assertion must use the public viewer scope")
		}
	} else if !userIDPattern.MatchString(viewerID) || viewerID == "public" {
		return "", errors.New("private media viewer identity is invalid")
	}
	if lifetime < time.Minute || lifetime > 10*time.Minute {
		return "", errors.New("CDN media URL lifetime must be between 1 and 10 minutes")
	}
	now = now.UTC()
	assertion := cdnMediaAssertion{
		Issuer: cdnAssertionIssuer,
		Audience: cdnAssertionAudience,
		ObjectKey: objectKey,
		ViewerID: viewerID,
		Visibility: visibility,
		IssuedAt: now.Unix(),
		ExpiresAt: now.Add(lifetime).Unix(),
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate CDN media assertion nonce: %w", err)
	}
	assertion.Nonce = hex.EncodeToString(nonce)
	mac := hmac.New(sha256.New, i.secret)
	_, _ = mac.Write([]byte(cdnMediaAssertionCanonical(assertion)))
	assertion.Signature = hex.EncodeToString(mac.Sum(nil))

	body, err := json.Marshal(assertion)
	if err != nil {
		return "", fmt.Errorf("encode CDN media assertion: %w", err)
	}
	endpoint := *i.endpoint
	endpoint.Path = "/v1/media/url"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create CDN media issuance request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := i.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request CDN media capability: %w", err)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if readErr != nil {
		return "", fmt.Errorf("read CDN media issuance response: %w", readErr)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDN media issuance rejected with HTTP %d", response.StatusCode)
	}
	var result cdnMediaURLResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return "", fmt.Errorf("decode CDN media issuance response: %w", err)
	}
	if result.Path != objectKey || result.ExpiresAt != assertion.ExpiresAt ||
		result.Visibility != visibility || result.ViewerID != viewerID {
		return "", errors.New("CDN media issuance response does not match the Backend authorization decision")
	}
	issuedURL, err := url.Parse(result.URL)
	if err != nil || issuedURL.Scheme != "https" || issuedURL.Host != i.endpoint.Host ||
		issuedURL.User != nil || issuedURL.Fragment != "" || issuedURL.Path == "" ||
		!strings.HasSuffix(issuedURL.Path, "/"+objectKey) {
		return "", errors.New("CDN returned an unsafe media delivery URL")
	}
	query := issuedURL.Query()
	token := query.Get("token")
	parts := strings.Split(token, ".")
	if len(query) != 1 || len(parts) != 4 || parts[0] != strconv.FormatInt(assertion.ExpiresAt, 10) ||
		parts[1] != viewerID || parts[2] != visibility || len(parts[3]) != 64 {
		return "", errors.New("CDN returned a playback token outside the authorized scope")
	}
	if _, err := hex.DecodeString(parts[3]); err != nil {
		return "", errors.New("CDN returned an invalid playback token signature")
	}
	return issuedURL.String(), nil
}

func cdnMediaAssertionCanonical(a cdnMediaAssertion) string {
	return strings.Join([]string{
		"v1", a.Issuer, a.Audience, a.ObjectKey, a.ViewerID, a.Visibility,
		strconv.FormatInt(a.IssuedAt, 10), strconv.FormatInt(a.ExpiresAt, 10), a.Nonce,
	}, "\n")
}
