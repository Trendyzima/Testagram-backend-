package main

import (
  "context"
  "encoding/json"
  "net/http"
  "strings"
  "time"
)

func (s *service) threadRead(w http.ResponseWriter,r *http.Request) {
  rid:=requestID(r)
  if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",rid));return}
  if s.db==nil{writeJSON(w,503,errorBody("SOCIAL_DATABASE_UNAVAILABLE","Social data service is unavailable",rid));return}
  userID:=""
  if strings.TrimSpace(r.Header.Get("Authorization"))!="" {
    var ok bool
    userID,rid,ok=s.socialAuth(w,r)
    if !ok{return}
  }
  id:=strings.ToLower(r.PathValue("thread_id"))
  if !socialUUIDPattern.MatchString(id){writeJSON(w,400,errorBody("INVALID_THREAD_ID","A valid thread ID is required",rid));return}
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  var threadID,ownerID,body,visibility,username,displayName,avatarKey string
  var mediaJSON []byte
  var created time.Time
  var likes,reposts,quotes,replies,views int64
  var viewer any
  if userID!=""{viewer=userID}
  err:=s.db.QueryRow(ctx,`SELECT t.id::text,t.owner_id::text,t.body,t.visibility,t.media_urls,t.created_at,
      t.likes_count,t.reposts_count,t.quotes_count,t.replies_count,t.views_count,
      a.username,a.display_name,a.avatar_key
    FROM testagram_local.threads t
    JOIN testagram_local.accounts a ON a.user_id=t.owner_id
    WHERE t.id=$1 AND t.deleted_at IS NULL
      AND (t.visibility IN ('public','unlisted') OR ($2::uuid IS NOT NULL AND t.owner_id=$2::uuid))
      AND a.account_status='active'`,id,viewer).Scan(&threadID,&ownerID,&body,&visibility,&mediaJSON,&created,&likes,&reposts,&quotes,&replies,&views,&username,&displayName,&avatarKey)
  if err!=nil{writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid));return}
  media:=make([]map[string]any,0)
  if len(mediaJSON)>0{_ = json.Unmarshal(mediaJSON,&media)}
  avatarURL:=""
  if strings.TrimSpace(avatarKey)!="" { avatarURL=publicMediaURL(avatarKey) }
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{
    "id":threadID,"owner_id":ownerID,"body":body,"visibility":visibility,"media_urls":media,"created_at":created,
    "likes_count":likes,"reposts_count":reposts,"quotes_count":quotes,"replies_count":replies,"views_count":views,
    "profiles":map[string]any{"id":ownerID,"username":username,"display_name":displayName,"avatar_url":avatarURL,"verified":false},
  },"error":nil,"request_id":rid})
}
