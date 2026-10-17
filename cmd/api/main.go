package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const maxCapabilityBody = 64 << 10
const maxUpstreamBody = 2 << 20

type service struct {
	db       *pgxpool.Pool
	requests atomic.Uint64
	upstream *http.Client
}

type capabilityRequest struct {
	Capability string         `json:"capability"`
	Input      map[string]any `json:"input"`
}

func env(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.db == nil {
			http.Error(w, "database not configured", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte("# HELP testagram_http_requests_total Total HTTP requests handled by this process.\n# TYPE testagram_http_requests_total counter\ntestagram_http_requests_total " + strconv.FormatUint(s.requests.Load(), 10) + "\n"))
	})
	mux.HandleFunc("OPTIONS /api/capability", s.capability)
	mux.HandleFunc("POST /api/capability", s.capability)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		mux.ServeHTTP(w, r)
	})
}

func (s *service) allowedOrigins() map[string]struct{} {
	origins := strings.Split(env("ALLOWED_ORIGINS", "https://testagram.site,https://www.testagram.site,http://localhost:5173,http://127.0.0.1:5173"), ",")
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return allowed
}

func (s *service) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "authorization, apikey, content-type, x-client-info, x-request-id, accept")
	w.Header().Set("Access-Control-Max-Age", "600")
	if origin == "" {
		return true // non-browser clients do not use CORS
	}
	if _, ok := s.allowedOrigins()[origin]; !ok {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func errorBody(code, message, requestID string) map[string]any {
	return map[string]any{"ok": false, "data": nil, "error": map[string]string{"code": code, "message": message}, "request_id": requestID}
}

func requestID(r *http.Request) string {
	id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if len(id) > 128 || strings.ContainsAny(id, "\r\n") {
		id = ""
	}
	if id == "" {
		id = fmt.Sprintf("ta-%d", time.Now().UnixNano())
	}
	return id
}

func (s *service) capability(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	id := requestID(r)
	supabaseURL := strings.TrimRight(env("SUPABASE_URL", ""), "/")
	publishableKey := env("SUPABASE_PUBLISHABLE_KEY", env("SUPABASE_ANON_KEY", ""))
	if supabaseURL == "" || publishableKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("BACKEND_NOT_CONFIGURED", "Capability gateway is not configured", id))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCapabilityBody)
	defer r.Body.Close()
	var body capabilityRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Request body must be valid JSON within 64 KiB", id))
		return
	}
	if body.Capability == "" || len(body.Capability) > 160 || strings.ContainsAny(body.Capability, "\r\n") {
		writeJSON(w, http.StatusBadRequest, errorBody("CAPABILITY_REQUIRED", "Capability name is required", id))
		return
	}
	if body.Input == nil {
		body.Input = map[string]any{}
	}
	if !json.Valid(mustJSON(body.Input)) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_INPUT", "Capability input must be JSON", id))
		return
	}

	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	public := body.Capability == "testagram.capabilities.list" ||
		body.Capability == "testagram.health.read" ||
		body.Capability == "testagram.news.trending"
	if !public && !strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		writeJSON(w, http.StatusUnauthorized, errorBody("AUTH_REQUIRED", "Authentication required", id))
		return
	}
	if authorization == "" {
		authorization = "Bearer " + publishableKey
	}
	if body.Capability == "testagram.bookmarks.list" {
		s.bookmarkList(w, r, id, supabaseURL, publishableKey, authorization)
		return
	}

	path := "/rest/v1/rpc/capability_dispatch_v2"
	method := http.MethodPost
	var upstreamBody []byte
	var err error
	if body.Capability == "testagram.bookmarks.add" || body.Capability == "testagram.bookmarks.remove" {
		postID, _ := body.Input["post_id"].(string)
		postID = strings.TrimSpace(postID)
		if postID == "" || len(postID) > 2048 {
			writeJSON(w, http.StatusBadRequest, errorBody("POST_ID_REQUIRED", "A valid post_id is required", id))
			return
		}
		isRemote := strings.HasPrefix(strings.ToLower(postID), "https://")
		if strings.Contains(postID, "://") && !isRemote {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "Remote bookmark IDs must use HTTPS", id))
			return
		}
		rpc := "testagram_bookmark_add"
		if body.Capability == "testagram.bookmarks.remove" {
			rpc = "testagram_bookmark_remove"
		}
		rpcBody := map[string]string{"p_post_id": postID}
		if isRemote {
			rpc = "testagram_federated_bookmark_add"
			if body.Capability == "testagram.bookmarks.remove" {
				rpc = "testagram_federated_bookmark_remove"
			}
			rpcBody = map[string]string{"p_object_uri": postID}
		}
		upstreamBody, err = json.Marshal(rpcBody)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("ENCODE_FAILED", "Unable to encode request", id))
			return
		}
		path = "/rest/v1/rpc/" + rpc
	} else {
		upstreamBody, err = json.Marshal(map[string]any{"p_capability": body.Capability, "p_input": body.Input})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_INPUT", "Capability input must be JSON", id))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	upstreamReq, err := http.NewRequestWithContext(ctx, method, supabaseURL+path, bytes.NewReader(upstreamBody))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("UPSTREAM_REQUEST_FAILED", "Unable to prepare capability request", id))
		return
	}
	upstreamReq.Header.Set("apikey", publishableKey)
	upstreamReq.Header.Set("Authorization", authorization)
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("X-Request-ID", id)
	if method != http.MethodGet {
		upstreamReq.Header.Set("Content-Type", "application/json")
	}
	client := s.upstream
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	upstreamResponse, err := client.Do(upstreamReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("DATABASE_UNAVAILABLE", "Capability backend is temporarily unavailable", id))
		return
	}
	defer upstreamResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(upstreamResponse.Body, maxUpstreamBody+1))
	if err != nil || len(raw) > maxUpstreamBody {
		writeJSON(w, http.StatusBadGateway, errorBody("UPSTREAM_RESPONSE_INVALID", "Capability backend returned an invalid response", id))
		return
	}
	if upstreamResponse.StatusCode < 200 || upstreamResponse.StatusCode >= 300 {
		message := "Capability database request failed"
		var detail struct { Message string `json:"message"` }
		if json.Unmarshal(raw, &detail) == nil && strings.TrimSpace(detail.Message) != "" {
			message = detail.Message
		}
		status := upstreamResponse.StatusCode
		if status >= 500 {
			status = http.StatusBadGateway
		}
		code := "CAPABILITY_DISPATCH_FAILED"
		if upstreamResponse.StatusCode == http.StatusUnauthorized || upstreamResponse.StatusCode == http.StatusForbidden {
			code = "AUTH_REQUIRED"
		}
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Request-ID", id)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *service) bookmarkList(w http.ResponseWriter, r *http.Request, id, baseURL, apiKey, authorization string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	paths := []string{
		"/rest/v1/bookmarks?select=id,post_id,created_at&order=created_at.desc&limit=100",
		"/rest/v1/federated_bookmarks?select=id,object_uri,created_at&order=created_at.desc&limit=100",
	}
	type result struct {
		items []map[string]any
		err   error
		status int
	}
	results := make(chan result, len(paths))
	client := s.upstream
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	for _, path := range paths {
		path := path
		go func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("apikey", apiKey)
			req.Header.Set("Authorization", authorization)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("X-Request-ID", id)
			resp, err := client.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBody+1))
			if err != nil || len(raw) > maxUpstreamBody {
				results <- result{err: errors.New("invalid upstream response"), status: resp.StatusCode}
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				results <- result{err: errors.New("bookmark query failed"), status: resp.StatusCode}
				return
			}
			var items []map[string]any
			if err := json.Unmarshal(raw, &items); err != nil {
				results <- result{err: err, status: resp.StatusCode}
				return
			}
			results <- result{items: items, status: resp.StatusCode}
		}()
	}
	var items []map[string]any
	for range paths {
		res := <-results
		if res.err != nil {
			status := http.StatusBadGateway
			if res.status == http.StatusUnauthorized || res.status == http.StatusForbidden {
				status = res.status
			}
			writeJSON(w, status, errorBody("BOOKMARK_LIST_FAILED", "Bookmark list request failed", id))
			return
		}
		items = append(items, res.items...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, _ := items[i]["created_at"].(string)
		right, _ := items[j]["created_at"].(string)
		return left > right
	})
	if len(items) > 100 {
		items = items[:100]
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items}, "error": nil, "request_id": id})
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("!")
	}
	return encoded
}

func main() {
	addr := env("LISTEN_ADDR", ":8080")
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatalf("parse DATABASE_URL: %v", err)
	}
	maxConns, parseErr := strconv.ParseInt(env("DB_MAX_CONNS", "10"), 10, 32)
	if parseErr != nil || maxConns < 1 || maxConns > 20 {
		log.Fatal("DB_MAX_CONNS must be an integer from 1 to 20")
	}
	poolConfig.MaxConns = int32(maxConns)
	if poolConfig.MinConns > poolConfig.MaxConns {
		poolConfig.MinConns = poolConfig.MaxConns
	}
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	db, err := pgxpool.NewWithConfig(startupCtx, poolConfig)
	startupCancel()
	if err != nil {
		log.Fatalf("create database pool: %v", err)
	}
	defer db.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = db.Ping(pingCtx)
	pingCancel()
	if err != nil {
		log.Fatalf("database startup check failed: %v", err)
	}

	svc := &service{db: db, upstream: &http.Client{Timeout: 8 * time.Second}}
	server := &http.Server{
		Addr: addr, Handler: svc.routes(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Testagram backend listening on %s", addr)
		serverErrors <- server.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server failed: %v", err)
		}
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	}
}
