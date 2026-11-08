package main

import (
 "net/http"
 "strconv"
 "strings"
 "time"
 "encoding/json"
)

func (s *service) discoveryCreators(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_UNAVAILABLE","Discovery service is unavailable",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 q:=strings.TrimSpace(r.URL.Query().Get("q"));if len(q)>100{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_DISCOVERY_QUERY","Search query is too long",id));return}
 rows,err:=s.db.Query(r.Context(),`SELECT user_id::text,username,display_name,avatar_key,bio,follower_count
 FROM testagram_local.accounts WHERE account_status='active' AND discoverable_by_username AND NOT protected_account
 AND ($1='' OR username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%')
 ORDER BY follower_count DESC,created_at DESC LIMIT $2`,q,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_CREATORS_FAILED","Creators could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var uid,username,display,avatar,bio string;var followers int64
  if err=rows.Scan(&uid,&username,&display,&avatar,&bio,&followers);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_CREATORS_FAILED","Creator records could not be read",id));return}
  items=append(items,map[string]any{"id":uid,"username":username,"display_name":display,"avatar_url":avatar,"bio":bio,"follower_count":followers,"verified":false,"verified_tier":nil})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_CREATORS_FAILED","Creator records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) discoveryPosts(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_UNAVAILABLE","Discovery service is unavailable",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 q:=strings.TrimSpace(r.URL.Query().Get("q"));if len(q)>200{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_DISCOVERY_QUERY","Search query is too long",id));return}
 sortBy:=r.URL.Query().Get("sort");order:="p.created_at DESC,p.id DESC";if sortBy=="top"{order="p.likes_count DESC,p.created_at DESC,p.id DESC"}
 since:=time.Now().UTC().Add(-48*time.Hour)
 query:=`SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,p.created_at,p.updated_at,p.likes_count,p.replies_count,p.reposts_count,
 a.username,a.display_name,a.avatar_key
 FROM testagram_local.posts p JOIN testagram_local.accounts a ON a.user_id=p.author_id
 WHERE p.deleted_at IS NULL AND p.visibility='public' AND p.community_id IS NULL AND p.created_at >= $1
 AND ($2='' OR p.text ILIKE '%'||$2||'%' OR a.username ILIKE '%'||$2||'%')
 ORDER BY `+order+` LIMIT $3`
 rows,err:=s.db.Query(r.Context(),query,since,q,limit);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_POSTS_FAILED","Posts could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var pid,uid,content,visibility,username,display,avatar string;var media []byte;var created,updated time.Time;var likes,replies,reposts,views int64
  if err=rows.Scan(&pid,&uid,&content,&visibility,&media,&created,&updated,&likes,&replies,&reposts,&username,&display,&avatar);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_POSTS_FAILED","Post records could not be read",id));return}
  var mediaIDs []string;_ = json.Unmarshal(media,&mediaIDs);profile:=map[string]any{"id":uid,"username":username,"display_name":display,"avatar_url":avatar,"verified":false}
  items=append(items,map[string]any{"id":pid,"user_id":uid,"author_id":uid,"content":content,"text":content,"visibility":visibility,"media_ids":mediaIDs,"media_urls":[]string{},"is_video":false,"created_at":created,"updated_at":updated,"likes_count":likes,"replies_count":replies,"reposts_count":reposts,"views_count":int64(0),"user_profiles":profile})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("DISCOVERY_POSTS_FAILED","Post records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}
