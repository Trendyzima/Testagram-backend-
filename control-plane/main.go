package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/Trendyzima/Testagram-backend-/internal/crypto"
)

type Server struct {
	db        *pgxpool.Pool
	internal  string
	bootstrap string
}

type Registration struct {
	Name         string         `json:"name"`
	Platform     string         `json:"platform"`
	Arch         string         `json:"arch"`
	CPUCores     int            `json:"cpuCores"`
	MemoryBytes  int64          `json:"memoryBytes"`
	StorageBytes int64          `json:"storageBytes"`
	FreeBytes    int64          `json:"freeBytes"`
	Endpoint     string         `json:"endpoint"`
	Capabilities map[string]any `json:"capabilities"`
}

type Heartbeat struct {
	CPUCores     int            `json:"cpuCores"`
	MemoryBytes  int64          `json:"memoryBytes"`
	StorageBytes int64          `json:"storageBytes"`
	FreeBytes    int64          `json:"freeBytes"`
	Status       string         `json:"status"`
	Capabilities map[string]any `json:"capabilities"`
}

type Workload struct {
	Image       string            `json:"image"`
	ImageDigest string            `json:"imageDigest"`
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env"`
	CPUMillis   int               `json:"cpuMillis"`
	MemoryBytes int64             `json:"memoryBytes"`
	StorageBytes int64            `json:"storageBytes"`
	Volume      string            `json:"volume"`
	Arch        string            `json:"arch"`
}

type WorkloadResult struct {
	Success bool   `json:"success"`
}

func bearer(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(v, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
	}
	return ""
}

func equalSecret(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func newToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func reply(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) internalOnly(w http.ResponseWriter, r *http.Request) bool {
	if !equalSecret(bearer(r), s.internal) {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !equalSecret(bearer(r), s.bootstrap) {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid bootstrap token"})
		return
	}

	var in Registration
	if err := decodeJSON(w, r, &in); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if in.Name == "" || in.Arch == "" || in.Endpoint == "" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "name, arch and endpoint are required"})
		return
	}

	nodeToken, err := newToken(32)
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id string
	const q = `insert into public.vps_nodes
		(name,platform,arch,cpu_cores,memory_bytes,storage_bytes,free_bytes,capabilities,endpoint,token_hash)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) returning id`
	err = s.db.QueryRow(ctx, q, in.Name, in.Platform, in.Arch, in.CPUCores,
		in.MemoryBytes, in.StorageBytes, in.FreeBytes, in.Capabilities, in.Endpoint,
		crypto.HashToken(nodeToken)).Scan(&id)
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "registration failed"})
		return
	}

	reply(w, http.StatusCreated, map[string]any{
		"id":               id,
		"token":            nodeToken,
		"heartbeatSeconds": 15,
	})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	nodeToken := bearer(r)
	if nodeToken == "" {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "missing node token"})
		return
	}

	var in Heartbeat
	if err := decodeJSON(w, r, &in); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id string
	status := in.Status
	if status == "" {
		status = "online"
	}
	const q = `update public.vps_nodes
		set cpu_cores=$1,memory_bytes=$2,storage_bytes=$3,free_bytes=$4,
		    status=$5,capabilities=$6,last_seen_at=now()
		where token_hash=$7 returning id`
	err := s.db.QueryRow(ctx, q, in.CPUCores, in.MemoryBytes, in.StorageBytes,
		in.FreeBytes, status, in.Capabilities, crypto.HashToken(nodeToken)).Scan(&id)
	if err == pgx.ErrNoRows {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "unknown node"})
		return
	}
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "heartbeat failed"})
		return
	}

	reply(w, http.StatusOK, map[string]any{"ok": true, "nodeId": id})
}

func (s *Server) schedule(w http.ResponseWriter, r *http.Request) {
	if !s.internalOnly(w, r) {
		return
	}
	var in Workload
	if err := decodeJSON(w, r, &in); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if in.Image == "" || strings.ContainsAny(in.Image, " \t\r\n") || !validImageDigest(in.ImageDigest) {
		reply(w, http.StatusBadRequest, map[string]string{"error": "image and immutable sha256 imageDigest are required"})
		return
	}
	approvedImages := os.Getenv("VPS_ALLOWED_WORKLOAD_IMAGES")
	if !allowedWorkloadImage(in.Image+"@"+in.ImageDigest, approvedImages) {
		reply(w, http.StatusForbidden, map[string]string{"error": "workload image is not in VPS_ALLOWED_WORKLOAD_IMAGES"})
		return
	}
	if strings.Contains(in.Image, "@") {
	if in.CPUMillis <= 0 { in.CPUMillis = 250 }
	if in.MemoryBytes <= 0 { in.MemoryBytes = 268435456 }
	if in.StorageBytes <= 0 { in.StorageBytes = 268435456 }
	if in.CPUMillis < 50 || in.CPUMillis > 64000 || in.MemoryBytes < 67108864 || in.MemoryBytes > 68719476736 || in.StorageBytes < 67108864 || in.StorageBytes > 1099511627776 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "resource request outside supported bounds"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		reply(w, http.StatusServiceUnavailable, map[string]string{"error": "scheduler transaction unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var nodeID, endpoint string
	const nodeQ = "select n.id::text,n.endpoint from public.vps_nodes n " +
		"left join lateral (select coalesce(sum(cpu_millis),0)::bigint cpu_reserved," +
		"coalesce(sum(memory_bytes),0)::bigint memory_reserved,coalesce(sum(storage_bytes),0)::bigint storage_reserved " +
		"from public.vps_workloads where node_id=n.id and status in ('assigned','running')) r on true " +
		"where n.status='online' and n.last_seen_at>now()-interval '45 seconds' " +
		"and n.capabilities @> '{\"workloadExecution\":true}'::jsonb " +
		"and n.cpu_cores*1000-r.cpu_reserved >= $1 and n.memory_bytes-r.memory_reserved >= $2 " +
		"and n.free_bytes-r.storage_reserved >= $3 and ($4='' or n.arch=$4) " +
		"order by (n.free_bytes-r.storage_reserved) desc limit 1 for update of n skip locked"
	err = tx.QueryRow(ctx, nodeQ, in.CPUMillis, in.MemoryBytes, in.StorageBytes, in.Arch).Scan(&nodeID, &endpoint)
	if err == pgx.ErrNoRows {
		reply(w, http.StatusServiceUnavailable, map[string]string{"error": "no healthy node has sufficient unreserved CPU, RAM and storage"})
		return
	}
	if err != nil {
		reply(w, http.StatusServiceUnavailable, map[string]string{"error": "scheduler failed"})
		return
	}
	commandJSON, err := json.Marshal(in.Command)
	if err != nil { reply(w, http.StatusBadRequest, map[string]string{"error": "invalid command"}); return }
	envJSON, err := json.Marshal(in.Env)
	if err != nil { reply(w, http.StatusBadRequest, map[string]string{"error": "invalid environment"}); return }
	var workloadID string
	const workloadQ = "insert into public.vps_workloads " +
		"(node_id,image,image_digest,command,env,cpu_millis,memory_bytes,storage_bytes,volume,status) " +
		"values($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9,'assigned') returning id::text"
	err = tx.QueryRow(ctx, workloadQ, nodeID, in.Image, in.ImageDigest, commandJSON, envJSON,
		in.CPUMillis, in.MemoryBytes, in.StorageBytes, nullIfEmpty(in.Volume)).Scan(&workloadID)
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "workload persist failed"})
		return
	}
	if err = tx.Commit(ctx); err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "scheduler transaction commit failed"})
		return
	}
	reply(w, http.StatusCreated, map[string]any{
		"workloadId": workloadID, "nodeId": nodeID, "endpoint": endpoint,
		"cpuMillis": in.CPUMillis, "memoryBytes": in.MemoryBytes, "storageBytes": in.StorageBytes,
	})
}

func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	nodeToken := bearer(r)
	if nodeToken == "" {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "missing node token"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var workloadID string
	var work Workload
	const q = `update public.vps_workloads w
		set status='running', started_at=now(), updated_at=now()
		where w.id = (
			select w2.id
			from public.vps_workloads w2
			join public.vps_nodes n on n.id=w2.node_id
			where n.token_hash=$1 and n.status='online' and w2.status='assigned'
			order by w2.created_at asc
			for update skip locked
			limit 1
		)
		returning w.id,w.image,w.image_digest,w.command,w.env,w.cpu_millis,w.memory_bytes,w.storage_bytes,w.volume`
	err := s.db.QueryRow(ctx, q, crypto.HashToken(nodeToken)).Scan(
		&workloadID, &work.Image, &work.ImageDigest, &work.Command, &work.Env,
		&work.CPUMillis, &work.MemoryBytes, &work.StorageBytes, &work.Volume)
	if err == pgx.ErrNoRows {
		reply(w, http.StatusNoContent, nil)
		return
	}
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "poll failed"})
		return
	}

	_, _ = s.db.Exec(ctx, "update testagram_hosting.deployments set status='running',updated_at=now() where workload_id=$1::uuid", workloadID)
	reply(w, http.StatusOK, map[string]any{"id": workloadID, "workload": work})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) { reply(w, http.StatusOK, map[string]string{"status": "ok"}) }

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Ping(r.Context()); err != nil {
		reply(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	reply(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	nodeToken := bearer(r)
	if nodeToken == "" {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "missing node token"})
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/v1/workloads/")
	id = strings.TrimSuffix(id, "/result")
	if id == "" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "workload id required"})
		return
	}

	var in WorkloadResult
	if err := decodeJSON(w, r, &in); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	status := "failed"
	if in.Success {
		status = "stopped"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	const q = `update public.vps_workloads w
		set status=$1,finished_at=now()
		from public.vps_nodes n
		where w.id=$2 and w.node_id=n.id and n.token_hash=$3
		  and w.status='running'`
	tag, err := s.db.Exec(ctx, q, status, id, crypto.HashToken(nodeToken))
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "result update failed"})
		return
	}
	if tag.RowsAffected() != 1 {
		reply(w, http.StatusNotFound, map[string]string{"error": "workload not found"})
		return
	}
	_, _ = s.db.Exec(ctx, "update testagram_hosting.deployments set status=$2,updated_at=now() where workload_id=$1::uuid", id, status)
	reply(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}


func allowedWorkloadImage(image, configured string) bool {
	if strings.TrimSpace(configured) == "" || strings.TrimSpace(image) == "" {
		return false
	}
	for _, candidate := range strings.Split(configured, ",") {
		if strings.TrimSpace(candidate) == image {
			return true
		}
	}
	return false
}

func validImageDigest(v string) bool { if len(v) != 71 || !strings.HasPrefix(v, "sha256:") { return false }; for _, c := range v[7:] { if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) { return false } }; return true }

func main() {
	dsn := os.Getenv("DATABASE_URL")
	internal := os.Getenv("VPS_INTERNAL_TOKEN")
	bootstrap := os.Getenv("VPS_NODE_BOOTSTRAP_TOKEN")
	if dsn == "" || internal == "" || bootstrap == "" {
		log.Fatal("DATABASE_URL, VPS_INTERNAL_TOKEN and VPS_NODE_BOOTSTRAP_TOKEN are required")
	}

	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	s := &Server{db: db, internal: internal, bootstrap: bootstrap}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.ready)
	mux.HandleFunc("/v1/nodes/register", s.register)
	mux.HandleFunc("/v1/nodes/heartbeat", s.heartbeat)
	mux.HandleFunc("/v1/nodes/poll", s.poll)
	mux.HandleFunc("/v1/workloads", s.schedule)
	mux.HandleFunc("/v1/workloads/", s.result)
	s.registerHostingRoutes(mux)

	addr := os.Getenv("VPS_CONTROL_PLANE_ADDR")
	if addr == "" {
		addr = ":8787"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           s.withManagedDomainRouting(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Testagram VPS control plane listening on %s", addr)
	certFile := os.Getenv("VPS_CONTROL_PLANE_TLS_CERT_FILE")
	keyFile := os.Getenv("VPS_CONTROL_PLANE_TLS_KEY_FILE")
	if (certFile == "") != (keyFile == "") {
		log.Fatal("VPS_CONTROL_PLANE_TLS_CERT_FILE and VPS_CONTROL_PLANE_TLS_KEY_FILE must be set together")
	}
	if certFile != "" {
		log.Fatal(server.ListenAndServeTLS(certFile, keyFile))
	}
	log.Fatal(server.ListenAndServe())
}
