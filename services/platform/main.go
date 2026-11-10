package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	db           *pgxpool.Pool
	internal     string
	ready        atomic.Bool
	configWrites atomic.Uint64
	eventsStored atomic.Uint64
	auditWrites  atomic.Uint64
}

type flagRequest struct {
	Key string `json:"key"`
	Enabled bool `json:"enabled"`
	Public bool `json:"public"`
	Value json.RawMessage `json:"value"`
}
type eventRequest struct {
	EventName string `json:"event_name"`
	UserID *string `json:"user_id"`
	SessionHash string `json:"session_hash"`
	Properties json.RawMessage `json:"properties"`
}
type auditRequest struct {
	ActorID *string `json:"actor_id"`
	Action string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID string `json:"resource_id"`
	Metadata json.RawMessage `json:"metadata"`
}

func main() {
	ctx := context.Background()
	dbURL := required("DATABASE_URL")
	internal := required("PLATFORM_INTERNAL_TOKEN")
	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("platform PostgreSQL is unavailable: %v", err)
	}
	s := &server{db: db, internal: internal}
	s.ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/metrics", s.metrics)
	mux.HandleFunc("/v1/config/set", s.configSet)
	mux.HandleFunc("/v1/events", s.events)
	mux.HandleFunc("/v1/audit", s.audit)
	mux.HandleFunc("/v1/config/", s.publicConfig)

	addr := env("PLATFORM_ADDR", ":8095")
	log.Printf("testagram platform listening on %s", addr)
	log.Fatal((&http.Server{Addr:addr, Handler:requestLog(mux), ReadHeaderTimeout:5*time.Second, ReadTimeout:15*time.Second, WriteTimeout:30*time.Second, IdleTimeout:60*time.Second}).ListenAndServe())
}
func (s *server) health(w http.ResponseWriter,_ *http.Request){w.WriteHeader(200);_,_=w.Write([]byte("ok\n"))}
func (s *server) readyz(w http.ResponseWriter,r *http.Request){if !s.ready.Load(){http.Error(w,"not ready",503);return};ctx,cancel:=context.WithTimeout(r.Context(),2*time.Second);defer cancel();if err:=s.db.Ping(ctx);err!=nil{http.Error(w,"database unavailable",503);return};_,_=w.Write([]byte("ready\n"))}
func (s *server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "testagram_platform_config_writes_total %d\n", s.configWrites.Load())
	fmt.Fprintf(w, "testagram_platform_events_stored_total %d\n", s.eventsStored.Load())
	fmt.Fprintf(w, "testagram_platform_audit_writes_total %d\n", s.auditWrites.Load())
	if s.db != nil {
		stats := s.db.Stat()
		fmt.Fprintf(w, "# HELP testagram_platform_db_pool_total_connections Total PostgreSQL pool connections.\n# TYPE testagram_platform_db_pool_total_connections gauge\ntestagram_platform_db_pool_total_connections %d\n", stats.TotalConns())
		fmt.Fprintf(w, "# HELP testagram_platform_db_pool_idle_connections Idle PostgreSQL pool connections.\n# TYPE testagram_platform_db_pool_idle_connections gauge\ntestagram_platform_db_pool_idle_connections %d\n", stats.IdleConns())
		fmt.Fprintf(w, "# HELP testagram_platform_db_pool_acquired_connections Acquired PostgreSQL pool connections.\n# TYPE testagram_platform_db_pool_acquired_connections gauge\ntestagram_platform_db_pool_acquired_connections %d\n", stats.AcquiredConns())
		fmt.Fprintf(w, "# HELP testagram_platform_db_pool_max_connections Configured maximum PostgreSQL pool connections.\n# TYPE testagram_platform_db_pool_max_connections gauge\ntestagram_platform_db_pool_max_connections %d\n", stats.MaxConns())
	}
}
func (s *server) authorize(w http.ResponseWriter,r *http.Request)bool{got:=strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"),"Bearer "));if got==""||subtle.ConstantTimeCompare([]byte(got),[]byte(s.internal))!=1{http.Error(w,"unauthorized",401);return false};return true}
func (s *server) configSet(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in flagRequest;if !decodeJSON(w,r,&in)||!validKey(in.Key)||len(in.Value)==0||!json.Valid(in.Value){http.Error(w,"invalid request",400);return};_,err:=s.db.Exec(r.Context(),`insert into testagram_platform.feature_flags(key,enabled,public,value,version,updated_at) values($1,$2,$3,$4::jsonb,1,now()) on conflict(key) do update set enabled=excluded.enabled,public=excluded.public,value=excluded.value,version=testagram_platform.feature_flags.version+1,updated_at=now()`,in.Key,in.Enabled,in.Public,string(in.Value));if err!=nil{http.Error(w,"database unavailable",503);return};w.WriteHeader(http.StatusNoContent)}

func (s *server) events(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in eventRequest;if !decodeJSON(w,r,&in)||len(in.EventName)<1||len(in.EventName)>160||!json.Valid(in.Properties){http.Error(w,"invalid request",400);return};var userID any;if in.UserID!=nil{userID=*in.UserID};_,err:=s.db.Exec(r.Context(),`insert into testagram_platform.events(event_name,user_id,session_hash,properties) values($1,$2,$3,$4::jsonb)`,in.EventName,userID,in.SessionHash,string(in.Properties));if err!=nil{http.Error(w,"database unavailable",503);return};w.WriteHeader(http.StatusNoContent)}

func (s *server) audit(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in auditRequest;if !decodeJSON(w,r,&in)||len(in.Action)<1||len(in.Action)>160||len(in.ResourceType)<1||len(in.ResourceType)>160||!json.Valid(in.Metadata){http.Error(w,"invalid request",400);return};var actorID any;if in.ActorID!=nil{actorID=*in.ActorID};_,err:=s.db.Exec(r.Context(),`insert into testagram_platform.audit_events(actor_id,action,resource_type,resource_id,metadata) values($1,$2,$3,$4,$5::jsonb)`,actorID,in.Action,in.ResourceType,in.ResourceID,string(in.Metadata));if err!=nil{http.Error(w,"database unavailable",503);return};w.WriteHeader(http.StatusNoContent)}

func (s *server) publicConfig(w http.ResponseWriter,r *http.Request){key:=strings.TrimPrefix(r.URL.Path,"/v1/config/");if !validKey(key){http.Error(w,"invalid key",400);return};var value []byte;err:=s.db.QueryRow(r.Context(),`select value from testagram_platform.feature_flags where key=$1 and public=true and enabled=true`,key).Scan(&value);if errors.Is(err,pgx.ErrNoRows){http.Error(w,"not found",404);return};if err!=nil{http.Error(w,"database unavailable",503);return};w.Header().Set("Cache-Control","public, max-age=30, stale-while-revalidate=60");w.Header().Set("Content-Type","application/json");_,_=w.Write(value)}

func decodeJSON(w http.ResponseWriter,r *http.Request,v any)bool{r.Body=http.MaxBytesReader(w,r.Body,1<<20);defer r.Body.Close();d:=json.NewDecoder(r.Body);d.DisallowUnknownFields();if err:=d.Decode(v);err!=nil{return false};var trailing any;if err:=d.Decode(&trailing);err!=io.EOF{return false};return true}
func writeJSON(w http.ResponseWriter,v any){w.Header().Set("Content-Type","application/json");_=json.NewEncoder(w).Encode(v)}
func validQueuePayload(payload map[string]string) bool {
 if len(payload)==0 || len(payload)>64 { return false }
 raw,err:=json.Marshal(payload)
 return err==nil && len(raw)<=256*1024
}

func validKey(s string)bool{if len(s)<1||len(s)>160{return false};for _,c:=range s{if !(c=='-'||c=='_'||c=='.'||c==':'||(c>='a'&&c<='z')||(c>='A'&&c<='Z')||(c>='0'&&c<='9')){return false}};return true}
func required(k string)string{v:=strings.TrimSpace(os.Getenv(k));if v==""{log.Fatalf("%s is required",k)};return v}
func env(k,d string)string{if v:=strings.TrimSpace(os.Getenv(k));v!=""{return v};return d}
func requestLog(next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){start:=time.Now();next.ServeHTTP(w,r);log.Printf("%s %s %s",r.Method,r.URL.Path,time.Since(start))})}
