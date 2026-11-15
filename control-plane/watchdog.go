package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/crypto"
	"github.com/jackc/pgx/v5/pgconn"
)

const watchdogLeaseDuration = 10 * time.Minute

type UserImpulse struct {
	IdempotencyKey string `json:"idempotencyKey"`
	EventType      string `json:"eventType"`
}

type ImpulseAck struct {
	ID      int64  `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// Service mapping is deliberately allowlisted. The always-on Backend remains
// available to authenticate users and accept impulses; activity wakes only
// optional downstream services.
func servicesForImpulse(event string) ([]string, bool) {
	switch event {
	case "login":
		return []string{"algorithm"}, true
	case "feed_open":
		return []string{"algorithm"}, true
	case "post_publish":
		return []string{"data-centre"}, true
	case "media_upload", "media_view":
		return []string{"cdn", "data-centre"}, true
	case "federation_interaction":
		return []string{"federation"}, true
	default:
		return nil, false
	}
}

func (s *Server) watchdogOnly(w http.ResponseWriter, r *http.Request) bool {
	if !equalSecret(bearer(r), s.watchdogToken) {
		reply(w, http.StatusUnauthorized, map[string]string{"error":"watchdog worker unauthorized"})
		return false
	}
	return true
}

func (s *Server) registerWatchdogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/watchdog/impulses", s.watchdogImpulses)
	mux.HandleFunc("/v1/watchdog/impulses/claim", s.watchdogClaimImpulses)
	mux.HandleFunc("/v1/watchdog/impulses/ack", s.watchdogAckImpulse)
	mux.HandleFunc("/v1/nodes/watchdog/claim", s.watchdogNodeClaimImpulses)
	mux.HandleFunc("/v1/nodes/watchdog/ack", s.watchdogNodeAckImpulse)
	mux.HandleFunc("/v1/watchdog/services", s.watchdogServices)
}

// This endpoint is internal-only. The public API/backend must call it only
// after it has independently validated the user's session and authorization.
func (s *Server) watchdogImpulses(w http.ResponseWriter, r *http.Request) {
	if !s.internalOnly(w, r) { return }
	if r.Method != http.MethodPost { methodNotAllowed(w); return }
	var in UserImpulse
	if err := decodeJSON(w, r, &in); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error":"invalid json"})
		return
	}
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	services, ok := servicesForImpulse(strings.TrimSpace(in.EventType))
	if !ok || len(in.IdempotencyKey) < 16 || len(in.IdempotencyKey) > 128 || strings.ContainsAny(in.IdempotencyKey, " \t\r\n") {
		reply(w, http.StatusBadRequest, map[string]string{"error":"a supported eventType and 16-128 character idempotencyKey are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil { reply(w, http.StatusServiceUnavailable, map[string]string{"error":"watchdog queue unavailable"}); return }
	defer func(){ _ = tx.Rollback(context.Background()) }()
	var id int64
	var status string
	err = tx.QueryRow(ctx, `insert into testagram_watchdog.impulses(idempotency_key,event_type,services)
		values($1,$2,$3) on conflict(idempotency_key) do nothing returning id,status`,
		in.IdempotencyKey, in.EventType, services).Scan(&id,&status)
	inserted := err == nil
	if err == pgx.ErrNoRows {
		var existingEvent string
		err = tx.QueryRow(ctx, `select id,status,event_type from testagram_watchdog.impulses where idempotency_key=$1`,
			in.IdempotencyKey).Scan(&id,&status,&existingEvent)
		if err == nil && existingEvent != in.EventType {
			reply(w,http.StatusConflict,map[string]string{"error":"idempotency key was already used for a different event"})
			return
		}
	}
	if err != nil { reply(w, http.StatusServiceUnavailable, map[string]string{"error":"watchdog impulse could not be persisted"}); return }
	// Extend leases only for a newly inserted event. A duplicate retry never
	// refreshes a lease and cannot keep a service awake indefinitely.
	if inserted {
		for _, service := range services {
			_, err = tx.Exec(ctx, `insert into testagram_watchdog.service_leases(service_name,active_until,last_impulse_at,updated_at)
				values($1,now()+interval '10 minutes',now(),now())
				on conflict(service_name) do update set
				active_until=greatest(testagram_watchdog.service_leases.active_until,excluded.active_until),
				last_impulse_at=now(),updated_at=now()`, service)
			if err != nil { reply(w, http.StatusServiceUnavailable, map[string]string{"error":"service activation lease could not be persisted"}); return }
		}
	}
	if err = tx.Commit(ctx); err != nil { reply(w, http.StatusServiceUnavailable, map[string]string{"error":"watchdog impulse commit failed"}); return }
	reply(w, http.StatusAccepted, map[string]any{"id":id,"status":status,"services":services,"leaseSeconds":int(watchdogLeaseDuration.Seconds())})
}

// Trusted node/activation workers claim queued impulses with a short lease.
// SKIP LOCKED makes concurrent workers safe; expired claims are retryable.
func (s *Server) watchdogNodeID(r *http.Request) (string,bool) {
	token:=bearer(r)
	if token=="" { return "",false }
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
	var id string
	err:=s.db.QueryRow(ctx,`select id::text from public.vps_nodes
		where token_hash=$1 and status='online' and last_seen_at>now()-interval '45 seconds'
		and capabilities @> '{"watchdogActivation":true}'::jsonb`,crypto.HashToken(token)).Scan(&id)
	if err!=nil { return "",false }
	return id,true
}

func (s *Server) watchdogClaimImpulses(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	s.claimWatchdogImpulses(w,r,"")
}

func (s *Server) watchdogNodeClaimImpulses(w http.ResponseWriter,r *http.Request) {
	if r.Method!=http.MethodPost { methodNotAllowed(w); return }
	nodeID,ok:=s.watchdogNodeID(r)
	if !ok { reply(w,http.StatusUnauthorized,map[string]string{"error":"watchdog-capable node authentication required"}); return }
	s.claimWatchdogImpulses(w,r,nodeID)
}

func (s *Server) claimWatchdogImpulses(w http.ResponseWriter,r *http.Request,nodeID string) {
	if r.Method!=http.MethodPost { methodNotAllowed(w); return }
	limit:=10
	if raw:=r.URL.Query().Get("limit"); raw!="" {
		n,err:=strconv.Atoi(raw)
		if err!=nil || n<1 || n>50 { reply(w,http.StatusBadRequest,map[string]string{"error":"limit must be between 1 and 50"}); return }
		limit=n
	}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	rows,err:=s.db.Query(ctx,`with picked as (
		select id from testagram_watchdog.impulses
		where ((status='queued' and available_at<=now())
			or (status='claimed' and lease_until<now() and attempts<5))
		order by created_at asc for update skip locked limit $1
	)
	update testagram_watchdog.impulses i set status='claimed',attempts=i.attempts+1,
		lease_until=now()+interval '60 seconds',claimed_by_node_id=nullif($2,'')::uuid,updated_at=now()
	from picked where i.id=picked.id
	returning i.id,i.event_type,i.services,i.attempts,i.lease_until`,limit,nodeID)
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog queue claim failed"}); return }
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var id int64; var event string; var services []string; var attempts int; var lease time.Time
		if err:=rows.Scan(&id,&event,&services,&attempts,&lease); err!=nil { reply(w,http.StatusInternalServerError,map[string]string{"error":"watchdog queue decode failed"}); return }
		items=append(items,map[string]any{"id":id,"eventType":event,"services":services,"attempts":attempts,"leaseUntil":lease})
	}
	if err:=rows.Err(); err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog queue read failed"}); return }
	reply(w,http.StatusOK,map[string]any{"impulses":items,"count":len(items)})
}

func (s *Server) watchdogAckImpulse(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	s.ackWatchdogImpulse(w,r,"")
}

func (s *Server) watchdogNodeAckImpulse(w http.ResponseWriter,r *http.Request) {
	if r.Method!=http.MethodPost { methodNotAllowed(w); return }
	nodeID,ok:=s.watchdogNodeID(r)
	if !ok { reply(w,http.StatusUnauthorized,map[string]string{"error":"watchdog-capable node authentication required"}); return }
	s.ackWatchdogImpulse(w,r,nodeID)
}

func (s *Server) ackWatchdogImpulse(w http.ResponseWriter,r *http.Request,nodeID string) {
	if r.Method!=http.MethodPost { methodNotAllowed(w); return }
	var in ImpulseAck
	if err:=decodeJSON(w,r,&in); err!=nil || in.ID<1 || len(in.Error)>500 {
		reply(w,http.StatusBadRequest,map[string]string{"error":"valid impulse id and bounded result are required"}); return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
	var tag pgconn.CommandTag
	var err error
	if nodeID=="" {
		tag,err=s.db.Exec(ctx,`update testagram_watchdog.impulses set
			status=case when $2 then 'completed' when attempts<5 then 'queued' else 'failed' end,
			available_at=case when $2 then available_at else now()+make_interval(secs=>least(300,power(2,least(attempts,8))::int)) end,
			last_error=$3,lease_until=null,claimed_by_node_id=null,updated_at=now()
			where id=$1 and status='claimed' and claimed_by_node_id is null`,in.ID,in.Success,strings.TrimSpace(in.Error))
	} else {
		tag,err=s.db.Exec(ctx,`update testagram_watchdog.impulses set
			status=case when $2 then 'completed' when attempts<5 then 'queued' else 'failed' end,
			available_at=case when $2 then available_at else now()+make_interval(secs=>least(300,power(2,least(attempts,8))::int)) end,
			last_error=$3,lease_until=null,claimed_by_node_id=null,updated_at=now()
			where id=$1 and status='claimed' and claimed_by_node_id=$4::uuid`,in.ID,in.Success,strings.TrimSpace(in.Error),nodeID)
	}
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog result could not be persisted"}); return }
	if tag.RowsAffected()!=1 { reply(w,http.StatusConflict,map[string]string{"error":"impulse is not currently claimed by this worker"}); return }
	status:="queued"; if in.Success { status="completed" } else {
		var current string
		_ = s.db.QueryRow(ctx,`select status from testagram_watchdog.impulses where id=$1`,in.ID).Scan(&current)
		status=current
	}
	if !in.Success {
		var finalStatus string
		if err := s.db.QueryRow(ctx, "select status from testagram_watchdog.impulses where id=$1", in.ID).Scan(&finalStatus); err == nil { status = finalStatus }
	}
	reply(w,http.StatusOK,map[string]any{"id":in.ID,"status":status})
}

func (s *Server) watchdogServices(w http.ResponseWriter, r *http.Request) {
	if !s.internalOnly(w,r) { return }
	if r.Method!=http.MethodGet { methodNotAllowed(w); return }
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
	rows,err:=s.db.Query(ctx,`select service_name,active_until,last_impulse_at from testagram_watchdog.service_leases
		where active_until>now() order by service_name`)
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"service lease inventory unavailable"}); return }
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var name string; var until,last time.Time
		if err:=rows.Scan(&name,&until,&last); err!=nil { reply(w,http.StatusInternalServerError,map[string]string{"error":"service lease decode failed"}); return }
		items=append(items,map[string]any{"service":name,"activeUntil":until,"lastImpulseAt":last})
	}
	if err:=rows.Err(); err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"service lease inventory unavailable"}); return }
	reply(w,http.StatusOK,map[string]any{"services":items,"count":len(items)})
}

