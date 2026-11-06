package freshapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

type Server struct {
	db         *appwritedb.Client
	endpoint   string
	projectID  string
	httpClient *http.Client
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
	return &Server{db: db, endpoint: strings.TrimRight(endpoint, "/"), projectID: projectID, httpClient: &copyClient}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "identity": "appwrite", "database": "appwrite"})
	})
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("PUT /v1/me/profile", s.updateProfile)
	return securityHeaders(mux)
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
