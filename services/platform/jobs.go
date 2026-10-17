package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type enqueueJobRequest struct {
	Queue           string          `json:"queue"`
	Payload         json.RawMessage `json:"payload"`
	IdempotencyKey  string          `json:"idempotency_key"`
	MaxAttempts     int             `json:"max_attempts"`
	DelaySeconds    int             `json:"delay_seconds"`
}

type claimJobRequest struct {
	Queue      string `json:"queue"`
	Worker     string `json:"worker"`
	Count      int    `json:"count"`
	LeaseSeconds int  `json:"lease_seconds"`
}

type finishJobRequest struct {
	ID      string `json:"id"`
	Worker  string `json:"worker"`
	Error   string `json:"error"`
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

type durableJob struct {
	ID          string          `json:"id"`
	Queue       string          `json:"queue"`
	Payload     json.RawMessage `json:"payload"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	LockedBy    string          `json:"locked_by,omitempty"`
}

func (s *server) jobEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
	if !s.authorize(w, r) { return }
	var in enqueueJobRequest
	if !decodeJSON(w, r, &in) || !validJobQueue(in.Queue) || !validJSONObject(in.Payload) ||
		len(in.IdempotencyKey) > 200 || strings.ContainsAny(in.IdempotencyKey, "\r\n") ||
		in.MaxAttempts < 0 || in.MaxAttempts > 20 || in.DelaySeconds < 0 || in.DelaySeconds > 604800 {
		http.Error(w, "invalid job request", http.StatusBadRequest); return
	}
	if in.MaxAttempts == 0 { in.MaxAttempts = 8 }
	ctx := r.Context()
	var id string
	err := s.db.QueryRow(ctx,
		"insert into testagram_platform.jobs(queue,payload,idempotency_key,max_attempts,run_after) "+
		"values($1,$2::jsonb,nullif($3,''),$4,now()+($5 * interval '1 second')) "+
		"on conflict (queue,idempotency_key) where idempotency_key is not null "+
		"do update set idempotency_key=excluded.idempotency_key returning id::text",
		in.Queue, string(in.Payload), in.IdempotencyKey, in.MaxAttempts, in.DelaySeconds).Scan(&id)
	if err != nil { http.Error(w, "job queue unavailable", http.StatusServiceUnavailable); return }
	writeJSON(w, map[string]any{"id": id, "queue": in.Queue, "accepted": true})
}

func (s *server) jobClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
	if !s.authorize(w, r) { return }
	var in claimJobRequest
	if !decodeJSON(w, r, &in) || !validJobQueue(in.Queue) || !validKey(in.Worker) {
		http.Error(w, "invalid claim request", http.StatusBadRequest); return
	}
	if in.Count < 1 || in.Count > 25 { in.Count = 1 }
	if in.LeaseSeconds < 5 || in.LeaseSeconds > 3600 { in.LeaseSeconds = 60 }
	ctx := r.Context()
	// A worker may die on its final attempt. Close expired exhausted leases instead of
	// leaving them stuck forever in running state.
	if _, err := s.db.Exec(ctx, "update testagram_platform.jobs set status='dead',locked_by=null,locked_at=null,completed_at=now(),updated_at=now(),last_error=case when last_error='' then 'worker lease expired after final attempt' else last_error end where queue=$1 and status='running' and attempts>=max_attempts and locked_at < now()-($2 * interval '1 second')", in.Queue, in.LeaseSeconds); err != nil {
		http.Error(w, "job queue unavailable", http.StatusServiceUnavailable); return
	}
	// Expired leases become eligible again. Attempts are incremented atomically when a
	// worker receives the job; delivery is at-least-once, never exactly-once.
	rows, err := s.db.Query(ctx,
		"with candidates as ("+
		" select id from testagram_platform.jobs "+
		" where queue=$1 and attempts < max_attempts and run_after <= now() and "+
		" (status='queued' or (status='running' and locked_at < now()-($3 * interval '1 second'))) "+
		" order by run_after,created_at limit $4 for update skip locked"+
		") update testagram_platform.jobs j set status='running', attempts=j.attempts+1,"+
		" locked_by=$2,locked_at=now(),updated_at=now() from candidates c where j.id=c.id "+
		" returning j.id::text,j.queue,j.payload,j.attempts,j.max_attempts,j.locked_by",
		in.Queue, in.Worker, in.LeaseSeconds, in.Count)
	if err != nil { http.Error(w, "job queue unavailable", http.StatusServiceUnavailable); return }
	defer rows.Close()
	items := make([]durableJob, 0)
	for rows.Next() {
		var item durableJob
		if err := rows.Scan(&item.ID,&item.Queue,&item.Payload,&item.Attempts,&item.MaxAttempts,&item.LockedBy); err != nil {
			http.Error(w, "job claim decode failed", http.StatusInternalServerError); return
		}
		items = append(items,item)
	}
	if err := rows.Err(); err != nil { http.Error(w, "job queue unavailable", http.StatusServiceUnavailable); return }
	writeJSON(w, map[string]any{"jobs": items, "count": len(items), "lease_seconds": in.LeaseSeconds})
}

func (s *server) jobAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
	if !s.authorize(w, r) { return }
	var in finishJobRequest
	if !decodeJSON(w, r, &in) || !validUUID(in.ID) || !validKey(in.Worker) {
		http.Error(w, "invalid job acknowledgement", http.StatusBadRequest); return
	}
	tag, err := s.db.Exec(r.Context(),
		"update testagram_platform.jobs set status='succeeded',locked_by=null,locked_at=null,completed_at=now(),updated_at=now() "+
		"where id=$1::uuid and status='running' and locked_by=$2", in.ID, in.Worker)
	if err != nil { http.Error(w, "job acknowledgement unavailable", http.StatusServiceUnavailable); return }
	if tag.RowsAffected() != 1 { http.Error(w, "job lease not owned by worker", http.StatusConflict); return }
	writeJSON(w, map[string]any{"id":in.ID,"status":"succeeded"})
}

func (s *server) jobFail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
	if !s.authorize(w, r) { return }
	var in finishJobRequest
	if !decodeJSON(w, r, &in) || !validUUID(in.ID) || !validKey(in.Worker) ||
		len(in.Error) > 2000 || strings.ContainsAny(in.Error, "\x00") ||
		in.RetryAfterSeconds < 0 || in.RetryAfterSeconds > 604800 {
		http.Error(w, "invalid job failure report", http.StatusBadRequest); return
	}
	if strings.TrimSpace(in.Error)=="" { in.Error = "worker reported failure without details" }
	if in.RetryAfterSeconds == 0 { in.RetryAfterSeconds = 30 }
	var status string
	err := s.db.QueryRow(r.Context(),
		"update testagram_platform.jobs set "+
		"status=case when attempts >= max_attempts then 'dead' else 'queued' end,"+
		"run_after=now()+($3 * interval '1 second'),locked_by=null,locked_at=null,"+
		"last_error=$4,updated_at=now(),completed_at=case when attempts >= max_attempts then now() else null end "+
		"where id=$1::uuid and status='running' and locked_by=$2 returning status",
		in.ID,in.Worker,in.RetryAfterSeconds,in.Error).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) { http.Error(w, "job lease not owned by worker", http.StatusConflict); return }
	if err != nil { http.Error(w, "job failure report unavailable", http.StatusServiceUnavailable); return }
	writeJSON(w, map[string]any{"id":in.ID,"status":status})
}

func validJobQueue(v string) bool { return validKey(v) && len(v) <= 80 }
func validJSONObject(v json.RawMessage) bool {
	if len(v)==0 || len(v)>512<<10 || !json.Valid(v) { return false }
	var object map[string]json.RawMessage
	return json.Unmarshal(v,&object)==nil && object!=nil
}
func validUUID(v string) bool {
	if len(v)!=36 { return false }
	for i,c := range v {
		if i==8||i==13||i==18||i==23 { if c!='-' { return false }; continue }
		if !((c>='0'&&c<='9')||(c>='a'&&c<='f')||(c>='A'&&c<='F')) { return false }
	}
	return true
}
