package freshapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

type Server struct {
	db             *appwritedb.Client
	endpoint       string
	projectID      string
	httpClient     *http.Client
	storage        MediaService
	allowedOrigins map[string]struct{}
}

type MediaService interface {
	ReserveUpload(context.Context, freshstorage.ReserveInput) (freshstorage.UploadReservation, error)
	CompleteUpload(context.Context, string, string) error
	DownloadURL(context.Context, string, string) (string, error)
}

func (s *Server) WithStorage(storage MediaService) *Server {
	s.storage = storage
	return s
}

func (s *Server) WithAllowedOrigins(origins []string) error {
	allowed := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLocalOriginHost(u.Hostname()))) {
			return errors.New("fresh API CORS origins must be HTTPS origins (HTTP only on localhost)")
		}
		allowed[u.Scheme+"://"+u.Host] = struct{}{}
	}
	s.allowedOrigins = allowed
	return nil
}

func isLocalOriginHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

func New(db *appwritedb.Client, endpoint, projectID string, client *http.Client) (*Server, error) {
	if db == nil || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(projectID) == "" {
		return nil, errors.New("fresh API requires Appwrite Database and Auth configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Server{db: db, endpoint: strings.TrimRight(endpoint, "/"), projectID: projectID, httpClient: &copyClient, allowedOrigins: make(map[string]struct{})}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "identity": "appwrite", "database": "appwrite"})
	})
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("PUT /v1/me/profile", s.updateProfile)
	if s.storage != nil {
		mux.HandleFunc("POST /v1/media/uploads/reservations", s.reserveMediaUpload)
		mux.HandleFunc("POST /v1/media/uploads/{reservationID}/complete", s.completeMediaUpload)
		mux.HandleFunc("GET /v1/media/{objectID}/download", s.downloadMedia)
	}
	return securityHeaders(s.cors(mux))
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			allowed := false
			if _, ok := s.allowedOrigins[origin]; ok {
				allowed = true
			}
			if parsed, err := url.Parse(origin); err == nil && parsed.Host == r.Host {
				allowed = true
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "origin is not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			if origin == "" {
				writeError(w, http.StatusForbidden, "CORS preflight origin is required")
				return
			}
			requestedMethod := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
			if requestedMethod != "GET" && requestedMethod != "POST" && requestedMethod != "PUT" {
				writeError(w, http.StatusForbidden, "CORS method is not allowed")
				return
			}
			allowedHeaders := map[string]bool{"authorization": true, "content-type": true, "idempotency-key": true, "accept": true}
			for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				if header = strings.ToLower(strings.TrimSpace(header)); header != "" && !allowedHeaders[header] {
					writeError(w, http.StatusForbidden, "CORS request header is not allowed")
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, Accept")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) reserveMediaUpload(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		Bytes       int64  `json:"bytes"`
		ContentType string `json:"content_type"`
		SHA256      string `json:"sha256"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload reservation payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "upload reservation must contain one JSON object")
		return
	}
	result, err := s.storage.ReserveUpload(r.Context(), freshstorage.ReserveInput{OwnerID: a.ID, IdempotencyKey: r.Header.Get("Idempotency-Key"), Bytes: input.Bytes, ContentType: input.ContentType, SHA256: input.SHA256})
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) completeMediaUpload(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	reservationID := r.PathValue("reservationID")
	// Finalization streams and hashes large objects. Extend only this authenticated
	// route's write deadline; ordinary API requests retain a short server timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Hour))
	if err := s.storage.CompleteUpload(r.Context(), a.ID, reservationID); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "protected"})
}

func (s *Server) downloadMedia(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	url, err := s.storage.DownloadURL(r.Context(), a.ID, r.PathValue("objectID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "expires_in_seconds": 120, "visibility": "private"})
}

func writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, freshstorage.ErrQuotaExceeded):
		writeError(w, http.StatusInsufficientStorage, "storage quota exceeded")
	case errors.Is(err, freshstorage.ErrStorageConflict):
		writeError(w, http.StatusConflict, "storage operation conflicted; retry the same idempotency key")
	case errors.Is(err, freshstorage.ErrReservationNotFound):
		writeError(w, http.StatusNotFound, "upload reservation not found")
	case errors.Is(err, freshstorage.ErrReservationExpired):
		writeError(w, http.StatusGone, "upload reservation expired")
	case errors.Is(err, freshstorage.ErrNotOwner), errors.Is(err, freshstorage.ErrNotReady):
		writeError(w, http.StatusForbidden, "media object is unavailable")
	case errors.Is(err, freshstorage.ErrReservationConflict):
		writeError(w, http.StatusConflict, "idempotency key was already used for a different upload")
	default:
		writeError(w, http.StatusServiceUnavailable, "media storage operation is unavailable or awaiting replica recovery")
	}
}

type account struct {
	ID     string `json:"$id"`
	Status bool   `json:"status"`
}

func (s *Server) authenticate(r *http.Request) (account, int, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return account{}, 401, errors.New("Appwrite session token required")
	}
	jwt := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if jwt == "" || len(jwt) > 8192 || strings.ContainsAny(jwt, "\r\n") {
		return account{}, 401, errors.New("invalid Appwrite session token")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint+"/account", nil)
	if err != nil {
		return account{}, 503, errors.New("Appwrite authentication unavailable")
	}
	req.Header.Set("X-Appwrite-Project", s.projectID)
	req.Header.Set("X-Appwrite-JWT", jwt)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return account{}, 503, errors.New("Appwrite authentication unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return account{}, 401, errors.New("Appwrite session rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return account{}, 503, errors.New("Appwrite authentication unavailable")
	}
	var a account
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a); err != nil || !validUserID(a.ID) || !a.Status {
		return account{}, 401, errors.New("Appwrite account is invalid or disabled")
	}
	return a, 200, nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	raw, err := s.db.GetDocument(r.Context(), "profiles", a.ID)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "profile not found")
		} else {
			writeError(w, http.StatusServiceUnavailable, "profile service unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		DisplayName string `json:"display_name"`
		Handle      string `json:"handle"`
		Bio         string `json:"bio"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, 400, "invalid profile payload")
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Handle = strings.ToLower(strings.TrimSpace(input.Handle))
	input.Bio = strings.TrimSpace(input.Bio)
	if input.DisplayName == "" || len(input.DisplayName) > 80 || len(input.Handle) < 3 || len(input.Handle) > 30 || !validHandle(input.Handle) || len(input.Bio) > 280 {
		writeError(w, 400, "profile fields are invalid")
		return
	}
	data := map[string]any{"user_id": a.ID, "display_name": input.DisplayName, "handle": input.Handle, "bio": input.Bio, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
	raw, err := s.db.CreateDocument(r.Context(), "profiles", a.ID, data, nil)
	if err != nil {
		// A conflict means this profile already exists; update only the
		// authenticated user's deterministic profile document.
		raw, err = s.db.UpdateDocument(r.Context(), "profiles", a.ID, data)
	}
	if err != nil {
		writeError(w, 503, "profile could not be saved")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

func validUserID(v string) bool {
	if v == "" || len(v) > 36 || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}
func validHandle(v string) bool {
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]string{"message": message}})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
