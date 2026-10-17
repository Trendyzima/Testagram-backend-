package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type server struct {
	db *pgxpool.Pool
	redis *redis.Client
	internal string
	ready atomic.Bool
	cacheHits atomic.Uint64
	cacheMiss atomic.Uint64
	queueAdds atomic.Uint64
	queueAcks atomic.Uint64
	rateChecks atomic.Uint64
}

type cacheRequest struct {
	Key string `json:"key"`
	Value string `json:"value"`
	TTL int `json:"ttl_seconds"`
}
type queueRequest struct {
	Queue string `json:"queue"`
	Payload map[string]string `json:"payload"`
	Group string `json:"group"`
	Consumer string `json:"consumer"`
	Count int64 `json:"count"`
}
type rateRequest struct {
	Key string `json:"key"`
	Limit int64 `json:"limit"`
	Window int64 `json:"window_seconds"`
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
	redisURL := env("REDIS_URL", "redis://redis:6379/0")
	internal := required("PLATFORM_INTERNAL_TOKEN")
	db, err := pgxpool.New(ctx, dbURL); if err != nil { log.Fatal(err) }; defer db.Close()
	opts, err := redis.ParseURL(redisURL); if err != nil { log.Fatal(err) }
	rdb := redis.NewClient(opts); defer rdb.Close()
	s := &server{db: db, redis: rdb, internal: internal}; s.ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/metrics", s.metrics)
	mux.HandleFunc("/v1/cache/get", s.cacheGet)
	mux.HandleFunc("/v1/cache/set", s.cacheSet)
	mux.HandleFunc("/v1/cache/delete", s.cacheDelete)
	mux.HandleFunc("/v1/rate-limit/check", s.rateLimit)
	mux.HandleFunc("/v1/queue/enqueue", s.queueEnqueue)
	mux.HandleFunc("/v1/queue/claim", s.queueClaim)
	mux.HandleFunc("/v1/queue/ack", s.queueAck)
	mux.HandleFunc("/v1/queue/reclaim", s.queueReclaim)
	mux.HandleFunc("/v1/config/set", s.configSet)
	mux.HandleFunc("/v1/events", s.events)
	mux.HandleFunc("/v1/audit", s.audit)
	mux.HandleFunc("/v1/config/", s.publicConfig)

	addr := env("PLATFORM_ADDR", ":8095")
	log.Printf("testagram platform listening on %s", addr)
	log.Fatal((&http.Server{Addr:addr, Handler:requestLog(mux), ReadHeaderTimeout:5*time.Second, ReadTimeout:15*time.Second, WriteTimeout:30*time.Second, IdleTimeout:60*time.Second}).ListenAndServe())
}
func (s *server) health(w http.ResponseWriter,_ *http.Request){w.WriteHeader(200);_,_=w.Write([]byte("ok\n"))}
func (s *server) readyz(w http.ResponseWriter,r *http.Request){if !s.ready.Load(){http.Error(w,"not ready",503);return};if err:=s.db.Ping(r.Context());err!=nil{http.Error(w,"database unavailable",503);return};if err:=s.redis.Ping(r.Context()).Err();err!=nil{http.Error(w,"redis unavailable",503);return};_,_=w.Write([]byte("ready\n"))}
func (s *server) metrics(w http.ResponseWriter,_ *http.Request){w.Header().Set("Content-Type","text/plain; version=0.0.4");fmt.Fprintf(w,"testagram_platform_cache_hits_total %d\n",s.cacheHits.Load());fmt.Fprintf(w,"testagram_platform_cache_misses_total %d\n",s.cacheMiss.Load());fmt.Fprintf(w,"testagram_platform_queue_enqueued_total %d\n",s.queueAdds.Load());fmt.Fprintf(w,"testagram_platform_queue_acked_total %d\n",s.queueAcks.Load());fmt.Fprintf(w,"testagram_platform_rate_limit_checks_total %d\n",s.rateChecks.Load())}
func (s *server) authorize(w http.ResponseWriter,r *http.Request)bool{got:=strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"),"Bearer "));if got==""||subtle.ConstantTimeCompare([]byte(got),[]byte(s.internal))!=1{http.Error(w,"unauthorized",401);return false};return true}
func (s *server) cacheGet(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};key:=r.URL.Query().Get("key");if !validKey(key){http.Error(w,"invalid key",400);return};v,err:=s.redis.Get(r.Context(),key).Result();if errors.Is(err,redis.Nil){s.cacheMiss.Add(1);http.Error(w,"not found",404);return};if err!=nil{http.Error(w,"cache unavailable",503);return};s.cacheHits.Add(1);writeJSON(w,map[string]string{"value":v})}
func (s *server) cacheSet(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in cacheRequest;if !decodeJSON(w,r,&in)||!validKey(in.Key)||in.TTL<1||in.TTL>86400{http.Error(w,"invalid request",400);return};if err:=s.redis.Set(r.Context(),in.Key,in.Value,time.Duration(in.TTL)*time.Second).Err();err!=nil{http.Error(w,"cache unavailable",503);return};w.WriteHeader(204)}
func (s *server) cacheDelete(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};key:=r.URL.Query().Get("key");if !validKey(key){http.Error(w,"invalid key",400);return};if err:=s.redis.Del(r.Context(),key).Err();err!=nil{http.Error(w,"cache unavailable",503);return};w.WriteHeader(204)}

var rateScript=redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then redis.call("EXPIRE", KEYS[1], ARGV[1]) end
if current > tonumber(ARGV[2]) then return {0, current, redis.call("TTL", KEYS[1])} end
return {1, current, redis.call("TTL", KEYS[1])}
`)
func (s *server) rateLimit(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in rateRequest;if !decodeJSON(w,r,&in)||!validKey(in.Key)||in.Limit<1||in.Limit>100000||in.Window<1||in.Window>86400{http.Error(w,"invalid request",400);return};s.rateChecks.Add(1);v,err:=rateScript.Run(r.Context(),s.redis,[]string{"testagram:rl:"+in.Key},in.Window,in.Limit).Result();if err!=nil{http.Error(w,"rate limiter unavailable",503);return};a,ok:=v.([]interface{});if !ok||len(a)!=3{http.Error(w,"invalid limiter response",500);return};allowed,_:=strconv.ParseInt(fmt.Sprint(a[0]),10,64);count,_:=strconv.ParseInt(fmt.Sprint(a[1]),10,64);ttl,_:=strconv.ParseInt(fmt.Sprint(a[2]),10,64);writeJSON(w,map[string]any{"allowed":allowed==1,"count":count,"remaining":max64(in.Limit-count,0),"retry_after_seconds":max64(ttl,0)})}

func (s *server) queueEnqueue(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in queueRequest;if !decodeJSON(w,r,&in)||!validKey(in.Queue)||len(in.Payload)==0{http.Error(w,"invalid request",400);return};fields:=make([]any,0,len(in.Payload)*2);for k,v:=range in.Payload{if !validKey(k){http.Error(w,"invalid payload key",400);return};fields=append(fields,k,v)};id,err:=s.redis.XAdd(r.Context(),&redis.XAddArgs{Stream:"testagram:queue:"+in.Queue,MaxLen:100000,Approx:true,Values:fields}).Result();if err!=nil{http.Error(w,"queue unavailable",503);return};s.queueAdds.Add(1);writeJSON(w,map[string]string{"id":id})}
func (s *server) queueClaim(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in queueRequest;if !decodeJSON(w,r,&in)||!validKey(in.Queue)||!validKey(in.Group)||!validKey(in.Consumer){http.Error(w,"invalid request",400);return};if in.Count<1||in.Count>100{in.Count=10};stream:="testagram:queue:"+in.Queue;if err:=s.redis.XGroupCreateMkStream(r.Context(),stream,in.Group,"$").Err();err!=nil&&!strings.Contains(err.Error(),"BUSYGROUP"){http.Error(w,"queue unavailable",503);return};entries,err:=s.redis.XReadGroup(r.Context(),&redis.XReadGroupArgs{Group:in.Group,Consumer:in.Consumer,Streams:[]string{stream,">"},Count:in.Count,Block:time.Second}).Result();if errors.Is(err,redis.Nil){writeJSON(w,map[string]any{"messages":[]any{}});return};if err!=nil{http.Error(w,"queue unavailable",503);return};writeJSON(w,entries)}
func (s *server) queueAck(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};queue:=r.URL.Query().Get("queue");group:=r.URL.Query().Get("group");id:=r.URL.Query().Get("id");if !validKey(queue)||!validKey(group)||id==""{http.Error(w,"invalid request",400);return};n,err:=s.redis.XAck(r.Context(),"testagram:queue:"+queue,group,id).Result();if err!=nil{http.Error(w,"queue unavailable",503);return};s.queueAcks.Add(uint64(n));writeJSON(w,map[string]int64{"acked":n})}


func (s *server) queueReclaim(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};queue:=r.URL.Query().Get("queue");group:=r.URL.Query().Get("group");consumer:=r.URL.Query().Get("consumer");if !validKey(queue)||!validKey(group)||!validKey(consumer){http.Error(w,"invalid request",400);return};idleMS,_:=strconv.ParseInt(r.URL.Query().Get("min_idle_ms"),10,64);if idleMS<1||idleMS>86400000{idleMS=30000};count,_:=strconv.ParseInt(r.URL.Query().Get("count"),10,64);if count<1||count>100{count=10};messages,start,err:=s.redis.XAutoClaim(r.Context(),&redis.XAutoClaimArgs{Stream:"testagram:queue:"+queue,Group:group,Consumer:consumer,MinIdle:time.Duration(idleMS)*time.Millisecond,Start:"0-0",Count:count}).Result();if err!=nil{http.Error(w,"queue unavailable",503);return};writeJSON(w,map[string]any{"messages":messages,"next_start":start})}

func (s *server) configSet(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in flagRequest;if !decodeJSON(w,r,&in)||!validKey(in.Key)||len(in.Value)==0||!json.Valid(in.Value){http.Error(w,"invalid request",400);return};_,err:=s.db.Exec(r.Context(),`insert into testagram_platform.feature_flags(key,enabled,public,value,version,updated_at) values($1,$2,$3,$4::jsonb,1,now()) on conflict(key) do update set enabled=excluded.enabled,public=excluded.public,value=excluded.value,version=testagram_platform.feature_flags.version+1,updated_at=now()`,in.Key,in.Enabled,in.Public,string(in.Value));if err!=nil{http.Error(w,"database unavailable",503);return};w.WriteHeader(http.StatusNoContent)}

func (s *server) events(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in eventRequest;if !decodeJSON(w,r,&in)||len(in.EventName)<1||len(in.EventName)>160||!json.Valid(in.Properties){http.Error(w,"invalid request",400);return};var userID any;if in.UserID!=nil{userID=*in.UserID};_,err:=s.db.Exec(r.Context(),`insert into testagram_platform.events(event_name,user_id,session_hash,properties) values($1,$2,$3,$4::jsonb)`,in.EventName,userID,in.SessionHash,string(in.Properties));if err!=nil{http.Error(w,"database unavailable",503);return};w.WriteHeader(http.StatusNoContent)}

func (s *server) audit(w http.ResponseWriter,r *http.Request){if !s.authorize(w,r){return};var in auditRequest;if !decodeJSON(w,r,&in)||len(in.Action)<1||len(in.Action)>160||len(in.ResourceType)<1||len(in.ResourceType)>160||!json.Valid(in.Metadata){http.Error(w,"invalid request",400);return};var actorID any;if in.ActorID!=nil{actorID=*in.ActorID};_,err:=s.db.Exec(r.Context(),`insert into testagram_platform.audit_events(actor_id,action,resource_type,resource_id,metadata) values($1,$2,$3,$4,$5::jsonb)`,actorID,in.Action,in.ResourceType,in.ResourceID,string(in.Metadata));if err!=nil{http.Error(w,"database unavailable",503);return};w.WriteHeader(http.StatusNoContent)}

func (s *server) publicConfig(w http.ResponseWriter,r *http.Request){key:=strings.TrimPrefix(r.URL.Path,"/v1/config/");if !validKey(key){http.Error(w,"invalid key",400);return};var value []byte;err:=s.db.QueryRow(r.Context(),`select value from testagram_platform.feature_flags where key=$1 and public=true and enabled=true`,key).Scan(&value);if errors.Is(err,pgx.ErrNoRows){http.Error(w,"not found",404);return};if err!=nil{http.Error(w,"database unavailable",503);return};w.Header().Set("Cache-Control","public, max-age=30, stale-while-revalidate=60");w.Header().Set("Content-Type","application/json");_,_=w.Write(value)}

func decodeJSON(w http.ResponseWriter,r *http.Request,v any)bool{r.Body=http.MaxBytesReader(w,r.Body,1<<20);defer r.Body.Close();d:=json.NewDecoder(r.Body);d.DisallowUnknownFields();return d.Decode(v)==nil}
func writeJSON(w http.ResponseWriter,v any){w.Header().Set("Content-Type","application/json");_=json.NewEncoder(w).Encode(v)}
func validKey(s string)bool{if len(s)<1||len(s)>160{return false};for _,c:=range s{if !(c=='-'||c=='_'||c=='.'||c==':'||(c>='a'&&c<='z')||(c>='A'&&c<='Z')||(c>='0'&&c<='9')){return false}};return true}
func max64(a,b int64)int64{if a>b{return a};return b}
func required(k string)string{v:=strings.TrimSpace(os.Getenv(k));if v==""{log.Fatalf("%s is required",k)};return v}
func env(k,d string)string{if v:=strings.TrimSpace(os.Getenv(k));v!=""{return v};return d}
func requestLog(next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){start:=time.Now();next.ServeHTTP(w,r);log.Printf("%s %s %s",r.Method,r.URL.Path,time.Since(start))})}
