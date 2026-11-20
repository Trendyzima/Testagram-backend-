package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"embed"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed web/*
var webFiles embed.FS

type app struct {
	backendAPIURL    string
	serviceAuthKey   []byte
	database         *pgxpool.Pool
	publicURL        string
	publicOrigin     string
	prometheusURL    string
	apiHealthURL     string
	grafanaURL       string
	cookieSecure     bool
	aead             cipher.AEAD
	client           *http.Client
}

type session struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	AccessExpiresAt  int64  `json:"access_expires_at"`
	RefreshExpiresAt int64  `json:"refresh_expires_at"`
	ElevatedToken string `json:"elevated_token,omitempty"`
}

type authUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	StaffRole string `json:"staff_role,omitempty"`
}

type nativeAuthEnvelope struct {
	OK   bool `json:"ok"`
	Data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		User         struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	} `json:"data"`
	Error any `json:"error"`
}

type authInput struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

type inviteInput struct {
	Email string `json:"email"`
	Role string `json:"role"`
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" { return value }
	return fallback
}

func newApp() (*app, error) {
	rawKey := strings.TrimSpace(os.Getenv("ADMIN_SESSION_KEY"))
	key, err := hex.DecodeString(rawKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("ADMIN_SESSION_KEY must be 64 hexadecimal characters (32 random bytes)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	rawServiceKey := strings.TrimSpace(os.Getenv("ADMIN_SERVICE_AUTH_KEY"))
	var serviceAuthKey []byte
	if rawServiceKey != "" {
		serviceAuthKey, err = hex.DecodeString(rawServiceKey)
		if err != nil || len(serviceAuthKey) != 32 { return nil, errors.New("ADMIN_SERVICE_AUTH_KEY must be 64 hexadecimal characters (32 random bytes)") }
	}
	if len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET"))) < 32 {
		return nil, errors.New("TESTAGRAM_AUTH_JWT_SECRET must contain at least 32 characters")
	}
	backendAPIURL := strings.TrimRight(env("BACKEND_INTERNAL_URL", "http://api:8080"), "/")
	backendParsed, backendErr := url.Parse(backendAPIURL)
	if backendErr != nil || backendParsed.Host == "" || backendParsed.User != nil || backendParsed.RawQuery != "" || backendParsed.Fragment != "" ||
		(backendParsed.Scheme != "https" && !(backendParsed.Scheme == "http" && (backendParsed.Hostname() == "api" || backendParsed.Hostname() == "localhost" || backendParsed.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("BACKEND_INTERNAL_URL must be HTTPS or a private Docker API origin")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required for PostgreSQL-owned staff roles")
	}
	db, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("staff role database unavailable: %w", err)
	}
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_URL")), "/")
	parsed, err := url.Parse(publicURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		db.Close()
		return nil, errors.New("ADMIN_PUBLIC_URL must be an absolute http(s) URL")
	}
	secure := !strings.EqualFold(env("ADMIN_COOKIE_SECURE", "true"), "false")
	if parsed.Scheme == "https" && !secure {
		db.Close()
		return nil, errors.New("ADMIN_COOKIE_SECURE cannot be false when ADMIN_PUBLIC_URL uses HTTPS")
	}
	return &app{
		backendAPIURL: backendAPIURL,
		serviceAuthKey: serviceAuthKey,
		database: db,
		publicURL: publicURL,
		publicOrigin: parsed.Scheme + "://" + parsed.Host,
		prometheusURL: strings.TrimRight(env("PROMETHEUS_URL", "http://prometheus:9090"), "/"),
		apiHealthURL: env("API_HEALTH_URL", "http://api:8080/readyz"),
		grafanaURL: env("GRAFANA_URL", "http://127.0.0.1:3000"),
		cookieSecure: secure,
		aead: aead,
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (a *app) routes() http.Handler {
	files, err := fs.Sub(webFiles, "web")
	if err != nil { panic(err) }
	mux := http.NewServeMux()
	static := http.FileServer(http.FS(files))
	mux.HandleFunc("GET /reset-password", func(w http.ResponseWriter, r *http.Request) { r.URL.Path = "/index.html"; static.ServeHTTP(w, r) })
	mux.Handle("GET /", static)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /api/session", a.handleSession)
	mux.HandleFunc("POST /api/auth/sign-in", a.handleSignIn)
	mux.HandleFunc("POST /api/auth/sign-up", a.handleSignUp)
	mux.HandleFunc("POST /api/auth/recover", a.handleRecover)
	mux.HandleFunc("POST /api/auth/update-password", a.handleUpdatePassword)
	mux.HandleFunc("POST /api/auth/sign-out", a.handleSignOut)
	mux.HandleFunc("GET /api/overview", a.handleOverview)
	mux.HandleFunc("POST /api/staff/invite", a.handleInvite)
	mux.HandleFunc("POST /api/admin/step-up", a.handleStaffWebAuthnLoginBegin)
	mux.HandleFunc("POST /api/admin/staff/{id}/webauthn/enrollment-authorize", a.handleStaffWebAuthnAuthorize)
	mux.HandleFunc("POST /api/admin/webauthn/registration/begin", a.handleStaffWebAuthnRegisterBegin)
	mux.HandleFunc("POST /api/admin/webauthn/registration/finish", a.handleStaffWebAuthnRegisterFinish)
	mux.HandleFunc("POST /api/admin/webauthn/assertion/begin", a.handleStaffWebAuthnLoginBegin)
	mux.HandleFunc("POST /api/admin/webauthn/assertion/finish", a.handleStaffWebAuthnLoginFinish)
	mux.HandleFunc("GET /api/admin/webauthn/readiness", a.handleWebAuthnReadiness)
	mux.HandleFunc("GET /api/admin/staff", a.handleStaffList)
	mux.HandleFunc("PUT /api/admin/staff/{id}/role", a.handleStaffRoleChange)
	mux.HandleFunc("DELETE /api/admin/staff/{id}", a.handleStaffRevoke)
	mux.HandleFunc("PUT /api/admin/staff/{id}/permissions", a.handleSetStaffPermission)
	mux.HandleFunc("POST /api/admin/sensitive-actions", a.handleSensitiveActionRequest)
	mux.HandleFunc("GET /api/admin/sensitive-actions", a.handleSensitiveActionQueue)
	mux.HandleFunc("GET /api/admin/audit", a.handleAdminAudit)
	mux.HandleFunc("GET /api/admin/identity/review-queue", a.handleIdentityReviewQueue)
	mux.HandleFunc("GET /api/admin/identity/sessions/{session_id}/evidence/{evidence_id}", a.handleIdentityReviewEvidence)
	mux.HandleFunc("POST /api/admin/identity/sessions/{session_id}/decision", a.handleIdentityReviewDecision)
	mux.HandleFunc("POST /internal/admin/authorize", a.handleInternalAuthorize)
	mux.HandleFunc("POST /api/admin/sensitive-actions/{id}/decision", a.handleSensitiveActionDecision)
	return a.securityHeaders(mux)
}

func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && r.URL.Path != "/internal/admin/authorize" && !a.sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error":"Request origin is not allowed"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) sameOrigin(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	return origin != "" && origin == a.publicOrigin
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error":"Invalid request body"})
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error":"Request must contain one JSON value"})
		return false
	}
	return true
}

func (a *app) setSession(w http.ResponseWriter, s session) error {
	raw, err := json.Marshal(s)
	if err != nil { return err }
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil { return err }
	sealed := a.aead.Seal(nonce, nonce, raw, []byte("testagram-staff-session-v3"))
	maxAge := 60 * 60 * 24 * 30
	if s.RefreshExpiresAt > 0 {
		remaining := int(s.RefreshExpiresAt - time.Now().Unix())
		if remaining < maxAge { maxAge = remaining }
	}
	if maxAge < 1 { return errors.New("native staff session is expired") }
	http.SetCookie(w, &http.Cookie{Name:"testagram-staff", Value:hex.EncodeToString(sealed), Path:"/", HttpOnly:true, Secure:a.cookieSecure, SameSite:http.SameSiteStrictMode, MaxAge:maxAge})
	return nil
}

func (a *app) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name:"testagram-staff", Value:"", Path:"/", HttpOnly:true, Secure:a.cookieSecure, SameSite:http.SameSiteStrictMode, MaxAge:-1})
}

func (a *app) readSession(r *http.Request) (session, error) {
	cookie, err := r.Cookie("testagram-staff")
	if err != nil { return session{}, err }
	sealed, err := hex.DecodeString(cookie.Value)
	if err != nil { return session{}, err }
	n := a.aead.NonceSize()
	if len(sealed) <= n { return session{}, errors.New("invalid session") }
	plain, err := a.aead.Open(nil, sealed[:n], sealed[n:], []byte("testagram-staff-session-v3"))
	if err != nil { return session{}, err }
	var s session
	if err := json.Unmarshal(plain, &s); err != nil || s.AccessToken == "" || s.RefreshToken == "" || s.RefreshExpiresAt <= time.Now().Unix() {
		return session{}, errors.New("invalid session")
	}
	return s, nil
}


var nativeConsoleUUIDPattern = regexp.MustCompile("(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")

type nativeJWTClaims struct {
	Subject string `json:"sub"`
	Email string `json:"email"`
	Role string `json:"role"`
	Issuer string `json:"iss"`
	Audience string `json:"aud"`
	IssuedAt int64 `json:"iat"`
	ExpiresAt int64 `json:"exp"`
	TokenVersion int64 `json:"ver"`
	SessionID string `json:"sid"`
}

func (a *app) backendRequest(ctx context.Context, method, path string, bearer string, body any, dst any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body); if err != nil { return 0, err }; reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.backendAPIURL+path, reader)
	if err != nil { return 0, err }
	req.Header.Set("Accept", "application/json")
	if bearer != "" { req.Header.Set("Authorization", "Bearer "+bearer) }
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := a.client.Do(req); if err != nil { return 0, err }; defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); if err != nil { return resp.StatusCode, err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return resp.StatusCode, fmt.Errorf("Testagram authentication returned HTTP %d", resp.StatusCode) }
	if dst != nil { if err := json.Unmarshal(raw, dst); err != nil { return resp.StatusCode, err } }
	return resp.StatusCode, nil
}

func (a *app) refreshSession(ctx context.Context, s *session) error {
	if s.RefreshToken == "" || s.RefreshExpiresAt <= time.Now().Unix() { return errors.New("native refresh session expired") }
	var envelope nativeAuthEnvelope
	_, err := a.backendRequest(ctx, http.MethodPost, "/api/auth/refresh", "", map[string]string{"refresh_token":s.RefreshToken}, &envelope)
	if err != nil || !envelope.OK || envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" {
		return errors.New("native session refresh failed")
	}
	s.AccessToken = envelope.Data.AccessToken
	s.RefreshToken = envelope.Data.RefreshToken
	s.AccessExpiresAt = time.Now().Add(time.Duration(envelope.Data.ExpiresIn)*time.Second).Unix()
	// Refresh-token rotation renews the 30-day session window.
	s.RefreshExpiresAt = time.Now().Add(30*24*time.Hour).Unix()
	return nil
}

func (a *app) currentUser(w http.ResponseWriter, r *http.Request) (session, authUser, bool) {
	s, err := a.readSession(r)
	if err != nil { a.clearSession(w); return session{}, authUser{}, false }
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second); defer cancel()
	if s.AccessExpiresAt <= time.Now().Add(30*time.Second).Unix() {
		if err := a.refreshSession(ctx, &s); err != nil { a.clearSession(w); return session{}, authUser{}, false }
		if err := a.setSession(w, s); err != nil { a.clearSession(w); return session{}, authUser{}, false }
	}
	claims, err := a.verifyNativeSession(ctx, s.AccessToken)
	if err != nil { a.clearSession(w); return session{}, authUser{}, false }
	user := authUser{ID:claims.Subject, Email:claims.Email}
	if a.database == nil { return session{}, authUser{}, false }
	err = a.database.QueryRow(ctx, `
		SELECT rg.role
		FROM testagram_auth.staff_principals sp
		JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
		WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE
		  AND rg.role IN ('owner','administrator','identity_reviewer','engineer','operations','support','moderator')
		ORDER BY CASE rg.role WHEN 'owner' THEN 0 WHEN 'administrator' THEN 1 ELSE 2 END
		LIMIT 1
	`, user.ID).Scan(&user.StaffRole)
	if errors.Is(err, pgx.ErrNoRows) { user.StaffRole = "" } else if err != nil { return session{}, authUser{}, false }
	return s, user, true
}

func (a *app) verifyNativeSession(ctx context.Context, token string) (nativeJWTClaims, error) {
	var claims nativeJWTClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 { return claims, errors.New("invalid native access token") }
	secret := strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET"))
	if len(secret) < 32 { return claims, errors.New("native authentication is not configured") }
	unsigned := parts[0]+"."+parts[1]
	headerBytes, headerErr := base64.RawURLEncoding.DecodeString(parts[0])
	var header struct { Algorithm string `json:"alg"`; Type string `json:"typ"` }
	if headerErr != nil || json.Unmarshal(headerBytes,&header)!=nil || header.Algorithm!="HS256" || header.Type!="JWT" { return claims, errors.New("unsupported native token header") }
	signature, err := base64.RawURLEncoding.DecodeString(parts[2]); if err != nil { return claims, errors.New("invalid native token signature") }
	mac := hmac.New(sha256.New, []byte(secret)); _, _ = mac.Write([]byte(unsigned))
	if !hmac.Equal(signature, mac.Sum(nil)) { return claims, errors.New("invalid native token signature") }
	payload, err := base64.RawURLEncoding.DecodeString(parts[1]); if err != nil || json.Unmarshal(payload,&claims)!=nil { return claims, errors.New("invalid native token claims") }
	now := time.Now().UTC().Unix()
	if claims.Issuer!="testagram-auth" || claims.Audience!="testagram-api" || claims.Role!="authenticated" ||
		!nativeConsoleUUIDPattern.MatchString(claims.Subject) || !nativeConsoleUUIDPattern.MatchString(claims.SessionID) ||
		claims.TokenVersion<1 || claims.IssuedAt<=0 || claims.IssuedAt>now+60 ||
		claims.ExpiresAt<=now || claims.ExpiresAt<=claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt>16*60 {
		return claims, errors.New("expired or invalid native token claims")
	}
	var active bool
	err = a.database.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM testagram_auth.sessions s
		JOIN testagram_auth.identities i ON i.user_id=s.user_id
		WHERE s.session_id=$1::uuid AND s.user_id=$2::uuid AND s.token_version=$3
		 AND s.revoked_at IS NULL AND s.expires_at>now()
		 AND i.token_version=$3 AND i.disabled_at IS NULL
		 AND i.email_verified_at IS NOT NULL AND NOT i.password_reset_required
		 AND NOT EXISTS(SELECT 1 FROM testagram_identity.native_registration_provisioning p
		                WHERE p.user_id=i.user_id AND p.status<>'active')
	)`, claims.SessionID, claims.Subject, claims.TokenVersion).Scan(&active)
	if err != nil || !active { return claims, errors.New("native session is revoked or unavailable") }
	return claims, nil
}

func staffRole(user authUser) string {
	switch user.StaffRole {
	case "owner", "administrator", "identity_reviewer", "engineer", "operations", "support", "moderator":
		return user.StaffRole
	default:
		return ""
	}
}

func (a *app) handleSession(w http.ResponseWriter, r *http.Request) {
	_, user, ok := a.currentUser(w, r)
	if !ok { writeJSON(w, 200, map[string]any{"authenticated":false,"staff":false}); return }
	role := staffRole(user)
	writeJSON(w, 200, map[string]any{"authenticated":true,"staff":role!="","email":user.Email,"role":role,"user_id":user.ID})
}

func (a *app) identityProxy(w http.ResponseWriter, r *http.Request, backendPath string, evidenceStream bool) {
	staffSession, _, ok := a.requireStaff(w, r)
	if !ok {
		return
	}
	// Identity queue contents, evidence retrieval and review decisions are
	// privileged operations. Never treat an enrollment timestamp or password
	// reauthentication as a live phishing-resistant MFA challenge.
	if !a.requireRecentStepUp(w, staffSession) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	jwt := staffSession.ElevatedToken
	if jwt == "" { writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"Backend-issued MFA elevation is unavailable"});return }
	var body []byte
	var err error
	if r.Method == http.MethodPost {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Review request is too large"})
			return
		}
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, a.backendAPIURL+backendPath, reader)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Identity review service is unavailable"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *a.client
	client.Timeout = 45 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Identity review service could not be reached"})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	if evidenceStream && resp.StatusCode == http.StatusOK {
		for _, key := range []string{"Content-Type", "Content-Length", "Content-Disposition", "X-Content-Type-Options", "Content-Security-Policy", "Cross-Origin-Resource-Policy"} {
			if value := resp.Header.Get(key); value != "" {
				w.Header().Set(key, value)
			}
		}
		size, parseErr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if parseErr != nil || size < 1 || size > 15<<20 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Evidence response metadata is invalid"})
			return
		}
		if _, err := io.CopyN(w, resp.Body, size); err != nil {
			// Content-Length makes a truncated stream fail closed at the client.
			return
		}
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Identity review response could not be read"})
		return
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

func (a *app) handleIdentityReviewQueue(w http.ResponseWriter, r *http.Request) {
	a.identityProxy(w, r, "/api/admin/identity/review-queue", false)
}

func (a *app) handleIdentityReviewEvidence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	backendPath := "/api/admin/identity/sessions/" + url.PathEscape(r.PathValue("session_id")) +
		"/evidence/" + url.PathEscape(r.PathValue("evidence_id"))
	a.identityProxy(w, r, backendPath, true)
}

func (a *app) handleIdentityReviewDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	backendPath := "/api/admin/identity/sessions/" + url.PathEscape(r.PathValue("session_id")) + "/decision"
	a.identityProxy(w, r, backendPath, false)
}

func (a *app) handleSignIn(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w, r, &input) {
		return
	}
	input.Email = strings.TrimSpace(input.Email)
	if input.Email == "" || len(input.Email) > 254 || len(input.Password) < 1 || len(input.Password) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Enter a valid email and password"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var envelope nativeAuthEnvelope
	status, err := a.backendRequest(ctx, http.MethodPost, "/api/auth/login", "", map[string]string{"email": input.Email, "password": input.Password}, &envelope)
	if err != nil || !envelope.OK || envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" || envelope.Data.User.ID == "" {
		if status == http.StatusTooManyRequests {
			writeJSON(w, status, map[string]string{"error": "Too many sign-in attempts. Try again later."})
			return
		}
		if status == 0 || status >= http.StatusInternalServerError {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Testagram authentication is temporarily unavailable. Try again shortly."})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Sign-in failed. Check your credentials, email verification and account approval."})
		return
	}
	s := session{
		AccessToken: envelope.Data.AccessToken, RefreshToken: envelope.Data.RefreshToken,
		AccessExpiresAt: time.Now().Add(time.Duration(envelope.Data.ExpiresIn) * time.Second).Unix(),
		RefreshExpiresAt: time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	if _, staff, sessionErr := a.currentUserFromSession(ctx, s); sessionErr != nil || !staff {
		_, revokeErr := a.backendRequest(ctx, http.MethodPost, "/api/auth/logout", s.AccessToken, map[string]string{}, nil)
		if revokeErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "This account is not enabled for staff access, and its temporary sign-in session could not be revoked. Contact an administrator."})
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "This account is not an enabled Testagram staff member with enrolled MFA."})
		return
	}
	if err := a.setSession(w, s); err != nil {
		_, _ = a.backendRequest(ctx, http.MethodPost, "/api/auth/logout", s.AccessToken, map[string]string{}, nil)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to create secure staff session"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (a *app) handleSignUp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w,http.StatusForbidden,map[string]string{"error":"Staff accounts are provisioned through the controlled two-person Testagram staff workflow."})
}

func (a *app) handleRecover(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w, r, &input) {
		return
	}
	input.Email = strings.TrimSpace(input.Email)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	status, err := a.backendRequest(ctx, http.MethodPost, "/api/auth/forgot-password", "", map[string]string{"email": input.Email}, nil)
	if err != nil {
		if status == http.StatusTooManyRequests {
			writeJSON(w, status, map[string]string{"error": "Too many recovery requests. Try again later."})
			return
		}
		if status == 0 || status >= http.StatusInternalServerError {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Testagram recovery is temporarily unavailable. Try again shortly."})
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "If the staff account exists, Testagram will send password recovery instructions."})
}
func (a *app) handleUpdatePassword(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w, r, &input) {
		return
	}
	if len(input.Token) < 32 || len(input.Token) > 128 || len(input.Password) < 12 || len(input.Password) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A valid recovery link and a password of 12–128 bytes are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	status, err := a.backendRequest(ctx, http.MethodPost, "/api/auth/reset-password", "", map[string]string{"token": input.Token, "password": input.Password}, nil)
	if err != nil {
		if status == 0 || status >= http.StatusInternalServerError {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Testagram could not update the password right now. Try again shortly."})
			return
		}
		if status == http.StatusTooManyRequests {
			writeJSON(w, status, map[string]string{"error": "Too many password reset attempts. Request a new link later."})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password could not be updated. The recovery link may have expired."})
		return
	}
	a.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Password updated. You can now sign in."})
}
func (a *app) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s, err := a.readSession(r)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		status, revokeErr := a.backendRequest(ctx, http.MethodPost, "/api/auth/logout", s.AccessToken, map[string]any{}, nil)
		cancel()
		// Keep the encrypted staff cookie when the Backend is unavailable so
		// the user can retry. A 401/403 means the server no longer recognizes
		// this session, so clearing the local cookie is safe.
		if revokeErr != nil && status != http.StatusUnauthorized && status != http.StatusForbidden {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Testagram could not confirm server-side sign-out. Your staff session is retained so you can safely retry."})
			return
		}
	}
	a.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (a *app) currentUserFromSession(ctx context.Context, s session) (authUser, bool, error) {
	claims,err:=a.verifyNativeSession(ctx,s.AccessToken)
	if err!=nil{return authUser{},false,err}
	user:=authUser{ID:claims.Subject,Email:claims.Email}
	err=a.database.QueryRow(ctx,`SELECT rg.role FROM testagram_auth.staff_principals sp
		JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
		WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE
		ORDER BY CASE rg.role WHEN 'owner' THEN 0 WHEN 'administrator' THEN 1 ELSE 2 END LIMIT 1`,user.ID).Scan(&user.StaffRole)
	if errors.Is(err,pgx.ErrNoRows){return user,false,nil}
	if err!=nil{return authUser{},false,err}
	return user,staffRole(user)!="",nil
}

func (a *app) requireStaff(w http.ResponseWriter,r *http.Request,roles ...string) (session,authUser,bool) {
	s,user,ok:=a.currentUser(w,r)
	if !ok { writeJSON(w,401,map[string]string{"error":"An active Testagram staff session is required"});return session{},authUser{},false }
	role:=staffRole(user)
	if role=="" { writeJSON(w,403,map[string]string{"error":"Your Testagram staff access is not enabled or MFA is not enrolled"});return session{},authUser{},false }
	if len(roles)>0 { allowed:=false;for _,candidate:=range roles{if role==candidate{allowed=true;break}};if !allowed{writeJSON(w,403,map[string]string{"error":"This action requires owner privileges"});return session{},authUser{},false} }
	return s,user,true
}

func (a *app) handleInvite(w http.ResponseWriter,r *http.Request) {
	s,_,ok:=a.requireStaff(w,r,"owner");if !ok{return}
	if !a.requireRecentStepUp(w,s){return}
	writeJSON(w,http.StatusNotImplemented,map[string]string{"error":"Native staff invitation provisioning is not yet available. Staff access remains closed until the two-person Testagram provisioning workflow is completed."})
}

type promResponse struct {
	Status string `json:"status"`
	Data struct { ResultType string `json:"resultType"`; Result []struct { Metric map[string]string `json:"metric"`; Value []any `json:"value"` } `json:"result"` } `json:"data"`
}

func (a *app) queryPrometheus(ctx context.Context, query string) (float64,error) {
	endpoint:=a.prometheusURL+"/api/v1/query?query="+url.QueryEscape(query)
	req,err:=http.NewRequestWithContext(ctx,http.MethodGet,endpoint,nil);if err!=nil{return 0,err}
	resp,err:=a.client.Do(req);if err!=nil{return 0,err};defer resp.Body.Close()
	if resp.StatusCode!=200{return 0,fmt.Errorf("prometheus returned %d",resp.StatusCode)}
	var result promResponse
	if err:=json.NewDecoder(io.LimitReader(resp.Body,1<<20)).Decode(&result);err!=nil{return 0,err}
	if result.Status!="success" {
		return 0,fmt.Errorf("prometheus query did not succeed")
	}
	if len(result.Data.Result)==0 || len(result.Data.Result[0].Value)<2 {
		return 0,fmt.Errorf("prometheus query returned no sample")
	}
	value,ok:=result.Data.Result[0].Value[1].(string)
	if !ok{return 0,fmt.Errorf("prometheus returned a non-string sample")}
	var n float64
	if _,err:=fmt.Sscanf(value,"%f",&n);err!=nil{return 0,fmt.Errorf("parse prometheus sample: %w",err)}
	return n,nil
}

func (a *app) handleOverview(w http.ResponseWriter,r *http.Request) {
	if _,_,ok:=a.requireStaff(w,r);!ok{return}
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second);defer cancel()
	type metric struct { Value float64 `json:"value"`; Available bool `json:"available"` }
	queries:=map[string]string{
		"api_up":`min(up{job="testagram-api"})`,
		"platform_up":`min(up{job="testagram-platform"})`,
		"request_rate":`sum(rate(testagram_http_requests_total[5m]))`,
		"error_rate":`sum(rate(testagram_http_requests_by_route_total{status=~"5.."}[5m]))`,
		"p95_seconds":`histogram_quantile(0.95, sum by (le) (rate(testagram_http_request_duration_seconds_bucket[5m])))`,
		"db_acquired":`testagram_db_pool_acquired_connections`,
		"db_max":`testagram_db_pool_max_connections`,
		"outbox_pending":`sum(testagram_replication_outbox_pending)`,
	}
	values:=map[string]metric{}
	for name,q:=range queries {
		v,err:=a.queryPrometheus(ctx,q)
		values[name]=metric{Value:v,Available:err==nil}
	}
	apiStatus:="unknown"
	healthReq,err:=http.NewRequestWithContext(ctx,http.MethodGet,a.apiHealthURL,nil)
	if err==nil {
		resp,e:=a.client.Do(healthReq)
		if e==nil { io.Copy(io.Discard,io.LimitReader(resp.Body,4096));resp.Body.Close();if resp.StatusCode>=200&&resp.StatusCode<300{apiStatus="healthy"}else{apiStatus="unhealthy"} }
	}
	writeJSON(w,200,map[string]any{"metrics":values,"api_health":apiStatus,"grafana_url":a.grafanaURL,"checked_at":time.Now().UTC().Format(time.RFC3339)})
}

func main() {
	a,err:=newApp();if err!=nil{log.Fatal(err)}
	defer a.database.Close()
	addr:=env("ADMIN_ADDR",":8090")
	srv:=&http.Server{Addr:addr,Handler:a.routes(),ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,WriteTimeout:15*time.Second,IdleTimeout:60*time.Second,MaxHeaderBytes:1<<20}
	log.Printf("Testagram staff console listening on %s",addr)
	log.Fatal(srv.ListenAndServe())
}
