package main

import (
  "context"
  "net/http"
  "strconv"
  "strings"
  "time"

  "github.com/jackc/pgx/v5"
)

func threadIDFromRequest(w http.ResponseWriter,r *http.Request,rid string) string {
  id:=strings.ToLower(r.PathValue("thread_id"))
  if !socialUUIDPattern.MatchString(id){writeJSON(w,400,errorBody("INVALID_THREAD_ID","A valid thread ID is required",rid));return ""}
  return id
}
func (s *service) threadRepostsList(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  id:=threadIDFromRequest(w,r,rid);if id==""{return}
  limit:=50
  if raw:=r.URL.Query().Get("limit");raw!=""{n,err:=strconv.Atoi(raw);if err!=nil||n<1||n>100{writeJSON(w,400,errorBody("INVALID_LIMIT","Limit must be between 1 and 100",rid));return};limit=n}
  rows,err:=s.db.Query(r.Context(),`SELECT r.user_id::text,a.username,a.display_name,a.avatar_key,r.created_at
    FROM testagram_local.thread_reposts r
    JOIN testagram_local.accounts a ON a.user_id=r.user_id
    JOIN testagram_local.threads t ON t.id=r.thread_id
    WHERE r.thread_id=$1 AND a.account_status='active'
      AND (t.visibility IN ('public','unlisted') OR t.owner_id=$2)
    ORDER BY r.created_at DESC LIMIT $3`,id,userID,limit)
  if err!=nil{writeJSON(w,503,errorBody("THREAD_REPOSTS_FAILED","Reposts could not be loaded",rid));return}
  defer rows.Close();items:=make([]map[string]any,0)
  for rows.Next(){var uid,username,displayName,avatarKey string;var created time.Time
    if err:=rows.Scan(&uid,&username,&displayName,&avatarKey,&created);err!=nil{writeJSON(w,503,errorBody("THREAD_REPOSTS_FAILED","Reposts could not be loaded",rid));return}
    avatarURL:="";if avatarKey!=""{avatarURL=publicMediaURL(avatarKey)}
    items=append(items,map[string]any{"user_id":uid,"profile":map[string]any{"id":uid,"username":username,"display_name":displayName,"avatar_url":avatarURL},"created_at":created})
  }
  if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("THREAD_REPOSTS_FAILED","Reposts could not be loaded",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items},"error":nil,"request_id":rid})
}
func (s *service) threadRepostState(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  id:=threadIDFromRequest(w,r,rid);if id==""{return}
  var liked bool
  err:=s.db.QueryRow(r.Context(),`SELECT EXISTS(SELECT 1 FROM testagram_local.thread_reposts WHERE thread_id=$1 AND user_id=$2)`,id,userID).Scan(&liked)
  if err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_STATE_FAILED","Repost state could not be loaded",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"reposted":liked},"error":nil,"request_id":rid})
}
func (s *service) threadRepostToggle(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  if !s.socialWriteAllowed(w,r,userID,rid,"thread-repost",30){return}
  id:=threadIDFromRequest(w,r,rid);if id==""{return}
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_FAILED","Repost could not be updated",rid));return};defer tx.Rollback(ctx)
  var visibility,ownerID string
  err=tx.QueryRow(ctx,`SELECT visibility,owner_id::text FROM testagram_local.threads WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`,id).Scan(&visibility,&ownerID)
  if err==pgx.ErrNoRows{writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid));return}
  if err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_FAILED","Thread could not be loaded",rid));return}
  if visibility=="private"&&ownerID!=userID{writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid));return}
  var exists bool
  if err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.thread_reposts WHERE thread_id=$1 AND user_id=$2)`,id,userID).Scan(&exists);err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_FAILED","Repost state could not be loaded",rid));return}
  active:=!exists
  if active{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.thread_reposts(thread_id,user_id) VALUES($1,$2)`,id,userID)}else{_,err=tx.Exec(ctx,`DELETE FROM testagram_local.thread_reposts WHERE thread_id=$1 AND user_id=$2`,id,userID)}
  if err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_FAILED","Repost could not be updated",rid));return}
  if err=tx.Commit(ctx);err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_FAILED","Repost could not be committed",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_id":id,"reposted":active},"error":nil,"request_id":rid})
}

func (s *service) threadRepostStates(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  var in struct{ThreadIDs []string `json:"thread_ids"`}
  if err:=decodeMediaJSON(w,r,&in);err!=nil||len(in.ThreadIDs)>100{writeJSON(w,400,errorBody("INVALID_THREAD_IDS","Provide up to 100 valid thread IDs",rid));return}
  ids:=make([]string,0,len(in.ThreadIDs))
  for _,id:=range in.ThreadIDs{id=strings.ToLower(strings.TrimSpace(id));if !socialUUIDPattern.MatchString(id){writeJSON(w,400,errorBody("INVALID_THREAD_IDS","Every thread ID must be a valid UUID",rid));return};ids=append(ids,id)}
  if len(ids)==0{writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_ids":[]string{}},"error":nil,"request_id":rid});return}
  rows,err:=s.db.Query(r.Context(),`SELECT thread_id::text FROM testagram_local.thread_reposts WHERE user_id=$1 AND thread_id=ANY($2::uuid[])`,userID,ids)
  if err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_STATE_FAILED","Repost states could not be loaded",rid));return}
  defer rows.Close();result:=make([]string,0)
  for rows.Next(){var id string;if err:=rows.Scan(&id);err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_STATE_FAILED","Repost states could not be loaded",rid));return};result=append(result,id)}
  if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("THREAD_REPOST_STATE_FAILED","Repost states could not be loaded",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_ids":result},"error":nil,"request_id":rid})
}

func (s *service) threadBookmarkStates(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  var in struct{ThreadIDs []string `json:"thread_ids"`}
  if err:=decodeMediaJSON(w,r,&in);err!=nil||len(in.ThreadIDs)>100{writeJSON(w,400,errorBody("INVALID_THREAD_IDS","Provide up to 100 valid thread IDs",rid));return}
  ids:=make([]string,0,len(in.ThreadIDs))
  for _,id:=range in.ThreadIDs{id=strings.ToLower(strings.TrimSpace(id));if !socialUUIDPattern.MatchString(id){writeJSON(w,400,errorBody("INVALID_THREAD_IDS","Every thread ID must be a valid UUID",rid));return};ids=append(ids,id)}
  if len(ids)==0{writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_ids":[]string{}},"error":nil,"request_id":rid});return}
  rows,err:=s.db.Query(r.Context(),`SELECT thread_id::text FROM testagram_local.thread_bookmarks WHERE user_id=$1 AND thread_id=ANY($2::uuid[])`,userID,ids)
  if err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_STATE_FAILED","Bookmark states could not be loaded",rid));return}
  defer rows.Close();result:=make([]string,0)
  for rows.Next(){var id string;if err:=rows.Scan(&id);err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_STATE_FAILED","Bookmark states could not be loaded",rid));return};result=append(result,id)}
  if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_STATE_FAILED","Bookmark states could not be loaded",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_ids":result},"error":nil,"request_id":rid})
}
func (s *service) threadBookmarkToggle(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  if !s.socialWriteAllowed(w,r,userID,rid,"thread-bookmark",60){return}
  id:=threadIDFromRequest(w,r,rid);if id==""{return}
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_FAILED","Bookmark could not be updated",rid));return};defer tx.Rollback(ctx)
  var visibility,ownerID string
  err=tx.QueryRow(ctx,`SELECT visibility,owner_id::text FROM testagram_local.threads WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`,id).Scan(&visibility,&ownerID)
  if err==pgx.ErrNoRows{writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid));return}
  if err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_FAILED","Thread could not be loaded",rid));return}
  if visibility=="private"&&ownerID!=userID{writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid));return}
  var exists bool
  if err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.thread_bookmarks WHERE thread_id=$1 AND user_id=$2)`,id,userID).Scan(&exists);err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_FAILED","Bookmark state could not be loaded",rid));return}
  active:=!exists
  if active{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.thread_bookmarks(thread_id,user_id) VALUES($1,$2)`,id,userID)}else{_,err=tx.Exec(ctx,`DELETE FROM testagram_local.thread_bookmarks WHERE thread_id=$1 AND user_id=$2`,id,userID)}
  if err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_FAILED","Bookmark could not be updated",rid));return}
  if err=tx.Commit(ctx);err!=nil{writeJSON(w,503,errorBody("THREAD_BOOKMARK_FAILED","Bookmark could not be committed",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_id":id,"bookmarked":active},"error":nil,"request_id":rid})
}
