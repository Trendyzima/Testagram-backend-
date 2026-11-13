package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"embed"
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
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed web/*
var webFiles embed.FS

type app struct {
	appwriteEndpoint string
	appwriteProject  string
	backendAPIURL    string
	appwriteAPIKey   string
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
	Secret      string `json:"secret"`
	ExpiresAt   int64  `json:"expires_at"`
	StepUpUntil int64  `json:"step_up_until,omitempty"`
}

type authUser struct {
	ID        string `json:"$id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Status    bool   `json:"status"`
	StaffRole string `json:"staff_role,omitempty"`
}

type appwriteSession struct {
	ID     string `json:"$id"`
	UserID string `json:"userId"`
	Secret string `json:"secret"`
	Expire string `json:"expire"`
}

type authInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	UserID   string `json:"user_id"`
	Secret   string `json:"secret"`
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
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), "/")
	project := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	rawServiceKey:=strings.TrimSpace(os.Getenv("ADMIN_SERVICE_AUTH_KEY"))
	var serviceAuthKey []byte
	if rawServiceKey!="" {
		serviceAuthKey,err=hex.DecodeString(rawServiceKey)
		if err!=nil || len(serviceAuthKey)!=32 { return nil,errors.New("ADMIN_SERVICE_AUTH_KEY must be 64 hexadecimal characters (32 random bytes)") }
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Host == "" || parsedEndpoint.User != nil ||
		(parsedEndpoint.Scheme != "https" && !(parsedEndpoint.Scheme == "http" && (parsedEndpoint.Hostname() == "localhost" || parsedEndpoint.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("APPWRITE_ENDPOINT must be HTTPS (HTTP is allowed only for localhost)")
	}
	if !strings.HasSuffix(parsedEndpoint.Path, "/v1") {
		parsedEndpoint.Path = strings.TrimRight(parsedEndpoint.Path, "/") + "/v1"
	}
	if project == "" {
		return nil, errors.New("APPWRITE_PROJECT_ID is required")
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
		appwriteEndpoint: strings.TrimRight(parsedEndpoint.String(), "/"),
		appwriteProject: project,
		backendAPIURL: backendAPIURL,
		appwriteAPIKey: strings.TrimSpace(os.Getenv("APPWRITE_API_KEY")),
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
	mux.HandleFunc("POST /api/admin/step-up", a.handleStepUp)
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
	mux.HandleFunc("POST /api/admin/identity/applications/{application_id}/approve", a.handleIdentityApplicationApprove)
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

func (a *app) appwriteRequest(ctx context.Context, method, path, sessionSecret string, body any, dst any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil { return 0, err }
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.appwriteEndpoint+path, reader)
	if err != nil { return 0, err }
	req.Header.Set("X-Appwrite-Project", a.appwriteProject)
	req.Header.Set("Accept", "application/json")
	if sessionSecret != "" { req.Header.Set("X-Appwrite-Session", sessionSecret) }
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := a.client.Do(req)
	if err != nil { return 0, err }
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil { return resp.StatusCode, err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, errors.New("Appwrite rejected the request")
	}
	if dst != nil {
		if err := json.Unmarshal(raw, dst); err != nil { return resp.StatusCode, err }
	}
	return resp.StatusCode, nil
}

func (a *app) appwriteAdminRequest(ctx context.Context, method, path string, body any, dst any) (int, error) {
	if a.appwriteAPIKey == "" { return 0, errors.New("APPWRITE_API_KEY is not configured") }
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil { return 0, err }
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.appwriteEndpoint+path, reader)
	if err != nil { return 0, err }
	req.Header.Set("X-Appwrite-Project", a.appwriteProject)
	req.Header.Set("X-Appwrite-Key", a.appwriteAPIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := a.client.Do(req)
	if err != nil { return 0, err }
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil { return resp.StatusCode, err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, errors.New("Appwrite admin operation failed")
	}
	if dst != nil {
		if err := json.Unmarshal(raw, dst); err != nil { return resp.StatusCode, err }
	}
	return resp.StatusCode, nil
}

func (a *app) setSession(w http.ResponseWriter, s session) error {
	raw, err := json.Marshal(s)
	if err != nil { return err }
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil { return err }
	sealed := a.aead.Seal(nonce, nonce, raw, []byte("testagram-staff-session-v2"))
	maxAge := 60 * 60 * 24 * 7
	if s.ExpiresAt > 0 {
		remaining := int(s.ExpiresAt - time.Now().Unix())
		if remaining < maxAge { maxAge = remaining }
	}
	if maxAge < 1 { return errors.New("Appwrite session is expired") }
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
	plain, err := a.aead.Open(nil, sealed[:n], sealed[n:], []byte("testagram-staff-session-v2"))
	if err != nil { return session{}, err }
	var s session
	if err := json.Unmarshal(plain, &s); err != nil || s.Secret == "" || s.ExpiresAt <= time.Now().Unix() {
		return session{}, errors.New("invalid session")
	}
	return s, nil
}

func (a *app) currentUser(w http.ResponseWriter, r *http.Request) (session, authUser, bool) {
	s, err := a.readSession(r)
	if err != nil { a.clearSession(w); return session{}, authUser{}, false }
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var user authUser
	_, err = a.appwriteRequest(ctx, http.MethodGet, "/account", s.Secret, nil, &user)
	if err != nil || user.ID == "" || !user.Status {
		a.clearSession(w)
		return session{}, authUser{}, false
	}
	if a.database == nil { return session{}, authUser{}, false }
	err = a.database.QueryRow(ctx, `SELECT role FROM testagram_auth.staff_roles WHERE appwrite_user_id=$1 AND access_revoked_at IS NULL`, user.ID).Scan(&user.StaffRole)
	if errors.Is(err, pgx.ErrNoRows) {
		user.StaffRole = ""
	} else if err != nil {
		return session{}, authUser{}, false
	}
	return s, user, true
}

func staffRole(user authUser) string {
	switch user.StaffRole {
	case "owner", "administrator", "engineer", "operations", "support", "moderator":
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

func (a *app) identityJWT(ctx context.Context, sessionSecret string) (string, error) {
	var result struct {
		JWT string `json:"jwt"`
	}
	if _, err := a.appwriteRequest(ctx, http.MethodPost, "/account/jwts", sessionSecret, nil, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.JWT) == "" {
		return "", errors.New("Appwrite did not issue a reviewer JWT")
	}
	return result.JWT, nil
}

// identityProxy authenticates the staff cookie locally, obtains a short-lived
// Appwrite JWT server-side, then lets Backend independently enforce identity.review.
// The JWT and private evidence bytes never enter browser JavaScript storage.
func (a *app) identityProxy(w http.ResponseWriter, r *http.Request, backendPath string, evidenceStream bool) {
	staffSession, _, ok := a.requireStaff(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	jwt, err := a.identityJWT(ctx, staffSession.Secret)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Reviewer authentication could not be established"})
		return
	}
	var body []byte
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

func (a *app) handleIdentityApplicationApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	backendPath := "/api/admin/identity/applications/" + url.PathEscape(r.PathValue("application_id")) + "/approve"
	a.identityProxy(w, r, backendPath, false)
}

func (a *app) handleSignIn(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	input.Email = strings.TrimSpace(input.Email)
	if input.Email == "" || len(input.Email)>320 || len(input.Password)<1 || len(input.Password)>1024 {
		writeJSON(w,400,map[string]string{"error":"Enter a valid email and password"}); return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second)
	defer cancel()
	var created appwriteSession
	_,err:=a.appwriteRequest(ctx,http.MethodPost,"/account/sessions/email","",map[string]string{"email":input.Email,"password":input.Password},&created)
	if err!=nil || created.Secret=="" {
		writeJSON(w,401,map[string]string{"error":"Sign-in failed. Check your credentials or email confirmation."}); return
	}
	expiresAt := time.Now().Add(24*time.Hour).Unix()
	if parsed, parseErr := time.Parse(time.RFC3339, created.Expire); parseErr == nil { expiresAt = parsed.Unix() }
	if err:=a.setSession(w,session{Secret:created.Secret,ExpiresAt:expiresAt});err!=nil {
		writeJSON(w,500,map[string]string{"error":"Unable to create secure session"});return
	}
	writeJSON(w,200,map[string]any{"ok":true})
}

func (a *app) handleSignUp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w,http.StatusForbidden,map[string]string{"error":"Staff accounts are invitation-only. Ask a Testagram owner for an invitation."})
}

func (a *app) handleRecover(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	input.Email=strings.TrimSpace(input.Email)
	if input.Email==""||len(input.Email)>320 { writeJSON(w,400,map[string]string{"error":"Enter a valid email address"});return }
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second)
	defer cancel()
	redirect:=a.publicURL+"/reset-password"
	_,_ = a.appwriteRequest(ctx,http.MethodPost,"/account/recovery","",map[string]string{"email":input.Email,"url":redirect},nil)
	writeJSON(w,200,map[string]string{"message":"If the account exists, a password-reset email will be sent."})
}

func (a *app) handleUpdatePassword(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	if input.UserID=="" || input.Secret=="" || len(input.Password)<12 || len(input.Password)>1024 {
		writeJSON(w,400,map[string]string{"error":"A valid recovery link and a password of at least 12 characters are required"});return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second)
	defer cancel()
	body:=map[string]string{"userId":input.UserID,"secret":input.Secret,"password":input.Password,"passwordAgain":input.Password}
	_,err:=a.appwriteRequest(ctx,http.MethodPut,"/account/recovery","",body,nil)
	if err!=nil { writeJSON(w,400,map[string]string{"error":"Password could not be updated. The recovery link may have expired."});return }
	writeJSON(w,200,map[string]string{"message":"Password updated. You can now sign in."})
}

func (a *app) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s,err:=a.readSession(r)
	if err==nil {
		ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second)
		_,_ = a.appwriteRequest(ctx,http.MethodDelete,"/account/sessions/current",s.Secret,nil,nil)
		cancel()
	}
	a.clearSession(w)
	writeJSON(w,200,map[string]bool{"ok":true})
}

func (a *app) requireStaff(w http.ResponseWriter,r *http.Request,roles ...string) (session,authUser,bool) {
	s,user,ok:=a.currentUser(w,r)
	if !ok { writeJSON(w,401,map[string]string{"error":"Sign in required"});return session{},authUser{},false }
	role:=staffRole(user)
	if role=="" { writeJSON(w,403,map[string]string{"error":"Your account is waiting for owner approval"});return session{},authUser{},false }
	if len(roles)>0 {
		allowed:=false
		for _,candidate:=range roles { if role==candidate { allowed=true;break } }
		if !allowed { writeJSON(w,403,map[string]string{"error":"This action requires owner privileges"});return session{},authUser{},false }
	}
	return s,user,true
}

func randomAppwriteUserID() (string,error) {
	raw:=make([]byte,16)
	if _,err:=rand.Read(raw);err!=nil{return "",err}
	encoded:=hex.EncodeToString(raw)
	return encoded[:8]+"-"+encoded[8:12]+"-"+encoded[12:16]+"-"+encoded[16:20]+"-"+encoded[20:],nil
}

func (a *app) handleInvite(w http.ResponseWriter,r *http.Request) {
	s,actor,ok:=a.requireStaff(w,r,"owner");if !ok{return}
	if !a.requireRecentStepUp(w,s){return}
	var input inviteInput
	if !decodeBody(w,r,&input){return}
	input.Email=strings.TrimSpace(input.Email)
	switch input.Role {case "administrator","engineer","operations","support","moderator":default:writeJSON(w,400,map[string]string{"error":"Unsupported staff role"});return}
	if input.Email==""||len(input.Email)>320||!strings.Contains(input.Email,"@"){writeJSON(w,400,map[string]string{"error":"Enter a valid email address"});return}
	userID,err:=randomAppwriteUserID()
	if err!=nil { writeJSON(w,500,map[string]string{"error":"Could not create invitation"});return }
	randomPassword:=make([]byte,32)
	if _,err:=rand.Read(randomPassword);err!=nil { writeJSON(w,500,map[string]string{"error":"Could not create invitation"});return }
	ctx,cancel:=context.WithTimeout(r.Context(),10*time.Second)
	defer cancel()
	var invited authUser
	_,err=a.appwriteAdminRequest(ctx,http.MethodPost,"/users",map[string]any{
		"userId":userID,"email":input.Email,"password":base64.RawURLEncoding.EncodeToString(randomPassword),"name":input.Email,
	},&invited)
	if err!=nil||invited.ID=="" { writeJSON(w,400,map[string]string{"error":"Invitation failed. Verify Appwrite user-management permissions and whether this email already has an account."});return }
	tx,err:=a.database.Begin(ctx)
	if err!=nil {
		_,_ = a.appwriteAdminRequest(ctx,http.MethodDelete,"/users/"+url.PathEscape(invited.ID),nil,nil)
		writeJSON(w,503,map[string]string{"error":"Staff role database is unavailable"});return
	}
	defer func(){_ = tx.Rollback(context.Background())}()
	if _,err=tx.Exec(ctx,`INSERT INTO testagram_auth.staff_roles(appwrite_user_id,role,created_by_appwrite_user_id)
		VALUES($1,$2,$3) ON CONFLICT(appwrite_user_id) DO UPDATE
		SET role=EXCLUDED.role,created_by_appwrite_user_id=EXCLUDED.created_by_appwrite_user_id,updated_at=now(),access_revoked_at=NULL`,
		invited.ID,input.Role,actor.ID);err!=nil {
		_,_ = a.appwriteAdminRequest(ctx,http.MethodDelete,"/users/"+url.PathEscape(invited.ID),nil,nil)
		writeJSON(w,503,map[string]string{"error":"Could not assign the staff role"});return
	}
	if _,err=tx.Exec(ctx,`INSERT INTO testagram_auth.staff_role_audit(actor_appwrite_user_id,target_appwrite_user_id,action,role)
		VALUES($1,$2,'invited',$3)`,actor.ID,invited.ID,input.Role);err!=nil {
		_,_ = a.appwriteAdminRequest(ctx,http.MethodDelete,"/users/"+url.PathEscape(invited.ID),nil,nil)
		writeJSON(w,503,map[string]string{"error":"Could not audit the staff invitation"});return
	}
	if err=tx.Commit(ctx);err!=nil {
		log.Printf("staff invitation commit outcome requires reconciliation actor=%s target_user=%s",actor.ID,invited.ID)
		writeJSON(w,503,map[string]string{"error":"Could not confirm the staff invitation commit. Check the staff directory before retrying."});return
	}
	_,recoveryErr:=a.appwriteRequest(ctx,http.MethodPost,"/account/recovery","",map[string]string{"email":input.Email,"url":a.publicURL+"/reset-password"},nil)
	if recoveryErr!=nil {
		log.Printf("staff invitation recovery email failed actor=%s target_user=%s role=%s",actor.ID,invited.ID,input.Role)
		writeJSON(w,201,map[string]any{"ok":true,"user_id":invited.ID,"role":input.Role,"message":"Staff account and role were created. The recipient can use password recovery to set their password."})
		return
	}
	log.Printf("staff invite created actor=%s target_user=%s role=%s",actor.ID,invited.ID,input.Role)
	writeJSON(w,201,map[string]any{"ok":true,"user_id":invited.ID,"role":input.Role,"message":"Invitation sent. The recipient must set a password before accessing the operations workspace."})
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
