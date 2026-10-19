package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/media"
	"github.com/Trendyzima/Testagram-backend-/internal/platformclient"
	"github.com/Trendyzima/Testagram-backend-/internal/replication"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxCapabilityBody = 64 << 10
const maxUpstreamBody = 2 << 20

type requestMetric struct {
	count         uint64
	durationNanos uint64
	buckets       [6]uint64
	statuses      map[int]uint64
}

type upstreamMetric struct {
	count         uint64
	durationNanos uint64
	buckets       [6]uint64
	outcomes      map[string]uint64
	statuses      map[int]uint64
}

type service struct {
	db                *pgxpool.Pool
	metricsMu         sync.Mutex
	routeMetrics      map[string]*requestMetric
	capabilityMetrics map[string]*requestMetric
	upstreamMetrics   map[string]*upstreamMetric
	requests          atomic.Uint64
	cacheHits         atomic.Uint64
	cacheMisses       atomic.Uint64
	rateLimited       atomic.Uint64
	platformErrors    atomic.Uint64
	upstream          *http.Client
	platform          *platformclient.Client
	media             *media.Signer
	virtualStorage    *virtualstorage.Manager
	replicationReceiver http.Handler
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

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

type capabilityMetricWriter struct {
	statusRecorder
	group string
}

func unifiedSearchRPCBody(input map[string]any) (map[string]any, error) {
	q, _ := input["q"].(string)
	if len(q) > 512 {
		return nil, errors.New("search query exceeds 512 characters")
	}
	kind, _ := input["kind"].(string)
	if kind == "" {
		kind = "all"
	}
	switch kind {
	case "all", "people", "posts", "hashtags", "threads", "communities", "fediverse", "media", "latest", "top", "instances":
	default:
		return nil, errors.New("unsupported search kind")
	}
	readBoundedInt := func(key string, fallback, min, max int) (int, error) {
		value, ok := input[key]
		if !ok || value == nil {
			return fallback, nil
		}
		parsed, err := strconv.Atoi(fmt.Sprint(value))
		if err != nil || parsed < min || parsed > max {
			return 0, fmt.Errorf("%s must be between %d and %d", key, min, max)
		}
		return parsed, nil
	}
	limit, err := readBoundedInt("limit", 40, 1, 100)
	if err != nil {
		return nil, err
	}
	offset, err := readBoundedInt("offset", 0, 0, 100000)
	if err != nil {
		return nil, err
	}
	sortMode, _ := input["sort"].(string)
	if sortMode == "" {
		sortMode = "relevance"
	}
	switch sortMode {
	case "relevance", "latest", "most_liked", "most_viewed":
	default:
		return nil, errors.New("unsupported search sort")
	}
	return map[string]any{
		"p_q": q, "p_kind": kind, "p_limit": limit, "p_offset": offset, "p_sort": sortMode,
	}, nil
}

func capabilityGroup(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(name, "search"):
		return "search"
	case strings.Contains(name, "feed"):
		return "feed"
	case strings.Contains(name, "media"):
		return "media"
	case strings.Contains(name, "auth"), strings.Contains(name, "session"):
		return "auth"
	case strings.Contains(name, "notification"):
		return "notifications"
	case strings.Contains(name, "federat"):
		return "federation"
	default:
		return "other"
	}
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func normalizedMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead:
		return method
	default:
		return "OTHER"
	}
}

var requestDurationBounds = [...]time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	5 * time.Second,
}

func (s *service) recordCapability(group string, status int, elapsed time.Duration) {
	if group == "" {
		group = "other"
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.capabilityMetrics == nil {
		s.capabilityMetrics = make(map[string]*requestMetric)
	}
	m := s.capabilityMetrics[group]
	if m == nil {
		m = &requestMetric{statuses: make(map[int]uint64)}
		s.capabilityMetrics[group] = m
	}
	m.count++
	if elapsed > 0 {
		m.durationNanos += uint64(elapsed)
	}
	for i, bound := range requestDurationBounds {
		if elapsed <= bound {
			m.buckets[i]++
		}
	}
	m.statuses[status]++
}

func upstreamFailureOutcome(requestErr, contextErr error) string {
	if errors.Is(requestErr, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport_error"
}

func upstreamBodyFailureOutcome(readErr, contextErr error, oversized bool) string {
	if errors.Is(readErr, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return "timeout"
	}
	if readErr != nil || oversized {
		return "invalid_response"
	}
	return "success"
}

func (s *service) recordUpstream(group, outcome string, status int, elapsed time.Duration) {
	switch group {
	case "search", "feed", "media", "auth", "notifications", "federation":
	default:
		group = "other"
	}
	switch outcome {
	case "success", "timeout", "transport_error", "invalid_response", "upstream_4xx", "upstream_5xx":
	default:
		outcome = "transport_error"
	}
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.upstreamMetrics == nil {
		s.upstreamMetrics = make(map[string]*upstreamMetric)
	}
	m := s.upstreamMetrics[group]
	if m == nil {
		m = &upstreamMetric{outcomes: make(map[string]uint64), statuses: make(map[int]uint64)}
		s.upstreamMetrics[group] = m
	}
	m.count++
	m.outcomes[outcome]++
	if status >= 100 && status <= 599 {
		m.statuses[status]++
	}
	if elapsed > 0 {
		m.durationNanos += uint64(elapsed)
	}
	for i, bound := range requestDurationBounds {
		if elapsed <= bound {
			m.buckets[i]++
		}
	}
}

func (s *service) recordRequest(method, route string, status int, elapsed time.Duration) {
	key := method + "|" + route
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.routeMetrics == nil {
		s.routeMetrics = make(map[string]*requestMetric)
	}
	m := s.routeMetrics[key]
	if m == nil {
		m = &requestMetric{statuses: make(map[int]uint64)}
		s.routeMetrics[key] = m
	}
	m.count++
	if elapsed > 0 {
		m.durationNanos += uint64(elapsed)
	}
	for i, bound := range requestDurationBounds {
		if elapsed <= bound {
			m.buckets[i]++
		}
	}
	m.statuses[status]++
}

func (s *service) metricsText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP testagram_http_requests_total Total HTTP requests handled by this process.\n# TYPE testagram_http_requests_total counter\ntestagram_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(&b, "# HELP testagram_platform_cache_hits_total Public capability responses served from the shared cache.\n# TYPE testagram_platform_cache_hits_total counter\ntestagram_platform_cache_hits_total %d\n", s.cacheHits.Load())
	fmt.Fprintf(&b, "# HELP testagram_platform_cache_misses_total Public capability cache misses.\n# TYPE testagram_platform_cache_misses_total counter\ntestagram_platform_cache_misses_total %d\n", s.cacheMisses.Load())
	fmt.Fprintf(&b, "# HELP testagram_rate_limited_requests_total Requests denied by the shared rate limiter.\n# TYPE testagram_rate_limited_requests_total counter\ntestagram_rate_limited_requests_total %d\n", s.rateLimited.Load())
	fmt.Fprintf(&b, "# HELP testagram_platform_errors_total Shared platform API errors observed by the gateway.\n# TYPE testagram_platform_errors_total counter\ntestagram_platform_errors_total %d\n", s.platformErrors.Load())

	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	fmt.Fprintln(&b, "# HELP testagram_http_requests_by_route_total Requests by normalized route, method, and status.")
	fmt.Fprintln(&b, "# TYPE testagram_http_requests_by_route_total counter")
	fmt.Fprintln(&b, "# HELP testagram_http_request_duration_seconds HTTP request duration histogram by normalized route and method.")
	fmt.Fprintln(&b, "# TYPE testagram_http_request_duration_seconds histogram")

	keys := make([]string, 0, len(s.routeMetrics))
	for key := range s.routeMetrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "|", 2)
		method, route := parts[0], parts[1]
		m := s.routeMetrics[key]
		for _, status := range sortedStatusCodes(m.statuses) {
			fmt.Fprintf(&b, "testagram_http_requests_by_route_total{method=%q,route=%q,status=%q} %d\n",
				method, route, strconv.Itoa(status), m.statuses[status])
		}
		for i, bound := range requestDurationBounds {
			fmt.Fprintf(&b, "testagram_http_request_duration_seconds_bucket{method=%q,route=%q,le=%q} %d\n",
				method, route, strconv.FormatFloat(bound.Seconds(), 'f', -1, 64), m.buckets[i])
		}
		fmt.Fprintf(&b, "testagram_http_request_duration_seconds_bucket{method=%q,route=%q,le=%q} %d\n",
			method, route, "+Inf", m.count)
		fmt.Fprintf(&b, "testagram_http_request_duration_seconds_sum{method=%q,route=%q} %.9f\n",
			method, route, float64(m.durationNanos)/float64(time.Second))
		fmt.Fprintf(&b, "testagram_http_request_duration_seconds_count{method=%q,route=%q} %d\n",
			method, route, m.count)
	}

	capabilityGroups := make([]string, 0, len(s.capabilityMetrics))
	for group := range s.capabilityMetrics {
		capabilityGroups = append(capabilityGroups, group)
	}
	sort.Strings(capabilityGroups)
	fmt.Fprintln(&b, "# HELP testagram_capability_requests_total Capability gateway requests by bounded capability group and response status.")
	fmt.Fprintln(&b, "# TYPE testagram_capability_requests_total counter")
	fmt.Fprintln(&b, "# HELP testagram_capability_request_duration_seconds Capability gateway request duration histogram by bounded capability group.")
	fmt.Fprintln(&b, "# TYPE testagram_capability_request_duration_seconds histogram")
	for _, group := range capabilityGroups {
		m := s.capabilityMetrics[group]
		for _, status := range sortedStatusCodes(m.statuses) {
			fmt.Fprintf(&b, "testagram_capability_requests_total{group=%q,status=%q} %d\n", group, strconv.Itoa(status), m.statuses[status])
		}
		for i, bound := range requestDurationBounds {
			fmt.Fprintf(&b, "testagram_capability_request_duration_seconds_bucket{group=%q,le=%q} %d\n", group, strconv.FormatFloat(bound.Seconds(), 'f', -1, 64), m.buckets[i])
		}
		fmt.Fprintf(&b, "testagram_capability_request_duration_seconds_bucket{group=%q,le=%q} %d\n", group, "+Inf", m.count)
		fmt.Fprintf(&b, "testagram_capability_request_duration_seconds_sum{group=%q} %.9f\n", group, float64(m.durationNanos)/float64(time.Second))
		fmt.Fprintf(&b, "testagram_capability_request_duration_seconds_count{group=%q} %d\n", group, m.count)
	}

	if s.db != nil {
		stats := s.db.Stat()
		fmt.Fprintf(&b, "# HELP testagram_db_pool_total_connections Total PostgreSQL pool connections.\n# TYPE testagram_db_pool_total_connections gauge\ntestagram_db_pool_total_connections %d\n", stats.TotalConns())
		fmt.Fprintf(&b, "# HELP testagram_db_pool_idle_connections Idle PostgreSQL pool connections.\n# TYPE testagram_db_pool_idle_connections gauge\ntestagram_db_pool_idle_connections %d\n", stats.IdleConns())
		fmt.Fprintf(&b, "# HELP testagram_db_pool_acquired_connections Acquired PostgreSQL pool connections.\n# TYPE testagram_db_pool_acquired_connections gauge\ntestagram_db_pool_acquired_connections %d\n", stats.AcquiredConns())
		fmt.Fprintf(&b, "# HELP testagram_db_pool_max_connections Configured maximum PostgreSQL pool connections.\n# TYPE testagram_db_pool_max_connections gauge\ntestagram_db_pool_max_connections %d\n", stats.MaxConns())
	}
	upstreamGroups := make([]string, 0, len(s.upstreamMetrics))
	for group := range s.upstreamMetrics {
		upstreamGroups = append(upstreamGroups, group)
	}
	sort.Strings(upstreamGroups)
	fmt.Fprintln(&b, "# HELP testagram_upstream_requests_total Requests to the canonical upstream, by bounded capability group and outcome.")
	fmt.Fprintln(&b, "# TYPE testagram_upstream_requests_total counter")
	fmt.Fprintln(&b, "# HELP testagram_upstream_response_status_total HTTP response statuses returned by the canonical upstream.")
	fmt.Fprintln(&b, "# TYPE testagram_upstream_response_status_total counter")
	fmt.Fprintln(&b, "# HELP testagram_upstream_request_duration_seconds Duration of canonical upstream requests by bounded capability group.")
	fmt.Fprintln(&b, "# TYPE testagram_upstream_request_duration_seconds histogram")
	for _, group := range upstreamGroups {
		m := s.upstreamMetrics[group]
		outcomes := make([]string, 0, len(m.outcomes))
		for outcome := range m.outcomes {
			outcomes = append(outcomes, outcome)
		}
		sort.Strings(outcomes)
		for _, outcome := range outcomes {
			fmt.Fprintf(&b, "testagram_upstream_requests_total{group=%q,outcome=%q} %d\n", group, outcome, m.outcomes[outcome])
		}
		for _, status := range sortedStatusCodes(m.statuses) {
			fmt.Fprintf(&b, "testagram_upstream_response_status_total{group=%q,status=%q} %d\n", group, strconv.Itoa(status), m.statuses[status])
		}
		for i, bound := range requestDurationBounds {
			fmt.Fprintf(&b, "testagram_upstream_request_duration_seconds_bucket{group=%q,le=%q} %d\n", group, strconv.FormatFloat(bound.Seconds(), 'f', -1, 64), m.buckets[i])
		}
		fmt.Fprintf(&b, "testagram_upstream_request_duration_seconds_bucket{group=%q,le=%q} %d\n", group, "+Inf", m.count)
		fmt.Fprintf(&b, "testagram_upstream_request_duration_seconds_sum{group=%q} %.9f\n", group, float64(m.durationNanos)/float64(time.Second))
		fmt.Fprintf(&b, "testagram_upstream_request_duration_seconds_count{group=%q} %d\n", group, m.count)
	}
	return b.String()
}

func sortedStatusCodes(statuses map[int]uint64) []int {
	codes := make([]int, 0, len(statuses))
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	return codes
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
		var output strings.Builder
		output.WriteString(s.metricsText())
		if s.db != nil {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			var outboxPending, oldestOutboxSeconds, outboxFailed, inboxPending, oldestInboxSeconds, inboxRejected, inboxStale, inboxRejected24h int64
			err := s.db.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL),
				       COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL)))::bigint, 0),
				       count(*) FILTER (WHERE failed_at IS NOT NULL)
				FROM platform_replication_outbox`).Scan(&outboxPending, &oldestOutboxSeconds, &outboxFailed)
			if err == nil {
				err = s.db.QueryRow(ctx, `
					SELECT count(*) FILTER (WHERE applied_at IS NULL AND outcome IN ('received','retry')),
					       COALESCE(EXTRACT(EPOCH FROM (now() - min(received_at) FILTER (WHERE applied_at IS NULL AND outcome IN ('received','retry'))))::bigint, 0),
					       count(*) FILTER (WHERE outcome = 'rejected'),
					       count(*) FILTER (WHERE outcome = 'stale'),
					       count(*) FILTER (WHERE outcome = 'rejected' AND received_at >= now() - interval '24 hours')
					FROM platform_replication_inbox`).Scan(&inboxPending, &oldestInboxSeconds, &inboxRejected, &inboxStale, &inboxRejected24h)
			}
			if err == nil {
				fmt.Fprintf(&output, "testagram_replication_outbox_pending %d\n", outboxPending)
				fmt.Fprintf(&output, "testagram_replication_outbox_oldest_seconds %d\n", oldestOutboxSeconds)
				fmt.Fprintf(&output, "testagram_replication_outbox_failed_total %d\n", outboxFailed)
				fmt.Fprintf(&output, "testagram_replication_inbox_pending %d\n", inboxPending)
				fmt.Fprintf(&output, "testagram_replication_inbox_oldest_seconds %d\n", oldestInboxSeconds)
				fmt.Fprintf(&output, "testagram_replication_inbox_rejected_total %d\n", inboxRejected)
				fmt.Fprintf(&output, "testagram_replication_inbox_rejected_24h %d\n", inboxRejected24h)
				fmt.Fprintf(&output, "testagram_replication_inbox_stale_total %d\n", inboxStale)
			}
		}
		_, _ = w.Write([]byte(output.String()))
	})
	// Independent authentication routes use Testagram PostgreSQL only; Redis, Upstash and Supabase are not in this request path.
	for _, action := range []string{"signup", "resend-verification", "verify-email", "login", "forgot-password", "reset-password"} {
		action := action
		mux.HandleFunc("OPTIONS /api/auth/"+action, func(w http.ResponseWriter, r *http.Request) { s.authRespond(w, r, action) })
		mux.HandleFunc("POST /api/auth/"+action, func(w http.ResponseWriter, r *http.Request) { s.authRespond(w, r, action) })
	}
	mux.HandleFunc("OPTIONS /api/capability", s.capability)
	mux.HandleFunc("POST /api/capability", s.capability)
	mux.HandleFunc("OPTIONS /api/media/upload-intent", s.mediaPreflight)
	mux.HandleFunc("POST /api/media/upload-intent", s.mediaUploadIntent)
	// Browser uploads use cross-origin PUT with Authorization and Content-Type,
	// so the bounded upload gateway must explicitly handle the OPTIONS preflight.
	mux.HandleFunc("OPTIONS /api/media/upload", s.mediaPreflight)
	mux.HandleFunc("PUT /api/media/upload", s.mediaUploadGateway)
	mux.HandleFunc("OPTIONS /api/media/complete", s.mediaPreflight)
	mux.HandleFunc("POST /api/media/complete", s.mediaComplete)
	mux.HandleFunc("OPTIONS /api/media/delete", s.mediaPreflight)
	mux.HandleFunc("POST /api/media/delete", s.mediaDelete)
	mux.HandleFunc("OPTIONS /api/media/attach", s.mediaPreflight)
	mux.HandleFunc("POST /api/media/attach", s.mediaAttach)
	mux.HandleFunc("POST /internal/media/quarantine", s.mediaQuarantine)
	mux.HandleFunc("GET /api/replication/public/{origin}/{entity_type}/{entity_id}", s.publicReplicationProjection)
	if s.replicationReceiver != nil {
		mux.Handle("POST /internal/replication/events", s.replicationReceiver)
	}
	mux.HandleFunc("OPTIONS /api/media/private-download-url", s.mediaPreflight)
	mux.HandleFunc("POST /api/media/private-download-url", s.mediaPrivateDownloadURL)
	mux.HandleFunc("OPTIONS /api/media/list", s.mediaPreflight)
	mux.HandleFunc("POST /api/media/list", s.mediaList)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		w.Header().Set("X-Request-ID", id)
		s.requests.Add(1)
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(recorder, r)
		s.recordRequest(normalizedMethod(r.Method), normalizedRoute(r.URL.Path), recorder.status, time.Since(started))
	})
}


func (s *service) publicReplicationProjection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	origin := r.PathValue("origin")
	entityType := r.PathValue("entity_type")
	entityID := r.PathValue("entity_id")
	if (origin != "xclone" && origin != "testagram-vps") ||
		(entityType != "account" && entityType != "post" && entityType != "media") ||
		!replicationReadIdentity.MatchString(entityID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid public projection identity"})
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "projection database unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	var payload string
	var version int64
	var updatedAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT payload::text, entity_version, updated_at
		FROM platform_replication_projection
		WHERE origin=$1 AND entity_type=$2 AND entity_id=$3 AND tombstoned=FALSE
	`, origin, entityType, entityID).Scan(&payload, &version, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "projection query failed"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true,"data":{"origin":` + replicationJSONString(origin) + `,"entity_type":` + replicationJSONString(entityType) + `,"entity_id":` + replicationJSONString(entityID) + `,"version":` + strconv.FormatInt(version, 10) + `,"updated_at":` + replicationJSONString(updatedAt.UTC().Format(time.RFC3339Nano)) + `,"payload":` + payload + `}}`))
}

var replicationReadIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func replicationJSONString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func normalizedRoute(path string) string {
	if strings.HasPrefix(path, "/api/replication/public/") {
		return "/api/replication/public/{origin}/{entity_type}/{entity_id}"
	}
	switch path {
	case "/healthz", "/readyz", "/metrics", "/api/capability",
		"/api/auth/signup", "/api/auth/resend-verification", "/api/auth/verify-email", "/api/auth/login", "/api/auth/forgot-password", "/api/auth/reset-password",
		"/api/auth/signup", "/api/auth/verify-email", "/api/auth/login", "/api/auth/forgot-password", "/api/auth/reset-password",
		"/api/media/upload-intent", "/api/media/upload", "/api/media/complete",
		"/api/media/delete", "/api/media/attach", "/api/media/private-download-url",
		"/api/media/list", "/internal/media/quarantine", "/internal/replication/events":
		return path
	default:
		return "unmatched"
	}
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
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
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

func publicCacheTTL(capability string) (time.Duration, bool) {
	switch capability {
	case "testagram.capabilities.list":
		return 10 * time.Minute, true
	case "testagram.news.trending":
		return 30 * time.Second, true
	case "testagram.health.read":
		return 10 * time.Second, true
	default:
		return 0, false
	}
}

func capabilityCacheKey(body capabilityRequest) string {
	raw, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "testagram:api:v1:" + hex.EncodeToString(sum[:])
}

func capabilityRateLimitKey(r *http.Request, authorization string) string {
	identity := strings.TrimSpace(authorization)
	if identity == "" {
		identity = strings.TrimSpace(r.Header.Get("X-Real-IP"))
		if identity == "" {
			identity = strings.TrimSpace(r.RemoteAddr)
		}
	}
	sum := sha256.Sum256([]byte(identity))
	return "api:" + hex.EncodeToString(sum[:])
}

func isWriteCapability(capability string) bool {
	for _, suffix := range []string{".create", ".add", ".remove", ".update", ".delete", ".send", ".upload"} {
		if strings.HasSuffix(capability, suffix) {
			return true
		}
	}
	return false
}

func writeRawCapability(w http.ResponseWriter, r *http.Request, id string, raw []byte, cacheTTL time.Duration) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Request-ID", id)
	if cacheTTL > 0 && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(cacheTTL.Seconds())))
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *service) capability(w http.ResponseWriter, r *http.Request) {
	var observation *capabilityMetricWriter
	if r.Method != http.MethodOptions {
		observation = &capabilityMetricWriter{
			statusRecorder: statusRecorder{ResponseWriter: w, status: http.StatusOK},
			group:          "other",
		}
		w = observation
		started := time.Now()
		defer func() { s.recordCapability(observation.group, observation.status, time.Since(started)) }()
	}
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

	r.Body = http.MaxBytesReader(w, r.Body, maxCapabilityBody)
	defer r.Body.Close()
	var body capabilityRequest
	if err := decodeSingleJSON(r.Body, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Request body must contain exactly one valid JSON value within 64 KiB", id))
		return
	}
	if body.Capability == "" || len(body.Capability) > 160 || strings.ContainsAny(body.Capability, "\r\n") {
		writeJSON(w, http.StatusBadRequest, errorBody("CAPABILITY_REQUIRED", "Capability name is required", id))
		return
	}
	if observation != nil {
		observation.group = capabilityGroup(body.Capability)
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
	if s.platform != nil {
		limit := int64(120)
		if isWriteCapability(body.Capability) {
			limit = 30
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		result, err := s.platform.Check(ctx, capabilityRateLimitKey(r, authorization), limit, time.Minute)
		cancel()
		if err != nil {
			// Rate limiting is defense in depth; a platform outage must not take
			// the capability gateway offline. The error is deliberately not logged
			// with the raw key or credential.
			s.platformErrors.Add(1)
		} else if !result.Allowed {
			s.rateLimited.Add(1)
			retryAfter := result.RetryAfterSeconds
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Request rate limit exceeded", id))
			return
		}
	}
	if !public && !strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		writeJSON(w, http.StatusUnauthorized, errorBody("AUTH_REQUIRED", "Authentication required", id))
		return
	}
	if s.localProductCapability(w, r, id, body) {
		return
	}
	if supabaseURL == "" || publishableKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("BACKEND_NOT_CONFIGURED", "Capability gateway is not configured", id))
		return
	}

	cacheTTL, cacheable := publicCacheTTL(body.Capability)
	cacheKey := ""
	if s.platform != nil && cacheable && authorization == "" {
		cacheKey = capabilityCacheKey(body)
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		cached, err := s.platform.Get(ctx, cacheKey)
		cancel()
		if err == nil && len(cached) <= 128<<10 && json.Valid([]byte(cached)) {
			s.cacheHits.Add(1)
			writeRawCapability(w, r, id, []byte(cached), cacheTTL)
			return
		}
		s.cacheMisses.Add(1)
		if err != nil && !errors.Is(err, platformclient.ErrNotFound) {
			s.platformErrors.Add(1)
		}
		if err == nil {
			// Invalid or oversized cache content must never be returned to clients.
			ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			_ = s.platform.Delete(ctx, cacheKey)
			cancel()
		}
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
	if body.Capability == "testagram.search.unified" {
		rpcBody, searchErr := unifiedSearchRPCBody(body.Input)
		if searchErr != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_INPUT", "Unified search parameters are invalid", id))
			return
		}
		upstreamBody, err = json.Marshal(rpcBody)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("ENCODE_FAILED", "Unable to encode search request", id))
			return
		}
		path = "/rest/v1/rpc/testagram_search_unified"
	} else if body.Capability == "testagram.bookmarks.add" || body.Capability == "testagram.bookmarks.remove" {
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
	upstreamGroup := capabilityGroup(body.Capability)
	upstreamStarted := time.Now()
	upstreamOutcome := "transport_error"
	upstreamStatus := 0
	defer func() {
		s.recordUpstream(upstreamGroup, upstreamOutcome, upstreamStatus, time.Since(upstreamStarted))
	}()
	upstreamResponse, err := client.Do(upstreamReq)
	if err != nil {
		upstreamOutcome = upstreamFailureOutcome(err, ctx.Err())
		log.Printf("capability upstream request failed request_id=%q group=%q error=%q", id, upstreamGroup, err)
		writeJSON(w, http.StatusBadGateway, errorBody("DATABASE_UNAVAILABLE", "Capability backend is temporarily unavailable", id))
		return
	}
	defer upstreamResponse.Body.Close()
	upstreamStatus = upstreamResponse.StatusCode
	raw, err := io.ReadAll(io.LimitReader(upstreamResponse.Body, maxUpstreamBody+1))
	if err != nil || len(raw) > maxUpstreamBody {
		upstreamOutcome = upstreamBodyFailureOutcome(err, ctx.Err(), len(raw) > maxUpstreamBody)
		if upstreamOutcome == "timeout" {
			writeJSON(w, http.StatusGatewayTimeout, errorBody("UPSTREAM_TIMEOUT", "Capability backend took too long to respond", id))
		} else {
			writeJSON(w, http.StatusBadGateway, errorBody("UPSTREAM_RESPONSE_INVALID", "Capability backend returned an invalid response", id))
		}
		return
	}
	if upstreamResponse.StatusCode < 200 || upstreamResponse.StatusCode >= 300 {
		if upstreamResponse.StatusCode >= 500 {
			upstreamOutcome = "upstream_5xx"
		} else {
			upstreamOutcome = "upstream_4xx"
		}
		// Never expose raw database/upstream errors or user search terms to logs.
		log.Printf("capability upstream response failed request_id=%q group=%q status=%d", id, capabilityGroup(body.Capability), upstreamResponse.StatusCode)
		message := "Capability database request failed"
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
	upstreamOutcome = "success"
	if cacheKey != "" && len(raw) <= 128<<10 && json.Valid(raw) {
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		if err := s.platform.Set(ctx, cacheKey, string(raw), cacheTTL); err != nil {
			s.platformErrors.Add(1)
		}
		cancel()
	}
	writeRawCapability(w, r, id, raw, cacheTTL)
}

func (s *service) bookmarkList(w http.ResponseWriter, r *http.Request, id, baseURL, apiKey, authorization string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	paths := []string{
		"/rest/v1/bookmarks?select=id,post_id,created_at&order=created_at.desc&limit=100",
		"/rest/v1/federated_bookmarks?select=id,object_uri,created_at&order=created_at.desc&limit=100",
	}
	type result struct {
		items  []map[string]any
		err    error
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

// decodeSingleJSON rejects trailing values and non-whitespace bytes after the first
// JSON value. Accepting concatenated JSON documents can cause different layers to
// interpret the same request body differently.
func decodeSingleJSON(r io.Reader, dst any) error {
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body contains multiple JSON values")
		}
		return err
	}
	return nil
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("!")
	}
	return encoded
}


func runReplicationApplyWorker(ctx context.Context, store *replication.Store) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	applier := replication.ProjectionApplier{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			batchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			applied, err := store.ApplyPendingBatch(batchCtx, 25, applier.Apply)
			cancel()
			if err != nil {
				// Error messages contain operation context only; payloads and secrets
				// are intentionally never logged.
				log.Printf("replication inbox projector failed: %v", err)
				continue
			}
			if applied > 0 {
				log.Printf("replication inbox projector applied %d public projections", applied)
			}
		}
	}
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

	var platform *platformclient.Client
	platformURL := strings.TrimSpace(os.Getenv("PLATFORM_URL"))
	platformToken := strings.TrimSpace(os.Getenv("PLATFORM_INTERNAL_TOKEN"))
	if platformURL != "" && platformToken != "" {
		platform, err = platformclient.New(platformURL, platformToken, 2*time.Second)
		if err != nil {
			log.Fatalf("configure internal platform client: %v", err)
		}
		log.Printf("internal cache and rate limiting enabled")
	} else if platformURL != "" || platformToken != "" {
		log.Printf("internal platform integration disabled: PLATFORM_URL and PLATFORM_INTERNAL_TOKEN must both be configured")
	}
	var mediaSigner *media.Signer
	// MEDIA_S3_* is the documented deployment contract. R2_* and legacy MINIO_*
	// variables remain accepted to avoid breaking existing self-hosted installs.
	mediaAccessKey := env("MEDIA_S3_ACCESS_KEY", env("R2_ACCESS_KEY_ID", env("MINIO_MEDIA_ACCESS_KEY", "")))
	mediaSecretKey := env("MEDIA_S3_SECRET_KEY", env("R2_SECRET_ACCESS_KEY", env("MINIO_MEDIA_SECRET_KEY", "")))
	if mediaAccessKey != "" || mediaSecretKey != "" {
		if mediaAccessKey == "" || mediaSecretKey == "" {
			log.Fatal("MEDIA_S3_ACCESS_KEY and MEDIA_S3_SECRET_KEY must be configured together")
		}
		endpoint := env("MEDIA_S3_ENDPOINT", env("R2_S3_ENDPOINT", env("MINIO_PUBLIC_ENDPOINT", "")))
		if endpoint == "" {
			log.Fatal("MEDIA_S3_ENDPOINT must be configured when media storage credentials are set")
		}
		mediaSigner, err = media.NewSigner(media.Config{
			Endpoint:    endpoint,
			SigningHost: env("MEDIA_S3_SIGNING_HOST", env("R2_SIGNING_HOST", env("MINIO_SIGNING_HOST", ""))),
			AccessKey:   mediaAccessKey, SecretKey: mediaSecretKey,
			Region:        env("MEDIA_S3_REGION", env("R2_REGION", env("MINIO_REGION", "auto"))),
			PrivateBucket: env("MEDIA_PRIVATE_BUCKET", env("R2_PRIVATE_BUCKET", env("MINIO_BUCKET", "testagram-private-media"))),
			PublicBucket:  env("MEDIA_PUBLIC_BUCKET", env("R2_PUBLIC_BUCKET", env("PUBLIC_MEDIA_BUCKET", "testagram-public-media"))),
		})
		if err != nil {
			log.Fatalf("configure S3-compatible media signer: %v", err)
		}
		log.Printf("S3-compatible media upload and private download signing enabled")
	}
	var virtualStorageManager *virtualstorage.Manager
	if strings.EqualFold(env("VIRTUAL_STORAGE_ENABLED", "false"), "true") {
		if mediaSigner == nil {
			log.Fatal("VIRTUAL_STORAGE_ENABLED requires the S3-compatible media credentials and endpoint")
		}
		provider, providerErr := virtualstorage.NewS3Provider(virtualstorage.S3Config{
			Endpoint:    env("MEDIA_S3_ENDPOINT", env("R2_S3_ENDPOINT", env("MINIO_PUBLIC_ENDPOINT", ""))),
			SigningHost: env("MEDIA_S3_SIGNING_HOST", env("R2_SIGNING_HOST", env("MINIO_SIGNING_HOST", ""))),
			AccessKey:   mediaAccessKey, SecretKey: mediaSecretKey,
			Region: env("MEDIA_S3_REGION", env("R2_REGION", env("MINIO_REGION", "auto"))),
			Client: &http.Client{Timeout: 8 * time.Second},
		})
		if providerErr != nil {
			log.Fatalf("configure virtual storage provider: %v", providerErr)
		}
		allowedTypes := map[string]struct{}{
			"image/jpeg": {}, "image/png": {}, "image/webp": {}, "image/gif": {}, "image/avif": {},
			"video/mp4": {}, "video/webm": {},
		}
		virtualStorageManager, err = virtualstorage.New(provider, []virtualstorage.Namespace{
			{Name: "media-private", PhysicalBucket: env("MEDIA_PRIVATE_BUCKET", env("R2_PRIVATE_BUCKET", env("MINIO_BUCKET", "testagram-private-media"))), MaxObjectBytes: 500 << 20, AllowedTypes: allowedTypes, UploadTTL: 10 * time.Minute, DownloadTTL: 5 * time.Minute},
			{Name: "media-public", PhysicalBucket: env("MEDIA_PUBLIC_BUCKET", env("R2_PUBLIC_BUCKET", env("PUBLIC_MEDIA_BUCKET", "testagram-public-media"))), Public: true, MaxObjectBytes: 20 << 20, AllowedTypes: allowedTypes, UploadTTL: 10 * time.Minute, DownloadTTL: 5 * time.Minute},
		})
		if err != nil {
			log.Fatalf("configure virtual storage namespaces: %v", err)
		}
		log.Printf("virtual storage policy manager enabled; new uploads use tenant-scoped object keys")
	}
	var replicationStore *replication.Store
	var replicationReceiver http.Handler
	replicationApplyEnabled := strings.EqualFold(env("REPLICATION_APPLY_ENABLED", "false"), "true")
	replicationKey := strings.TrimSpace(os.Getenv("REPLICATION_PEER_KEY"))
	replicationPeerOrigin := strings.TrimSpace(os.Getenv("REPLICATION_PEER_ORIGIN"))
	replicationPeerURL := strings.TrimSpace(os.Getenv("REPLICATION_PEER_URL"))
	replicationOrigin := strings.TrimSpace(os.Getenv("REPLICATION_ORIGIN"))
	if replicationKey != "" || replicationPeerOrigin != "" || replicationPeerURL != "" || replicationOrigin != "" {
		if replicationKey == "" || len([]byte(replicationKey)) < 32 {
			log.Fatal("replication configuration requires REPLICATION_PEER_KEY with at least 32 characters")
		}
		replicationStore, err = replication.NewStore(db)
		if err != nil {
			log.Fatalf("configure replication store: %v", err)
		}
		if replicationPeerOrigin != "" {
			receiver, receiverErr := replication.NewQueueReceiver(replicationStore, map[string][]byte{
				replicationPeerOrigin: []byte(replicationKey),
			})
			if receiverErr != nil {
				log.Fatalf("configure replication receiver: %v", receiverErr)
			}
			replicationReceiver = receiver
			log.Printf("authenticated replication inbox enabled")
		}
		if replicationPeerURL != "" {
			if replicationOrigin == "" {
				log.Fatal("REPLICATION_ORIGIN is required when REPLICATION_PEER_URL is set")
			}
			if !strings.HasPrefix(replicationPeerURL, "https://") {
				log.Fatal("REPLICATION_PEER_URL must use HTTPS")
			}
			log.Printf("durable replication relay enabled")
		}
	}
	svc := &service{db: db, upstream: &http.Client{Timeout: 8 * time.Second}, platform: platform, media: mediaSigner, virtualStorage: virtualStorageManager, replicationReceiver: replicationReceiver}
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
	go svc.runMediaJanitor(signalCtx)
	go svc.runStorageDeletionWorker(signalCtx)
	if replicationApplyEnabled {
		if replicationStore == nil {
			log.Fatal("REPLICATION_APPLY_ENABLED requires replication configuration and database access")
		}
		log.Printf("replication public-projection worker enabled")
		go runReplicationApplyWorker(signalCtx, replicationStore)
	}
	if replicationStore != nil && replicationPeerURL != "" {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			for {
				select {
				case <-signalCtx.Done():
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(signalCtx, 8*time.Minute)
					err := replicationStore.RelayOnce(ctx, client, replicationPeerURL, replicationOrigin, []byte(replicationKey), 50)
					cancel()
					if err != nil {
						// Avoid logging peer response bodies, event payloads, or key material.
						log.Printf("replication relay batch encountered retryable failures")
					}
				}
			}
		}()
	}
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
