package main

import (
 "net/http"
 "strconv"
 "strings"
 "github.com/jackc/pgx/v5"
)

func (s *service) searchPublic(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,503,errorBody("SEARCH_UNAVAILABLE","Search service is unavailable",id));return}
 q:=strings.TrimSpace(r.URL.Query().Get("q"));if q==""||len(q)>200{writeJSON(w,400,errorBody("INVALID_SEARCH_QUERY","Search query must contain 1 to 200 characters",id));return}
 kind:=r.URL.Query().Get("kind");if kind==""{kind="all"};switch kind{case "all","people","posts","hashtags","communities":default:writeJSON(w,400,errorBody("INVALID_SEARCH_KIND","Search kind is unsupported",id));return}
 limit:=20;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>50{n=50};limit=n}}
 offset:=0;if raw:=r.URL.Query().Get("offset");raw!=""{if n,e:=strconv.Atoi(raw);e==nil&&n>0{offset=n};if offset>10000{offset=10000}}
 userID:="";if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;userID,_,ok=s.socialAuth(w,r);if !ok{return}}
 users:=make([]map[string]any,0);hashtags:=make([]map[string]any,0);posts:=make([]map[string]any,0);communities:=make([]map[string]any,0)
 if kind=="all"||kind=="people" {
  rows,err:=s.db.Query(r.Context(),`SELECT user_id::text,username,display_name,bio,avatar_key,follower_count,protected_account FROM testagram_local.accounts
   WHERE account_status='active' AND discoverable_by_username AND NOT protected_account AND (username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%')
   ORDER BY follower_count DESC,username ASC LIMIT $2 OFFSET $3`,q,limit,offset)
  if err!=nil{writeJSON(w,503,errorBody("SEARCH_FAILED","People search failed",id));return}
  for rows.Next(){var uid,username,display,bio,avatar string;var followers int64;var protected bool
   if err=rows.Scan(&uid,&username,&display,&bio,&avatar,&followers,&protected);err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","People results could not be read",id));return}
   users=append(users,map[string]any{"id":uid,"user_id":uid,"username":username,"display_name":display,"bio":bio,"avatar_url":avatar,"follower_count":followers,"protected":protected})
  };if err=rows.Err();err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","People results could not be read",id));return};rows.Close()
 }
 if kind=="all"||kind=="hashtags" {
  rows,err:=s.db.Query(r.Context(),`SELECT id::text,tag,usage_count,follower_count,federated_post_count,last_used_at FROM testagram_local.hashtags WHERE tag ILIKE $1||'%' ORDER BY usage_count DESC,follower_count DESC,tag ASC LIMIT $2 OFFSET $3`,strings.TrimPrefix(strings.ToLower(q),"#"),limit,offset)
  if err!=nil{writeJSON(w,503,errorBody("SEARCH_FAILED","Hashtag search failed",id));return}
  for rows.Next(){var hid,tag string;var usage,followers,fed int64;var last any
   if err=rows.Scan(&hid,&tag,&usage,&followers,&fed,&last);err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","Hashtag results could not be read",id));return}
   hashtags=append(hashtags,map[string]any{"id":hid,"tag":tag,"usage_count":usage,"post_count":usage,"follower_count":followers,"federated_post_count":fed,"last_used_at":last})
  };if err=rows.Err();err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","Hashtag results could not be read",id));return};rows.Close()
 }
 if kind=="all"||kind=="posts" {
  rows,err:=s.db.Query(r.Context(),`SELECT p.id::text,p.author_id::text,p.text,p.created_at,p.likes_count,p.replies_count,a.username,a.display_name,a.avatar_key
   FROM testagram_local.posts p JOIN testagram_local.accounts a ON a.user_id=p.author_id
   WHERE p.deleted_at IS NULL AND p.visibility='public' AND a.account_status='active' AND a.discoverable_by_username AND NOT a.protected_account
   AND p.community_id IS NULL AND p.text ILIKE '%'||$1||'%'
   ORDER BY p.likes_count DESC,p.created_at DESC,p.id DESC LIMIT $2 OFFSET $3`,q,limit,offset)
  if err!=nil{writeJSON(w,503,errorBody("SEARCH_FAILED","Post search failed",id));return}
  for rows.Next(){var pid,author,content,username,display,avatar string;var created any;var likes,replies int64
   if err=rows.Scan(&pid,&author,&content,&created,&likes,&replies,&username,&display,&avatar);err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","Post results could not be read",id));return}
   profile:=map[string]any{"id":author,"username":username,"display_name":display,"avatar_url":avatar}
   posts=append(posts,map[string]any{"id":pid,"post_id":pid,"author_id":author,"user_id":author,"text":content,"content":content,"created_at":created,"likes_count":likes,"replies_count":replies,"user_profiles":profile})
  };if err=rows.Err();err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","Post results could not be read",id));return};rows.Close()
 }
 if kind=="all"||kind=="communities" {
  rows,err:=s.db.Query(r.Context(),`SELECT id::text,slug,name,description,member_count,icon_url FROM testagram_local.communities WHERE visibility='public' AND (name ILIKE '%'||$1||'%' OR slug ILIKE '%'||$1||'%') ORDER BY member_count DESC,created_at DESC LIMIT $2 OFFSET $3`,q,limit,offset)
  if err!=nil{writeJSON(w,503,errorBody("SEARCH_FAILED","Community search failed",id));return}
  for rows.Next(){var cid,slug,name,description,icon string;var members int64
   if err=rows.Scan(&cid,&slug,&name,&description,&members,&icon);err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","Community results could not be read",id));return}
   communities=append(communities,map[string]any{"id":cid,"slug":slug,"name":slug,"display_name":name,"description":description,"member_count":members,"icon_url":icon})
  };if err=rows.Err();err!=nil{rows.Close();writeJSON(w,503,errorBody("SEARCH_FAILED","Community results could not be read",id));return};rows.Close()
 }
 _=pgx.ErrNoRows
 writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"users":users,"hashtags":hashtags,"posts":posts,"communities":communities,"next_cursor":nil}})
}
