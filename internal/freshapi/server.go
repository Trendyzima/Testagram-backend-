package freshapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshfeed"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

type Server struct {
	db             *appwritedb.Client
	endpoint       string
	projectID      string
	httpClient     *http.Client
	storage        MediaService
	feed           HomeFeedService
	allowedOrigins map[string]struct{}
	internalToken  string
	moderation     ContentModeration
}

type HomeFeedService interface {
	Home(context.Context, string, int, string) (freshfeed.Result, error)
	Ready(context.Context) error
}

type MediaService interface {
	ReserveUpload(context.Context, freshstorage.ReserveInput) (freshstorage.UploadReservation, error)
	CompleteUpload(context.Context, string, string) error
	DownloadURL(context.Context, string, string) (string, error)
	RestoreArchive(context.Context, string, string) error
}

func (s *Server) WithFeed(feed HomeFeedService) *Server {
	s.feed = feed
	return s
}

func (s *Server) WithModeration(moderation ContentModeration) *Server {
	s.moderation = moderation
	return s
}

func (s *Server) WithInternalToken(token string) error {
	token = strings.TrimSpace(token)
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return errors.New("federation internal token must contain at least 32 characters")
	}
	s.internalToken = token
	return nil
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
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("PUT /v1/me/profile", s.updateProfile)
	mux.HandleFunc("GET /v1/me/federation", s.getFederationPreference)
	mux.HandleFunc("PUT /v1/me/federation", s.updateFederationPreference)
	mux.HandleFunc("GET /v1/feed/home", s.homeFeed)
	mux.HandleFunc("POST /v1/posts", s.createPost)
	mux.HandleFunc("POST /v1/moderation/check", s.moderateContent)
	if s.internalToken != "" {
		mux.HandleFunc("GET /internal/v1/federation/outbox", s.federationOutbox)
		mux.HandleFunc("GET /internal/v1/federation/actors/{handle}", s.federationActor)
		mux.HandleFunc("GET /internal/v1/federation/actors/by-id/{userID}", s.federationActorByID)
	}
	if s.storage != nil {
		mux.HandleFunc("POST /v1/media/uploads/reservations", s.reserveMediaUpload)
		mux.HandleFunc("POST /v1/media/uploads/{reservationID}/complete", s.completeMediaUpload)
		mux.HandleFunc("GET /v1/media/{objectID}/download", s.downloadMedia)
		mux.HandleFunc("POST /v1/media/{objectID}/restore", s.restoreMedia)
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

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.feed == nil {
		writeError(w, http.StatusServiceUnavailable, "feed dependencies are not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.feed.Ready(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "feed dependencies are not ready")
		return
	}
	if _, err := s.db.ListDocumentsWithQueries(ctx, "backend_outbox", []string{
		mustQuery("equal", "event_type", "testagram.post.published"),
		mustQuery("equal", "federation_eligible", true),
		mustQuery("orderAsc", "created_at"),
		mustQuery("limit", "", 1),
	}, 1); err != nil {
		writeError(w, http.StatusServiceUnavailable, "transactional outbox is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identity": "appwrite", "database": "appwrite", "feed": "ready", "post_write": "ready", "outbox": "ready"})
}

func (s *Server) homeFeed(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if s.feed == nil {
		writeError(w, http.StatusServiceUnavailable, "feed service is not configured")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("before"))
	if freshfeed.ValidateCursor(cursor) != nil {
		writeError(w, http.StatusBadRequest, "invalid feed cursor")
		return
	}
	result, err := s.feed.Home(r.Context(), a.ID, limit, cursor)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "feed is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"data":  result,
		"error": nil,
	})
}

func (s *Server) requireFederationService(w http.ResponseWriter, r *http.Request) bool {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if !strings.HasPrefix(authorization, "Bearer ") || len(provided) != len(s.internalToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.internalToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "internal service authentication required")
		return false
	}
	return true
}

// federationActor exposes only explicitly federation-enabled local profiles.
// Appwrite Auth status is checked independently so suspended/deleted accounts
// cannot remain discoverable merely because their profile document exists.
func (s *Server) federationActor(w http.ResponseWriter, r *http.Request) {
	if !s.requireFederationService(w, r) {
		return
	}
	handle := strings.ToLower(strings.TrimSpace(r.PathValue("handle")))
	if !validHandle(handle) || len(handle) < 3 {
		writeError(w, http.StatusNotFound, "local actor not found")
		return
	}
	documents, err := s.db.ListDocumentsWithQueries(r.Context(), "profiles", []string{
		mustQuery("equal", "handle", handle),
		mustQuery("limit", "", 2),
	}, 2)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "local actor lookup is unavailable")
		return
	}
	if len(documents) != 1 {
		writeError(w, http.StatusNotFound, "local actor not found")
		return
	}
	var profile map[string]any
	if err := json.Unmarshal(documents[0], &profile); err != nil {
		writeError(w, http.StatusServiceUnavailable, "local actor profile is invalid")
		return
	}
	s.writeFederationActorProfile(w, r, profile, handle)
}

func (s *Server) federationActorByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireFederationService(w, r) { return }
	userID := strings.TrimSpace(r.PathValue("userID"))
	if !validUserID(userID) {
		writeError(w, http.StatusNotFound, "local actor not found")
		return
	}
	raw, err := s.db.GetDocument(r.Context(), "profiles", userID)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "local actor not found")
		} else {
			writeError(w, http.StatusServiceUnavailable, "local actor lookup is unavailable")
		}
		return
	}
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		writeError(w, http.StatusServiceUnavailable, "local actor profile is invalid")
		return
	}
	handle, _ := profile["handle"].(string)
	s.writeFederationActorProfile(w, r, profile, handle)
}

func (s *Server) writeFederationActorProfile(w http.ResponseWriter, r *http.Request, profile map[string]any, expectedHandle string) {
	if enabled, ok := profile["federation_enabled"].(bool); !ok || !enabled {
		writeError(w, http.StatusNotFound, "local actor not found")
		return
	}
	userID, ok := profile["user_id"].(string)
	if !ok || !validUserID(userID) {
		writeError(w, http.StatusServiceUnavailable, "local actor identity is invalid")
		return
	}
	handle, _ := profile["handle"].(string)
	if !validHandle(handle) || len(handle) < 3 || (expectedHandle != "" && handle != expectedHandle) {
		writeError(w, http.StatusServiceUnavailable, "local actor handle is invalid")
		return
	}
	user, err := s.db.GetUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "local actor account status is unavailable")
		return
	}
	if !user.Status {
		writeError(w, http.StatusNotFound, "local actor not found")
		return
	}
	displayName, _ := profile["display_name"].(string)
	bio, _ := profile["bio"].(string)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userID, "handle": handle,
		"display_name": strings.TrimSpace(displayName),
		"summary": strings.TrimSpace(bio),
		"federation_enabled": true,
	})
}

func (s *Server) getFederationPreference(w http.ResponseWriter, r *http.Request) {
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
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		writeError(w, http.StatusServiceUnavailable, "profile is invalid")
		return
	}
	enabled, _ := profile["federation_enabled"].(bool)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"enabled": enabled}, "error": nil})
}

func (s *Server) updateFederationPreference(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled boolean is required")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "federation preference must contain one JSON object")
		return
	}
	if _, err := s.db.GetDocument(r.Context(), "profiles", a.ID); err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "profile not found")
		} else {
			writeError(w, http.StatusServiceUnavailable, "profile service unavailable")
		}
		return
	}
	_, err = s.db.UpdateDocument(r.Context(), "profiles", a.ID, map[string]any{
		"federation_enabled": *input.Enabled,
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "federation preference could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"enabled": *input.Enabled}, "error": nil})
}

func (s *Server) federationOutbox(w http.ResponseWriter, r *http.Request) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if !strings.HasPrefix(authorization, "Bearer ") || len(provided) != len(s.internalToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.internalToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "internal service authentication required")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 99 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 99")
			return
		}
		limit = parsed
	}
	queries := []string{
		mustQuery("equal", "event_type", "testagram.post.published"),
		mustQuery("equal", "federation_eligible", true),
		mustQuery("orderAsc", "created_at"),
		mustQuery("limit", "", limit+1),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || !validOutboxDocumentID(string(decoded)) {
			writeError(w, http.StatusBadRequest, "invalid outbox cursor")
			return
		}
		queries = append(queries, mustQuery("cursorAfter", "", string(decoded)))
	}
	documents, err := s.db.ListDocumentsWithQueries(r.Context(), "backend_outbox", queries, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "federation outbox is temporarily unavailable")
		return
	}
	hasMore := len(documents) > limit
	if hasMore {
		documents = documents[:limit]
	}
	var nextCursor string
	if hasMore && len(documents) > 0 {
		var last struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(documents[len(documents)-1], &last); err != nil || !validOutboxDocumentID(last.ID) {
			writeError(w, http.StatusServiceUnavailable, "federation outbox returned an invalid event")
			return
		}
		nextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"events": documents, "next_cursor": nextCursor, "has_more": hasMore}, "error": nil})
}

func validOutboxDocumentID(value string) bool {
	if len(value) < 1 || len(value) > 36 || !((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= '0' && value[0] <= '9')) {
		return false
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.' {
			continue
		}
		return false
	}
	return value != "unique()"
}

func mustQuery(method, column string, values ...any) string {
	query := map[string]any{"method": method, "values": values}
	if column != "" {
		query["column"] = column
	}
	encoded, _ := json.Marshal(query)
	return string(encoded)
}

func (s *Server) moderateContent(w http.ResponseWriter, r *http.Request) {
	if _, status, err := s.authenticate(r); err != nil {
		writeError(w, status, err.Error())
		return
	}
	if s.moderation == nil {
		writeError(w, http.StatusServiceUnavailable, "content moderation is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 24<<10)
	var input struct {
		Content string `json:"content"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid moderation payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "moderation payload must contain one JSON object")
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if !utf8.ValidString(input.Content) || input.Content == "" || utf8.RuneCountInString(input.Content) > 5000 {
		writeError(w, http.StatusBadRequest, "moderation content must contain 1-5000 valid Unicode characters")
		return
	}
	result, err := s.moderation.Analyze(r.Context(), input.Content)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "content moderation is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": result, "error": nil})
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n") {
		writeError(w, http.StatusBadRequest, "an 8-128 character Idempotency-Key is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		Content    string `json:"content"`
		Visibility string `json:"visibility"`
		Sensitive  bool   `json:"sensitive"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid post payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "post payload must contain one JSON object")
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if !utf8.ValidString(input.Content) || input.Content == "" || utf8.RuneCountInString(input.Content) > 5000 {
		writeError(w, http.StatusBadRequest, "post content must contain between 1 and 5000 valid Unicode characters")
		return
	}
	switch input.Visibility {
	case "public", "followers", "private":
	default:
		writeError(w, http.StatusBadRequest, "visibility must be public, followers, or private")
		return
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid post payload")
		return
	}
	requestHash := sha256.Sum256(canonical)
	keyHash := sha256.Sum256([]byte(idempotencyKey))
	postHash := sha256.Sum256([]byte(a.ID + "\x00" + idempotencyKey))
	postID := "p_" + hex.EncodeToString(postHash[:16])

	// Resolve an already-committed idempotent retry before calling an external
	// moderator. A provider outage must not turn a successful earlier write into
	// an unrecoverable client retry.
	existing, readErr := s.db.GetDocument(r.Context(), "posts", postID)
	if readErr == nil {
		var saved map[string]any
		if json.Unmarshal(existing, &saved) != nil {
			writeError(w, http.StatusServiceUnavailable, "existing post could not be read")
			return
		}
		if saved["author_id"] != a.ID || saved["idempotency_key_hash"] != hex.EncodeToString(keyHash[:]) || saved["request_hash"] != hex.EncodeToString(requestHash[:]) {
			writeError(w, http.StatusConflict, "Idempotency-Key was already used for a different post")
			return
		}
		public, sanitizeErr := publicPostDocument(existing)
		if sanitizeErr != nil {
			writeError(w, http.StatusServiceUnavailable, "existing post could not be read")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": public, "error": nil})
		return
	}
	var readAPIError appwritedb.HTTPError
	if !errors.As(readErr, &readAPIError) || readAPIError.StatusCode != http.StatusNotFound {
		writeError(w, http.StatusServiceUnavailable, "post idempotency lookup is unavailable")
		return
	}
	if s.moderation == nil {
		writeError(w, http.StatusServiceUnavailable, "content moderation is not configured")
		return
	}
	moderation, moderationErr := s.moderation.Analyze(r.Context(), input.Content)
	if moderationErr != nil {
		writeError(w, http.StatusServiceUnavailable, "content moderation is temporarily unavailable")
		return
	}
	if moderation.Action != "pass" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "data": moderation, "error": map[string]string{"code": "CONTENT_REQUIRES_REVIEW", "message": moderation.Reason}})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	data := map[string]any{
		"author_id":            a.ID,
		"body":                 input.Content,
		"visibility":           input.Visibility,
		"status":               "published",
		"sensitive":            input.Sensitive,
		"idempotency_key_hash": hex.EncodeToString(keyHash[:]),
		"request_hash":         hex.EncodeToString(requestHash[:]),
		"created_at":           now,
		"updated_at":           now,
	}
	tx, err := s.db.CreateTransaction(r.Context(), 120)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "post transaction could not be started")
		return
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	if err = s.db.CreateDocumentInTransaction(r.Context(), "posts", postID, data, tx); err != nil {
		rollback()
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			existing, readErr := s.db.GetDocument(r.Context(), "posts", postID)
			if readErr == nil {
				var saved map[string]any
				if json.Unmarshal(existing, &saved) == nil && saved["author_id"] == a.ID && saved["idempotency_key_hash"] == hex.EncodeToString(keyHash[:]) && saved["request_hash"] == hex.EncodeToString(requestHash[:]) {
					public, sanitizeErr := publicPostDocument(existing)
					if sanitizeErr != nil {
						writeError(w, http.StatusServiceUnavailable, "post could not be read")
						return
					}
					writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": public, "error": nil})
					return
				}
				writeError(w, http.StatusConflict, "Idempotency-Key was already used for a different post")
				return
			}
		}
		writeError(w, http.StatusServiceUnavailable, "post could not be saved")
		return
	}
	eventID := "evt_" + hex.EncodeToString(postHash[:16])
	federationEligible := false
	actorHandle, actorDisplayName := "", ""
	// Public visibility alone is not consent to federate. The profile must
	// explicitly opt in; lookup failures suppress federation without blocking
	// the user's local post.
	if input.Visibility == "public" && !input.Sensitive {
		if profileRaw, profileErr := s.db.GetDocument(r.Context(), "profiles", a.ID); profileErr == nil {
			var profile map[string]any
			if json.Unmarshal(profileRaw, &profile) == nil {
				enabled, _ := profile["federation_enabled"].(bool)
				actorHandle, _ = profile["handle"].(string)
				actorDisplayName, _ = profile["display_name"].(string)
				federationEligible = enabled && validHandle(actorHandle) && len(actorHandle) >= 3
			}
		}
	}
	event := map[string]any{
		"event_id": eventID, "event_type": "testagram.post.published", "schema_version": 1,
		"occurred_at": now, "actor_id": a.ID, "object_id": postID, "visibility": input.Visibility,
		"federation_eligible": federationEligible, "created_at": now,
		"payload": map[string]any{"id": postID, "author_id": a.ID, "actor_handle": actorHandle, "actor_display_name": actorDisplayName, "content": input.Content, "visibility": input.Visibility, "sensitive": input.Sensitive, "created_at": now},
	}
	if err = s.db.CreateDocumentInTransaction(r.Context(), "backend_outbox", eventID, event, tx); err != nil {
		rollback()
		writeError(w, http.StatusServiceUnavailable, "post event could not be recorded")
		return
	}
	if err = s.db.UpdateTransaction(r.Context(), tx, true); err != nil {
		writeError(w, http.StatusServiceUnavailable, "post transaction could not be committed")
		return
	}
	created := map[string]any{"$id": postID}
	for key, value := range data {
		created[key] = value
	}
	encoded, err := json.Marshal(created)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "post could not be encoded")
		return
	}
	public, sanitizeErr := publicPostDocument(encoded)
	if sanitizeErr != nil {
		writeError(w, http.StatusServiceUnavailable, "post could not be read")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": public, "error": nil})
}

func publicPostDocument(raw []byte) (json.RawMessage, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil || document == nil {
		return nil, errors.New("invalid Appwrite post document")
	}
	delete(document, "idempotency_key_hash")
	delete(document, "request_hash")
	encoded, err := json.Marshal(document)
	return json.RawMessage(encoded), err
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
		Scope       string `json:"scope"`
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
	result, err := s.storage.ReserveUpload(r.Context(), freshstorage.ReserveInput{OwnerID: a.ID, IdempotencyKey: r.Header.Get("Idempotency-Key"), Scope: input.Scope, Bytes: input.Bytes, ContentType: input.ContentType, SHA256: input.SHA256})
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

func (s *Server) restoreMedia(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if err = s.storage.RestoreArchive(r.Context(), a.ID, r.PathValue("objectID")); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "restored"})
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
	createData := map[string]any{}
	for key, value := range data { createData[key] = value }
	// Federation is opt-in. Set the default only on first profile creation;
	// ordinary profile edits must never revoke or silently re-enable consent.
	createData["federation_enabled"] = false
	raw, err := s.db.CreateDocument(r.Context(), "profiles", a.ID, createData, nil)
	if err != nil {
		// A conflict means this profile already exists; update only profile
		// fields and preserve its independent federation preference.
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
