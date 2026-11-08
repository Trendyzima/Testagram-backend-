package main

import (
 "net/http"
 "strings"
 "strconv"
 "time"
 "github.com/jackc/pgx/v5"
)

type hashtagChallengeInput struct { Title string `json:"title"`; Description string `json:"description"`; Prize string `json:"prize"`; EndDate time.Time `json:"end_date"`; Hashtag string `json:"hashtag"` }

func (s *service) hashtagChallengesList(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGES_UNAVAILABLE","Challenge service is unavailable",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 activeOnly:=r.URL.Query().Get("active")!="false"
 rows,err:=s.db.Query(r.Context(),`SELECT c.id::text,c.title,c.description,c.prize,c.end_date,c.created_by::text,c.entry_count,c.is_active,c.created_at,h.id::text,h.tag,h.usage_count
 FROM testagram_local.hashtag_challenges c JOIN testagram_local.hashtags h ON h.id=c.hashtag_id
 WHERE (NOT $1::boolean OR (c.is_active AND c.end_date>now()))
 ORDER BY c.entry_count DESC,c.end_date ASC,c.created_at DESC LIMIT $2`,activeOnly,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGES_LIST_FAILED","Hashtag challenges could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var cid,title,description,prize,creator,hid,tag string;var end,created time.Time;var entries,usage int64;var active bool
  if err=rows.Scan(&cid,&title,&description,&prize,&end,&creator,&entries,&active,&created,&hid,&tag,&usage);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGES_LIST_FAILED","Challenge records could not be read",id));return}
  items=append(items,map[string]any{"id":cid,"title":title,"description":description,"prize":prize,"end_date":end,"created_by":creator,"entry_count":entries,"is_active":active,"created_at":created,"hashtags":map[string]any{"id":hid,"tag":tag,"usage_count":usage}})
 }
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) hashtagChallengeGet(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGES_UNAVAILABLE","Challenge service is unavailable",id));return}
 cid:=r.PathValue("challenge_id");if !socialUUIDPattern.MatchString(cid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHALLENGE_ID","Challenge ID is invalid",id));return}
 var item map[string]any
 err:=s.db.QueryRow(r.Context(),`SELECT jsonb_build_object('id',c.id,'title',c.title,'description',c.description,'prize',c.prize,'end_date',c.end_date,'created_by',c.created_by,'entry_count',c.entry_count,'is_active',c.is_active,'created_at',c.created_at,'hashtags',jsonb_build_object('id',h.id,'tag',h.tag,'usage_count',h.usage_count))
 FROM testagram_local.hashtag_challenges c JOIN testagram_local.hashtags h ON h.id=c.hashtag_id WHERE c.id=$1::uuid`,cid).Scan(&item)
 if err==pgx.ErrNoRows{http.NotFound(w,r);return};if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGE_LOOKUP_FAILED","Challenge could not be loaded",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":item})
}

func (s *service) hashtagChallengeCreate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"hashtag_challenge_create",5){return}
 r.Body=http.MaxBytesReader(w,r.Body,12<<10);defer r.Body.Close();var in hashtagChallengeInput
 if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHALLENGE","Challenge payload is invalid",id));return}
 in.Title=strings.TrimSpace(in.Title);in.Description=strings.TrimSpace(in.Description);in.Prize=strings.TrimSpace(in.Prize);tag:=strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.Hashtag),"#"))
 if len(in.Title)<3||len(in.Title)>120||len(in.Description)>2000||len(in.Prize)>500||!hashtagTagPattern.MatchString(tag)||in.EndDate.IsZero()||!in.EndDate.After(time.Now().UTC())||in.EndDate.After(time.Now().UTC().AddDate(1,0,0)){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHALLENGE","Challenge fields or end date are invalid",id));return}
 ctx:=r.Context();tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGE_CREATE_FAILED","Challenge could not be created",id));return};defer tx.Rollback(ctx)
 var hashtagID,challengeID string
 err=tx.QueryRow(ctx,`INSERT INTO testagram_local.hashtags(tag) VALUES($1) ON CONFLICT(tag) DO UPDATE SET updated_at=now() RETURNING id::text`,tag).Scan(&hashtagID)
 if err==nil{err=tx.QueryRow(ctx,`INSERT INTO testagram_local.hashtag_challenges(hashtag_id,created_by,title,description,prize,end_date)
 VALUES($1::uuid,$2::uuid,$3,$4,$5,$6) RETURNING id::text`,hashtagID,uid,in.Title,in.Description,in.Prize,in.EndDate.UTC()).Scan(&challengeID)}
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGE_CREATE_FAILED","Challenge could not be saved",id));return}
 if err=tx.Commit(ctx);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHALLENGE_CREATE_FAILED","Challenge could not be committed",id));return}
 writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":challengeID,"title":in.Title,"description":in.Description,"prize":in.Prize,"end_date":in.EndDate.UTC(),"created_by":uid,"entry_count":0,"is_active":true,"hashtags":map[string]any{"id":hashtagID,"tag":tag}}})
}
