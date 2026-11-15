package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const watchdogLeaseDuration = 10 * time.Minute

type UserImpulse struct {
	IdempotencyKey string `json:"idempotencyKey"`
	EventType string `json:"eventType"`
}

type ImpulseAck struct {
	ID int64 `json:"id"`
	Success bool `json:"success"`
	Error string `json:"error"`
}

func servicesForImpulse(event string) ([]string,bool) {
	switch event {
	case "login","feed_open":
		return []string{"algorithm"},true
	case "post_publish":
		return []string{"worker","notification"},true
	case "media_upload":
		return []string{"cdn","media","video-worker"},true
	case "media_view":
		return []string{"cdn","media"},true
	case "federation_interaction":
		return []string{"federation"},true
	default:
		return nil,false
	}
}

func (s *Server) watchdogIngressOnly(w http.ResponseWriter,r *http.Request) bool {
	if !equalSecret(bearer(r),s.watchdogIngressToken) {
		reply(w,http.StatusUnauthorized,map[string]string{"error":"watchdog ingress unauthorized"})
		return false
	}
	return true
}

func (s *Server) watchdogOnly(w http.ResponseWriter,r *http.Request) bool {
	if !equalSecret(bearer(r),s.watchdogToken) {
		reply(w,http.StatusUnauthorized,map[string]string{"error":"watchdog worker unauthorized"})
		return false
	}
	return true
}

func (s *Server) registerWatchdogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/watchdog/impulses",s.watchdogImpulses)
	mux.HandleFunc("/v1/watchdog/impulses/claim",s.watchdogClaimImpulses)
	mux.HandleFunc("/v1/watchdog/impulses/ack",s.watchdogAckImpulse)
	mux.HandleFunc("/v1/watchdog/services",s.watchdogServices)
}

// Only the trusted Backend API may enqueue an event, and it calls this after
// validating the existing user session and authorizing the product action.
func (s *Server) watchdogImpulses(w http.ResponseWriter,r *http.Request) {
	if !s.watchdogIngressOnly(w,r) { return }
	if r.Method!=http.MethodPost { methodNotAllowed(w); return }
	var in UserImpulse
	if err:=decodeJSON(w,r,&in);err!=nil {
		reply(w,http.StatusBadRequest,map[string]string{"error":"invalid json"});return
	}
	in.IdempotencyKey=strings.TrimSpace(in.IdempotencyKey)
	in.EventType=strings.TrimSpace(in.EventType)
	services,ok:=servicesForImpulse(in.EventType)
	if !ok || len(in.IdempotencyKey)<16 || len(in.IdempotencyKey)>128 || strings.ContainsAny(in.IdempotencyKey," \t\r\n") {
		reply(w,http.StatusBadRequest,map[string]string{"error":"a supported eventType and 16-128 character idempotencyKey are required"});return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	tx,err:=s.db.Begin(ctx)
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog queue unavailable"});return }
	defer func(){_ = tx.Rollback(context.Background())}()
	var id int64
	var status string
	err=tx.QueryRow(ctx,`INSERT INTO testagram_watchdog.impulses(idempotency_key,event_type,services)
		VALUES($1,$2,$3) ON CONFLICT(idempotency_key) DO NOTHING RETURNING id,status`,
		in.IdempotencyKey,in.EventType,services).Scan(&id,&status)
	inserted:=err==nil
	if err==pgx.ErrNoRows {
		var existingEvent string
		err=tx.QueryRow(ctx,`SELECT id,status,event_type FROM testagram_watchdog.impulses WHERE idempotency_key=$1`,
			in.IdempotencyKey).Scan(&id,&status,&existingEvent)
		if err==nil && existingEvent!=in.EventType {
			reply(w,http.StatusConflict,map[string]string{"error":"idempotency key already belongs to another event"});return
		}
	}
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog impulse could not be persisted"});return }
	if inserted {
		for _,service:=range services {
			_,err=tx.Exec(ctx,`INSERT INTO testagram_watchdog.service_leases(service_name,active_until,last_impulse_at,updated_at)
				VALUES($1,now()+interval '10 minutes',now(),now())
				ON CONFLICT(service_name) DO UPDATE SET
				active_until=greatest(testagram_watchdog.service_leases.active_until,excluded.active_until),
				last_impulse_at=now(),updated_at=now()`,service)
			if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"service activation lease could not be persisted"});return }
		}
	}
	if err=tx.Commit(ctx);err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog transaction commit failed"});return }
	reply(w,http.StatusAccepted,map[string]any{"id":id,"status":status,"services":services,"leaseSeconds":int(watchdogLeaseDuration.Seconds())})
}

func (s *Server) watchdogClaimImpulses(w http.ResponseWriter,r *http.Request) {
	if !s.watchdogOnly(w,r) { return }
	if r.Method!=http.MethodPost { methodNotAllowed(w);return }
	limit:=10
	if raw:=r.URL.Query().Get("limit");raw!="" {
		n,err:=strconv.Atoi(raw)
		if err!=nil || n<1 || n>50 { reply(w,http.StatusBadRequest,map[string]string{"error":"limit must be between 1 and 50"});return }
		limit=n
	}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	rows,err:=s.db.Query(ctx,`WITH picked AS (
		SELECT id FROM testagram_watchdog.impulses
		WHERE attempts<5 AND ((status='queued' AND available_at<=now()) OR (status='claimed' AND lease_until<now()))
		ORDER BY created_at ASC FOR UPDATE SKIP LOCKED LIMIT $1
	)
	UPDATE testagram_watchdog.impulses i SET status='claimed',attempts=i.attempts+1,
		lease_until=now()+interval '60 seconds',updated_at=now()
	FROM picked WHERE i.id=picked.id
	RETURNING i.id,i.event_type,i.services,i.attempts,i.lease_until`,limit)
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog queue claim failed"});return }
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var id int64;var event string;var services []string;var attempts int;var lease time.Time
		if err:=rows.Scan(&id,&event,&services,&attempts,&lease);err!=nil { reply(w,http.StatusInternalServerError,map[string]string{"error":"watchdog claim decode failed"});return }
		items=append(items,map[string]any{"id":id,"eventType":event,"services":services,"attempts":attempts,"leaseUntil":lease})
	}
	if err:=rows.Err();err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog queue read failed"});return }
	reply(w,http.StatusOK,map[string]any{"impulses":items,"count":len(items)})
}

func (s *Server) watchdogAckImpulse(w http.ResponseWriter,r *http.Request) {
	if !s.watchdogOnly(w,r) { return }
	if r.Method!=http.MethodPost { methodNotAllowed(w);return }
	var in ImpulseAck
	if err:=decodeJSON(w,r,&in);err!=nil || in.ID<1 || len(in.Error)>500 {
		reply(w,http.StatusBadRequest,map[string]string{"error":"valid impulse id and bounded result are required"});return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second);defer cancel()
	tag,err:=s.db.Exec(ctx,`UPDATE testagram_watchdog.impulses SET
		status=CASE WHEN $2 THEN 'completed' WHEN attempts<5 THEN 'queued' ELSE 'failed' END,
		available_at=CASE WHEN $2 THEN available_at ELSE now()+make_interval(secs=>least(300,power(2,least(attempts,8))::int)) END,
		last_error=$3,lease_until=NULL,updated_at=now()
		WHERE id=$1 AND status='claimed'`,in.ID,in.Success,strings.TrimSpace(in.Error))
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"watchdog result could not be persisted"});return }
	if tag.RowsAffected()!=1 { reply(w,http.StatusConflict,map[string]string{"error":"impulse is not currently claimed"});return }
	status:="queued"
	if in.Success { status="completed" } else {
		_ = s.db.QueryRow(ctx,"SELECT status FROM testagram_watchdog.impulses WHERE id=$1",in.ID).Scan(&status)
	}
	reply(w,http.StatusOK,map[string]any{"id":in.ID,"status":status})
}

func (s *Server) watchdogServices(w http.ResponseWriter,r *http.Request) {
	if !s.internalOnly(w,r) { return }
	if r.Method!=http.MethodGet { methodNotAllowed(w);return }
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second);defer cancel()
	rows,err:=s.db.Query(ctx,`SELECT service_name,active_until,last_impulse_at FROM testagram_watchdog.service_leases
		WHERE active_until>now() ORDER BY service_name`)
	if err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"service lease inventory unavailable"});return }
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var name string;var until,last time.Time
		if err:=rows.Scan(&name,&until,&last);err!=nil { reply(w,http.StatusInternalServerError,map[string]string{"error":"service lease decode failed"});return }
		items=append(items,map[string]any{"service":name,"activeUntil":until,"lastImpulseAt":last})
	}
	if err:=rows.Err();err!=nil { reply(w,http.StatusServiceUnavailable,map[string]string{"error":"service lease inventory unavailable"});return }
	reply(w,http.StatusOK,map[string]any{"services":items,"count":len(items)})
}
