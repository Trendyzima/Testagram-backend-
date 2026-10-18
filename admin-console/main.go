package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"embed"
	"io/fs"
)

//go:embed web/*
var webFiles embed.FS

type app struct {
	supabaseURL string
	publishableKey string
	serviceRoleKey string
	publicURL string
	publicOrigin string
	prometheusURL string
	apiHealthURL string
	grafanaURL string
	cookieSecure bool
	aead cipher.AEAD
	client *http.Client
}

type session struct {
	AccessToken string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt int64 `json:"expires_at"`
}

type authUser struct {
	ID string `json:"id"`
	Email string `json:"email"`
	AppMetadata map[string]any `json:"app_metadata"`
	UserMetadata map[string]any `json:"user_metadata"`
}

type authPayload struct {
	AccessToken string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn int64 `json:"expires_in"`
	User authUser `json:"user"`
}

type authInput struct {
	Email string `json:"email"`
	Password string `json:"password"`
	AccessToken string `json:"access_token"`
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
	if err != nil { return nil, err }
	aead, err := cipher.NewGCM(block)
	if err != nil { return nil, err }
	supabaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SUPABASE_URL")), "/")
	publishable := strings.TrimSpace(os.Getenv("SUPABASE_PUBLISHABLE_KEY"))
	if supabaseURL == "" || publishable == "" {
		return nil, errors.New("SUPABASE_URL and SUPABASE_PUBLISHABLE_KEY are required")
	}
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_URL")), "/")
	parsed, err := url.Parse(publicURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("ADMIN_PUBLIC_URL must be an absolute http(s) URL")
	}
	secure := !strings.EqualFold(env("ADMIN_COOKIE_SECURE", "true"), "false")
	if parsed.Scheme == "https" && !secure { return nil, errors.New("ADMIN_COOKIE_SECURE cannot be false when ADMIN_PUBLIC_URL uses HTTPS") }
	return &app{
		supabaseURL: supabaseURL,
		publishableKey: publishable,
		serviceRoleKey: strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY")),
		publicURL: publicURL,
		publicOrigin: parsed.Scheme + "://" + parsed.Host,
		prometheusURL: strings.TrimRight(env("PROMETHEUS_URL", "http://prometheus:9090"), "/"),
		apiHealthURL: env("API_HEALTH_URL", "http://api:8080/readyz"),
		grafanaURL: env("GRAFANA_URL", "http://127.0.0.1:3000"),
		cookieSecure: secure,
		aead: aead,
		client: &http.Client{Timeout: 3 * time.Second},
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
		if r.Method == http.MethodPost && !a.sameOrigin(r) {
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

func (a *app) authRequest(ctx context.Context, method, path, bearer string, body any, dst any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body); if err != nil { return 0, err }
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.supabaseURL+path, reader)
	if err != nil { return 0, err }
	req.Header.Set("apikey", a.publishableKey)
	if bearer != "" { req.Header.Set("Authorization", "Bearer "+bearer) }
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := a.client.Do(req)
	if err != nil { return 0, err }
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil { return resp.StatusCode, err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return resp.StatusCode, fmt.Errorf("auth provider rejected request") }
	if dst != nil {
		if err := json.Unmarshal(raw, dst); err != nil { return resp.StatusCode, err }
	}
	return resp.StatusCode, nil
}

func (a *app) adminAuthRequest(ctx context.Context, method, path string, body any, dst any) (int, error) {
	if a.serviceRoleKey == "" { return 0, errors.New("server admin key is not configured") }
	raw, err := json.Marshal(body); if err != nil { return 0, err }
	req, err := http.NewRequestWithContext(ctx, method, a.supabaseURL+path, bytes.NewReader(raw)); if err != nil { return 0, err }
	req.Header.Set("apikey", a.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+a.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req); if err != nil { return 0, err }
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); if err != nil { return resp.StatusCode, err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return resp.StatusCode, errors.New("auth admin operation failed") }
	if dst != nil { if err := json.Unmarshal(data, dst); err != nil { return resp.StatusCode, err } }
	return resp.StatusCode, nil
}

func (a *app) setSession(w http.ResponseWriter, s session) error {
	raw, err := json.Marshal(s); if err != nil { return err }
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil { return err }
	sealed := a.aead.Seal(nonce, nonce, raw, []byte("testagram-staff-session-v1"))
	http.SetCookie(w, &http.Cookie{Name:"testagram-staff", Value:hex.EncodeToString(sealed), Path:"/", HttpOnly:true, Secure:a.cookieSecure, SameSite:http.SameSiteStrictMode, MaxAge:60*60*24*7})
	return nil
}

func (a *app) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name:"__Host-testagram-staff", Value:"", Path:"/", HttpOnly:true, Secure:a.cookieSecure, SameSite:http.SameSiteStrictMode, MaxAge:-1})
}

func (a *app) readSession(r *http.Request) (session, error) {
	cookie, err := r.Cookie("__Host-testagram-staff"); if err != nil { return session{}, err }
	sealed, err := hex.DecodeString(cookie.Value); if err != nil { return session{}, err }
	n := a.aead.NonceSize()
	if len(sealed) <= n { return session{}, errors.New("invalid session") }
	plain, err := a.aead.Open(nil, sealed[:n], sealed[n:], []byte("testagram-staff-session-v1")); if err != nil { return session{}, err }
	var s session
	if err := json.Unmarshal(plain, &s); err != nil || s.AccessToken == "" || s.RefreshToken == "" { return session{}, errors.New("invalid session") }
	return s, nil
}

func (a *app) currentUser(w http.ResponseWriter, r *http.Request) (session, authUser, bool) {
	s, err := a.readSession(r)
	if err != nil { a.clearSession(w); return session{}, authUser{}, false }
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second); defer cancel()
	var user authUser
	_, err = a.authRequest(ctx, http.MethodGet, "/auth/v1/user", s.AccessToken, nil, &user)
	if err != nil {
		var refreshed authPayload
		_, refreshErr := a.authRequest(ctx, http.MethodPost, "/auth/v1/token?grant_type=refresh_token", "", map[string]string{"refresh_token":s.RefreshToken}, &refreshed)
		if refreshErr == nil && refreshed.AccessToken != "" && refreshed.RefreshToken != "" {
			s.AccessToken, s.RefreshToken = refreshed.AccessToken, refreshed.RefreshToken
			s.ExpiresAt = time.Now().Unix() + refreshed.ExpiresIn
			_, err = a.authRequest(ctx, http.MethodGet, "/auth/v1/user", s.AccessToken, nil, &user)
			if err == nil { if cookieErr := a.setSession(w, s); cookieErr != nil { log.Printf("refresh session cookie failed: %v", cookieErr) } }
		}
	}
	if err != nil { a.clearSession(w); return session{}, authUser{}, false }
	return s, user, true
}

func staffRole(user authUser) string {
	role, _ := user.AppMetadata["staff_role"].(string)
	switch role {
	case "owner", "administrator", "engineer", "operations", "support", "moderator": return role
	default: return ""
	}
}

func (a *app) handleSession(w http.ResponseWriter, r *http.Request) {
	_, user, ok := a.currentUser(w, r)
	if !ok { writeJSON(w, 200, map[string]any{"authenticated":false,"staff":false}); return }
	role := staffRole(user)
	writeJSON(w, 200, map[string]any{"authenticated":true,"staff":role!="","email":user.Email,"role":role,"user_id":user.ID})
}

func (a *app) handleSignIn(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	input.Email = strings.TrimSpace(input.Email)
	if input.Email == "" || len(input.Email)>320 || len(input.Password)<1 || len(input.Password)>1024 { writeJSON(w,400,map[string]string{"error":"Enter a valid email and password"}); return }
	var result authPayload
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	_,err:=a.authRequest(ctx,http.MethodPost,"/auth/v1/token?grant_type=password","",map[string]string{"email":input.Email,"password":input.Password},&result)
	if err!=nil || result.AccessToken=="" || result.RefreshToken=="" { writeJSON(w,401,map[string]string{"error":"Sign-in failed. Check your credentials or email confirmation."}); return }
	s:=session{AccessToken:result.AccessToken,RefreshToken:result.RefreshToken,ExpiresAt:time.Now().Unix()+result.ExpiresIn}
	if err:=a.setSession(w,s);err!=nil { writeJSON(w,500,map[string]string{"error":"Unable to create secure session"});return }
	writeJSON(w,200,map[string]any{"ok":true})
}

func (a *app) handleSignUp(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	input.Email=strings.TrimSpace(input.Email)
	if input.Email==""||len(input.Email)>320||len(input.Password)<12||len(input.Password)>1024 { writeJSON(w,400,map[string]string{"error":"Use a valid email and a password of at least 12 characters"});return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	_,err:=a.authRequest(ctx,http.MethodPost,"/auth/v1/signup","",map[string]any{"email":input.Email,"password":input.Password,"data":map[string]string{"account_type":"staff_candidate"}},nil)
	if err!=nil { writeJSON(w,400,map[string]string{"error":"Account creation could not be completed. Check the email or try again later."});return }
	writeJSON(w,200,map[string]any{"ok":true,"message":"Account request created. Verify your email if prompted; the owner must assign a staff role before you can access the console."})
}

func (a *app) handleRecover(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	input.Email=strings.TrimSpace(input.Email)
	if input.Email==""||len(input.Email)>320 { writeJSON(w,400,map[string]string{"error":"Enter a valid email address"});return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	redirect:=a.publicURL+"/reset-password"
	_,_ = a.authRequest(ctx,http.MethodPost,"/auth/v1/recover?redirect_to="+url.QueryEscape(redirect),"",map[string]string{"email":input.Email},nil)
	// Always return the same response so callers cannot enumerate accounts.
	writeJSON(w,200,map[string]string{"message":"If the account exists, a password-reset email will be sent."})
}

func (a *app) handleUpdatePassword(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeBody(w,r,&input) { return }
	if len(input.AccessToken)<20 || len(input.Password)<12 || len(input.Password)>1024 { writeJSON(w,400,map[string]string{"error":"A valid reset link and a password of at least 12 characters are required"});return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	_,err:=a.authRequest(ctx,http.MethodPut,"/auth/v1/user",input.AccessToken,map[string]string{"password":input.Password},nil)
	if err!=nil { writeJSON(w,400,map[string]string{"error":"Password could not be updated. The reset link may have expired."});return }
	writeJSON(w,200,map[string]string{"message":"Password updated. You can now sign in."})
}

func (a *app) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s,err:=a.readSession(r)
	if err==nil {
		ctx,cancel:=context.WithTimeout(r.Context(),2*time.Second)
		_,_ = a.authRequest(ctx,http.MethodPost,"/auth/v1/logout",s.AccessToken,nil,nil)
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

func (a *app) handleInvite(w http.ResponseWriter,r *http.Request) {
	_,actor,ok:=a.requireStaff(w,r,"owner");if !ok{return}
	var input inviteInput
	if !decodeBody(w,r,&input){return}
	input.Email=strings.TrimSpace(input.Email)
	switch input.Role {case "administrator","engineer","operations","support","moderator":default:writeJSON(w,400,map[string]string{"error":"Unsupported staff role"});return}
	if input.Email==""||len(input.Email)>320||!strings.Contains(input.Email,"@"){writeJSON(w,400,map[string]string{"error":"Enter a valid email address"});return}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	redirect:=a.publicURL+"/"
	var invited authUser
	_,err:=a.adminAuthRequest(ctx,http.MethodPost,"/auth/v1/invite?redirect_to="+url.QueryEscape(redirect),map[string]any{"email":input.Email},&invited)
	if err!=nil||invited.ID=="" { writeJSON(w,400,map[string]string{"error":"Invitation failed. Verify Auth admin configuration and whether this email already has an account."});return }
	appMetadata:=map[string]any{};for key,value:=range invited.AppMetadata{appMetadata[key]=value};appMetadata["staff_role"]=input.Role
	_,err=a.adminAuthRequest(ctx,http.MethodPut,"/auth/v1/admin/users/"+url.PathEscape(invited.ID),map[string]any{"app_metadata":appMetadata},nil)
	if err!=nil { log.Printf("staff invite role assignment failed actor=%s target_user=%s role=%s",actor.ID,invited.ID,input.Role);writeJSON(w,502,map[string]string{"error":"Invite was created but staff role assignment failed. Revoke or repair this account in the Supabase Auth admin panel before use."});return }
	log.Printf("staff invite created actor=%s target_user=%s role=%s",actor.ID,invited.ID,input.Role)
	writeJSON(w,201,map[string]any{"ok":true,"message":"Invitation sent. The recipient must complete email verification and set a password."})
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
	if result.Status!="success"||len(result.Data.Result)==0||len(result.Data.Result[0].Value)<2{return 0,nil}
	value,ok:=result.Data.Result[0].Value[1].(string);if !ok{return 0,nil}
	var n float64
	if _,err:=fmt.Sscanf(value,"%f",&n);err!=nil{return 0,err}
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
		"queue_pending":`sum(testagram_platform_queue_pending)`,
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
	addr:=env("ADMIN_ADDR",":8090")
	srv:=&http.Server{Addr:addr,Handler:a.routes(),ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,WriteTimeout:15*time.Second,IdleTimeout:60*time.Second,MaxHeaderBytes:1<<20}
	log.Printf("Testagram staff console listening on %s",addr)
	log.Fatal(srv.ListenAndServe())
}
