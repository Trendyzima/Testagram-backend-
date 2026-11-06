package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	githubOIDCIssuer   = "https://token.actions.githubusercontent.com"
	githubOIDCAudience = "testagram-tv-health"
	tvHealthRepository = "Trendyzima/XClone-9b4dz1-9b4dz1-8936-20260809tr"
	tvHealthWorkflow   = "TV Channel Health Monitor"
	maxTVHealthBody    = 1 << 20
	maxTVHealthBatch   = 100
)

type githubOIDCKeySet struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

type githubOIDCKeyCache struct {
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time
}

var tvOIDCKeys githubOIDCKeyCache

func githubOIDCPublicKey(ctx context.Context, client *http.Client, kid string) (*rsa.PublicKey, error) {
	tvOIDCKeys.mu.Lock()
	defer tvOIDCKeys.mu.Unlock()
	if key := tvOIDCKeys.keys[kid]; key != nil && time.Now().Before(tvOIDCKeys.expiresAt) {
		return key, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubOIDCIssuer+"/.well-known/jwks", nil)
	if err != nil {
		return nil, errors.New("could not create GitHub OIDC key request")
	}
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(request)
	if err != nil {
		return nil, errors.New("GitHub OIDC key service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub OIDC key service returned HTTP %d", response.StatusCode)
	}
	var set githubOIDCKeySet
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&set); err != nil {
		return nil, errors.New("GitHub OIDC key set is invalid")
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, item := range set.Keys {
		if item.Kid == "" || item.Kty != "RSA" || (item.Alg != "" && item.Alg != "RS256") || (item.Use != "" && item.Use != "sig") {
			continue
		}
		modulus, errN := base64.RawURLEncoding.DecodeString(item.N)
		exponent, errE := base64.RawURLEncoding.DecodeString(item.E)
		if errN != nil || errE != nil || len(modulus) < 256 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		e := 0
		for _, b := range exponent {
			e = (e << 8) | int(b)
		}
		if e < 3 || e%2 == 0 {
			continue
		}
		keys[item.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	if len(keys) == 0 {
		return nil, errors.New("GitHub OIDC key set contains no supported signing keys")
	}
	tvOIDCKeys.keys = keys
	tvOIDCKeys.expiresAt = time.Now().Add(30 * time.Minute)
	if key := keys[kid]; key != nil {
		return key, nil
	}
	return nil, errors.New("GitHub OIDC signing key was not found")
}

type githubOIDCClaims struct {
	Issuer      string          `json:"iss"`
	Audience    json.RawMessage `json:"aud"`
	ExpiresAt   int64           `json:"exp"`
	NotBefore   int64           `json:"nbf"`
	Repository  string          `json:"repository"`
	Workflow    string          `json:"workflow"`
	WorkflowRef string          `json:"workflow_ref"`
	Ref         string          `json:"ref"`
	EventName   string          `json:"event_name"`
}

func (c githubOIDCClaims) hasAudience(want string) bool {
	var single string
	if json.Unmarshal(c.Audience, &single) == nil {
		return single == want
	}
	var multiple []string
	if json.Unmarshal(c.Audience, &multiple) == nil {
		for _, value := range multiple {
			if value == want {
				return true
			}
		}
	}
	return false
}

func verifyTVGitHubOIDCToken(ctx context.Context, client *http.Client, raw string, now time.Time) error {
	if len(raw) < 100 || len(raw) > 16<<10 || strings.ContainsAny(raw, "\r\n \t") {
		return errors.New("missing or invalid GitHub OIDC token")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return errors.New("invalid GitHub OIDC token format")
	}
	decode := func(value string, target any) error {
		data, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, target)
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}
	if err := decode(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return errors.New("unsupported GitHub OIDC token header")
	}
	key, err := githubOIDCPublicKey(ctx, client, header.KeyID)
	if err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("invalid GitHub OIDC signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return errors.New("GitHub OIDC signature verification failed")
	}
	var claims githubOIDCClaims
	if err := decode(parts[1], &claims); err != nil {
		return errors.New("invalid GitHub OIDC claims")
	}
	unixNow := now.Unix()
	if claims.Issuer != githubOIDCIssuer || !claims.hasAudience(githubOIDCAudience) || claims.ExpiresAt < unixNow-30 || claims.NotBefore > unixNow+30 {
		return errors.New("GitHub OIDC token is expired or has invalid issuer/audience")
	}
	if claims.Repository != tvHealthRepository || claims.Workflow != tvHealthWorkflow || claims.WorkflowRef != tvHealthRepository+"/.github/workflows/tv-health-monitor.yml@refs/heads/main" || claims.Ref != "refs/heads/main" {
		return errors.New("GitHub OIDC token is not from the authorized Testagram TV workflow")
	}
	switch claims.EventName {
	case "schedule", "workflow_dispatch", "push":
		return nil
	default:
		return errors.New("GitHub OIDC event is not authorized for TV health writes")
	}
}

type tvHealthInput struct {
	ChannelID  string `json:"channel_id"`
	URL        string `json:"url"`
	IsOnline   bool   `json:"is_online"`
	LatencyMS  int    `json:"latency_ms"`
	CheckError string `json:"check_error"`
	Source     string `json:"source"`
	Country    string `json:"country"`
	GroupName  string `json:"group_name"`
	Priority   int    `json:"priority"`
}

type tvHealthRequest struct {
	Channels []tvHealthInput `json:"channels"`
}

func validTVHealthInput(channel tvHealthInput) bool {
	if !replicationReadIdentity.MatchString(channel.ChannelID) || len(channel.URL) > 2048 || channel.LatencyMS < 0 || channel.LatencyMS > 30000 || len(channel.CheckError) > 500 || len(channel.Source) > 128 || len(channel.Country) > 128 || len(channel.GroupName) > 256 || channel.Priority < 0 || channel.Priority > 100000 {
		return false
	}
	parsed, err := url.ParseRequestURI(channel.URL)
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "https" || parsed.Scheme == "http")
}

func (s *service) tvHealthBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := verifyTVGitHubOIDCToken(ctx, s.upstream, r.Header.Get("X-GitHub-OIDC-Token"), time.Now()); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "authorized GitHub Actions identity required"})
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health database unavailable"})
		return
	}
	var body tvHealthRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTVHealthBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid TV health batch"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "request must contain one JSON value"})
		return
	}
	if len(body.Channels) == 0 || len(body.Channels) > maxTVHealthBatch {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "TV health batch must contain 1 to 100 channels"})
		return
	}
	unique := make(map[string]tvHealthInput, len(body.Channels))
	for _, channel := range body.Channels {
		if !validTVHealthInput(channel) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "TV health batch contains an invalid channel record"})
			return
		}
		unique[channel.ChannelID] = channel
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health database unavailable"})
		return
	}
	defer tx.Rollback(context.Background())
	now := time.Now().UTC()
	for _, channel := range unique {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_tv.channel_health
				(channel_id, url, is_online, last_checked_at, last_online_at,
				 consecutive_failures, consecutive_successes, latency_ms, check_error,
				 source, country, group_name, priority, updated_at)
			VALUES ($1,$2,$3,$4,CASE WHEN $3 THEN $4 ELSE NULL END,
			        CASE WHEN $3 THEN 0 ELSE 1 END,CASE WHEN $3 THEN 1 ELSE 0 END,
			        $5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10,$4)
			ON CONFLICT (channel_id) DO UPDATE SET
				url=EXCLUDED.url,
				is_online=EXCLUDED.is_online,
				last_checked_at=EXCLUDED.last_checked_at,
				last_online_at=CASE WHEN EXCLUDED.is_online THEN EXCLUDED.last_checked_at ELSE testagram_tv.channel_health.last_online_at END,
				consecutive_failures=CASE WHEN EXCLUDED.is_online THEN 0 ELSE testagram_tv.channel_health.consecutive_failures+1 END,
				consecutive_successes=CASE WHEN EXCLUDED.is_online THEN testagram_tv.channel_health.consecutive_successes+1 ELSE 0 END,
				latency_ms=EXCLUDED.latency_ms, check_error=EXCLUDED.check_error,
				source=EXCLUDED.source,country=EXCLUDED.country,group_name=EXCLUDED.group_name,
				priority=EXCLUDED.priority,updated_at=EXCLUDED.updated_at`,
			channel.ChannelID, channel.URL, channel.IsOnline, now, channel.LatencyMS, channel.CheckError,
			channel.Source, channel.Country, channel.GroupName, channel.Priority)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health persistence temporarily unavailable"})
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health persistence temporarily unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checked": len(unique), "persisted": len(unique), "checked_at": now.Format(time.RFC3339Nano)})
}

func (s *service) tvHealthRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health database unavailable"})
		return
	}
	limit := 20000
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 20000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "limit must be between 1 and 20000"})
			return
		}
		limit = parsed
	}
	onlineOnly := r.URL.Query().Get("online") != "false"
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT channel_id, is_online, last_checked_at, latency_ms, consecutive_successes, priority
		FROM testagram_tv.channel_health
		WHERE ($1::boolean = false OR is_online = true)
		ORDER BY is_online DESC, priority DESC, last_checked_at DESC
		LIMIT $2`, onlineOnly, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health data temporarily unavailable"})
		return
	}
	defer rows.Close()
	health := make([]map[string]any, 0, min(limit, 20000))
	for rows.Next() {
		var id string
		var isOnline bool
		var checkedAt time.Time
		var latency, successes, priority int
		if err := rows.Scan(&id, &isOnline, &checkedAt, &latency, &successes, &priority); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health data temporarily unavailable"})
			return
		}
		health = append(health, map[string]any{
			"channel_id": id, "is_online": isOnline, "last_checked_at": checkedAt.UTC().Format(time.RFC3339Nano),
			"latency_ms": latency, "consecutive_successes": successes, "priority": priority,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "TV health data temporarily unavailable"})
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=60")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": health, "count": len(health)})
}
