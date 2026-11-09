package main

import (
  "context"
  "encoding/base64"
  "encoding/json"
  "net/http"
  "strconv"

  "github.com/jackc/pgx/v5"
  "strings"
  "time"
)

func (s *service) threadsList(w http.ResponseWriter,r *http.Request) {
  rid:=requestID(r)
  if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",rid));return}
  if s.db==nil{writeJSON(w,503,errorBody("SOCIAL_DATABASE_UNAVAILABLE","Social data service is unavailable",rid));return}
  tab:=strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")));if tab==""{tab="for-you"}
  if tab!="for-you"&&tab!="following"&&tab!="saved"{writeJSON(w,400,errorBody("INVALID_THREAD_TAB","Thread tab is invalid",rid));return}
  userID:=""
  if tab!="for-you"||strings.TrimSpace(r.Header.Get("Authorization"))!="" {
    var ok bool
    userID,rid,ok=s.socialAuth(w,r)
    if !ok{return}
  }
  if (tab=="following"||tab=="saved")&&userID==""{writeJSON(w,401,errorBody("AUTH_REQUIRED","Sign in to view this thread list",rid));return}
  limit:=6
  if raw:=r.URL.Query().Get("limit");raw!=""{n,err:=strconv.Atoi(raw);if err!=nil||n<1||n>30{writeJSON(w,400,errorBody("INVALID_LIMIT","Limit must be between 1 and 30",rid));return};limit=n}
  cursor:=strings.TrimSpace(r.URL.Query().Get("cursor"))
  cursorTime,cursorID:="",""
  if cursor!=""{
    decoded,decodeErr:=base64.RawURLEncoding.DecodeString(cursor)
    if decodeErr!=nil{writeJSON(w,400,errorBody("INVALID_CURSOR","Thread cursor is invalid",rid));return}
    parts:=strings.SplitN(string(decoded),"|",2)
    if len(parts)!=2||!socialUUIDPattern.MatchString(parts[1]){writeJSON(w,400,errorBody("INVALID_CURSOR","Thread cursor is invalid",rid));return}
    cursorTime,cursorID=parts[0],strings.ToLower(parts[1])
    if _,timeErr:=time.Parse(time.RFC3339Nano,cursorTime);timeErr!=nil{writeJSON(w,400,errorBody("INVALID_CURSOR","Thread cursor timestamp is invalid",rid));return}
  }
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  var rows pgx.Rows
  var err error
  baseSelect:=`SELECT t.id::text,t.owner_id::text,t.body,t.visibility,t.created_at,t.media_urls,
      t.likes_count,t.reposts_count,t.quotes_count,t.replies_count,t.views_count,
      a.username,a.display_name,a.avatar_key
    FROM testagram_local.threads t
    JOIN testagram_local.accounts a ON a.user_id=t.owner_id
    WHERE t.deleted_at IS NULL AND a.account_status='active'`
  switch tab {
  case "following":
    if cursor!="" { rows,err=s.db.Query(ctx,baseSelect+` AND t.visibility='public' AND EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=t.owner_id AND f.status='accepted') AND (t.created_at,t.id)<($2::timestamptz,$3::uuid) ORDER BY t.created_at DESC,t.id DESC LIMIT $4`,userID,cursorTime,cursorID,limit+1) } else { rows,err=s.db.Query(ctx,baseSelect+` AND EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=t.owner_id AND f.status='accepted') ORDER BY t.created_at DESC,t.id DESC LIMIT $2`,userID,limit+1) }
  case "saved":
    if cursor!="" { rows,err=s.db.Query(ctx,baseSelect+` AND (t.visibility IN ('public','unlisted') OR t.owner_id=$1) AND EXISTS(SELECT 1 FROM testagram_local.thread_bookmarks b WHERE b.thread_id=t.id AND b.user_id=$1 AND (t.created_at,t.id)<($2::timestamptz,$3::uuid)) ORDER BY t.created_at DESC,t.id DESC LIMIT $4`,userID,cursorTime,cursorID,limit+1) } else { rows,err=s.db.Query(ctx,baseSelect+` AND (t.visibility IN ('public','unlisted') OR t.owner_id=$1) AND EXISTS(SELECT 1 FROM testagram_local.thread_bookmarks b WHERE b.thread_id=t.id AND b.user_id=$1) ORDER BY t.created_at DESC,t.id DESC LIMIT $2`,userID,limit+1) }
  default:
    if cursor!="" { rows,err=s.db.Query(ctx,baseSelect+` AND t.visibility='public' AND (t.created_at,t.id)<($1::timestamptz,$2::uuid) ORDER BY t.created_at DESC,t.id DESC LIMIT $3`,cursorTime,cursorID,limit+1) } else { rows,err=s.db.Query(ctx,baseSelect+` AND t.visibility='public' ORDER BY t.created_at DESC,t.id DESC LIMIT $1`,limit+1) }
  }
  if err!=nil{writeJSON(w,503,errorBody("THREAD_LIST_FAILED","Threads could not be loaded",rid));return}
  defer rows.Close()
  items:=make([]map[string]any,0,limit+1)
  for rows.Next(){
    var id,owner,body,visibility,username,displayName,avatarKey string
    var created time.Time
    var mediaJSON []byte
    var likes,reposts,quotes,replies,views int64
    if err:=rows.Scan(&id,&owner,&body,&visibility,&created,&mediaJSON,&likes,&reposts,&quotes,&replies,&views,&username,&displayName,&avatarKey);err!=nil{writeJSON(w,503,errorBody("THREAD_LIST_FAILED","Thread rows could not be decoded",rid));return}
    media:=make([]any,0);if len(mediaJSON)>0{_ = json.Unmarshal(mediaJSON,&media)}
    avatarURL:="";if avatarKey!=""{avatarURL=publicMediaURL(avatarKey)}
    items=append(items,map[string]any{"id":id,"owner_id":owner,"body":body,"visibility":visibility,"created_at":created,"media_urls":media,"likes_count":likes,"reposts_count":reposts,"quotes_count":quotes,"replies_count":replies,"views_count":views,"profiles":map[string]any{"id":owner,"username":username,"display_name":displayName,"avatar_url":avatarURL,"verified":false}})
  }
  if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("THREAD_LIST_FAILED","Thread list could not be read",rid));return}
  hasMore:=len(items)>limit;if hasMore{items=items[:limit]}
  next:="";if len(items)>0{last:=items[len(items)-1];next=base64.RawURLEncoding.EncodeToString([]byte(last["created_at"].(time.Time).Format(time.RFC3339Nano)+"|"+last["id"].(string)))}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":next,"has_more":hasMore},"error":nil,"request_id":rid})
}
