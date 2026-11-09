package main

import (
  "context"
  "encoding/json"
  "errors"
  "net/http"
  "strings"
  "time"

  "github.com/jackc/pgx/v5"
)

type threadCreateInput struct {
  Body string `json:"body"`
  Visibility string `json:"visibility"`
}
type threadMediaInput struct { Key string `json:"key"` }
type threadMediaDTO struct {
  URL string `json:"url"`
  Name string `json:"name"`
  Type string `json:"type"`
  Size int64 `json:"size"`
}

func (s *service) threadCreate(w http.ResponseWriter, r *http.Request) {
  userID, rid, ok := s.socialAuth(w,r); if !ok { return }
  if !s.socialWriteAllowed(w,r,userID,rid,"thread-create",5) { return }
  var in threadCreateInput
  if err:=decodeMediaJSON(w,r,&in); err!=nil { writeJSON(w,400,errorBody("INVALID_THREAD","Thread payload is invalid",rid)); return }
  in.Body=strings.TrimSpace(in.Body)
  if len(in.Body)>500 { writeJSON(w,400,errorBody("THREAD_TOO_LONG","Thread text must be 500 characters or fewer",rid)); return }
  if in.Visibility=="" { in.Visibility="public" }
  if in.Visibility!="public" && in.Visibility!="unlisted" && in.Visibility!="private" { writeJSON(w,400,errorBody("INVALID_VISIBILITY","Thread visibility is invalid",rid)); return }
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
  var id string
  err:=s.db.QueryRow(ctx,`INSERT INTO testagram_local.threads(owner_id,body,visibility) VALUES($1,$2,$3) RETURNING id::text`,userID,in.Body,in.Visibility).Scan(&id)
  if err!=nil { writeJSON(w,503,errorBody("THREAD_CREATE_FAILED","Thread could not be created",rid)); return }
  writeJSON(w,201,map[string]any{"ok":true,"data":map[string]any{"id":id,"owner_id":userID,"body":in.Body,"visibility":in.Visibility,"media_urls":[]threadMediaDTO{},"created_at":time.Now().UTC()},"error":nil,"request_id":rid})
}

func (s *service) threadAttachMedia(w http.ResponseWriter, r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r); if !ok{return}
  threadID:=strings.ToLower(r.PathValue("thread_id"))
  if !socialUUIDPattern.MatchString(threadID){writeJSON(w,400,errorBody("INVALID_THREAD_ID","A valid thread ID is required",rid));return}
  var in threadMediaInput
  if err:=decodeMediaJSON(w,r,&in);err!=nil||strings.TrimSpace(in.Key)==""{writeJSON(w,400,errorBody("INVALID_MEDIA_KEY","A valid media key is required",rid));return}
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,503,errorBody("THREAD_MEDIA_FAILED","Could not attach media",rid));return};defer tx.Rollback(ctx)
  var body string
  if err=tx.QueryRow(ctx,`SELECT body FROM testagram_local.threads WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL FOR UPDATE`,threadID,userID).Scan(&body);err!=nil{
    if errors.Is(err,pgx.ErrNoRows){writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid))}else{writeJSON(w,503,errorBody("THREAD_MEDIA_FAILED","Could not load thread",rid))};return
  }
  var visibility,contentType,name,status,namespace string
  var size int64
  err=tx.QueryRow(ctx,`SELECT visibility,content_type,original_name,size_bytes,status,storage_namespace FROM testagram_media.assets WHERE object_key=$1 AND owner_id=$2`,strings.TrimSpace(in.Key),userID).Scan(&visibility,&contentType,&name,&size,&status,&namespace)
  if err!=nil||status!="ready"||visibility!="public"||size<1||size>maxProductPostMediaBytes{
    writeJSON(w,404,errorBody("MEDIA_NOT_ATTACHABLE","Only ready public media owned by this account can be attached",rid));return
  }
  mediaURL:=publicMediaURLForStorage(strings.TrimSpace(in.Key),userID,namespace)
  if mediaURL==""{writeJSON(w,503,errorBody("MEDIA_PUBLIC_URL_INVALID","Media delivery URL is unavailable",rid));return}
  mediaJSON,marshalErr:=json.Marshal([]threadMediaDTO{{URL:mediaURL,Name:name,Type:contentType,Size:size}})
  if marshalErr!=nil{writeJSON(w,500,errorBody("THREAD_MEDIA_FAILED","Media metadata could not be encoded",rid));return}
  tag,err:=tx.Exec(ctx,`UPDATE testagram_local.threads
    SET media_urls=media_urls || $3::jsonb,updated_at=now()
    WHERE id=$1 AND owner_id=$2 AND jsonb_array_length(media_urls)<20
      AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(media_urls) AS existing WHERE existing->>'url'=$4)`,threadID,userID,mediaJSON,mediaURL)
  if err!=nil{writeJSON(w,503,errorBody("THREAD_MEDIA_FAILED","Media could not be attached",rid));return}
  if tag.RowsAffected()==0{writeJSON(w,409,errorBody("THREAD_MEDIA_LIMIT_OR_DUPLICATE","Thread already has this attachment or reached the 20-file limit",rid));return}
  if err=tx.Commit(ctx);err!=nil{writeJSON(w,503,errorBody("THREAD_MEDIA_FAILED","Media attachment could not be committed",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"thread_id":threadID,"attached":true,"media":threadMediaDTO{URL:mediaURL,Name:name,Type:contentType,Size:size}},"error":nil,"request_id":rid})
}


func (s *service) threadDelete(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  id:=strings.ToLower(r.PathValue("thread_id"));if !socialUUIDPattern.MatchString(id){writeJSON(w,400,errorBody("INVALID_THREAD_ID","A valid thread ID is required",rid));return}
  tag,err:=s.db.Exec(r.Context(),`UPDATE testagram_local.threads SET deleted_at=now(),updated_at=now() WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL`,id,userID)
  if err!=nil{writeJSON(w,503,errorBody("THREAD_DELETE_FAILED","Thread could not be deleted",rid));return}
  if tag.RowsAffected()==0{writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Thread was not found",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"id":id,"deleted":true},"error":nil,"request_id":rid})
}

type browsingHistoryInput struct { EntityType string `json:"entity_type"`; EntityID string `json:"entity_id"`; Metadata map[string]any `json:"metadata"` }
func (s *service) browsingHistoryRecord(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  var in browsingHistoryInput
  if err:=decodeMediaJSON(w,r,&in);err!=nil{writeJSON(w,400,errorBody("INVALID_HISTORY_ITEM","History item is invalid",rid));return}
  in.EntityType=strings.ToLower(strings.TrimSpace(in.EntityType));in.EntityID=strings.ToLower(strings.TrimSpace(in.EntityID))
  if (in.EntityType!="post"&&in.EntityType!="thread"&&in.EntityType!="video")||!socialUUIDPattern.MatchString(in.EntityID){writeJSON(w,400,errorBody("INVALID_HISTORY_ITEM","Only valid post, thread or video IDs can be recorded",rid));return}
  if in.Metadata==nil{in.Metadata=map[string]any{}}
  metadata,marshalErr:=json.Marshal(in.Metadata)
  if marshalErr!=nil{writeJSON(w,400,errorBody("INVALID_HISTORY_METADATA","History metadata must be valid JSON",rid));return}
  _,err:=s.db.Exec(r.Context(),`INSERT INTO testagram_local.browsing_history(user_id,entity_type,entity_id,metadata,viewed_at) VALUES($1,$2,$3,$4::jsonb,now()) ON CONFLICT(user_id,entity_type,entity_id) DO UPDATE SET metadata=EXCLUDED.metadata,viewed_at=now()`,userID,in.EntityType,in.EntityID,metadata)
  if err!=nil{writeJSON(w,503,errorBody("HISTORY_RECORD_FAILED","Browsing history could not be saved",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"recorded":true},"error":nil,"request_id":rid})
}
func (s *service) browsingHistoryList(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  rows,err:=s.db.Query(r.Context(),`SELECT entity_type,entity_id::text,metadata,viewed_at FROM testagram_local.browsing_history WHERE user_id=$1 ORDER BY viewed_at DESC LIMIT 100`,userID)
  if err!=nil{writeJSON(w,503,errorBody("HISTORY_READ_FAILED","Browsing history could not be loaded",rid));return}
  defer rows.Close();items:=make([]map[string]any,0)
  for rows.Next(){var typ,id string;var metadata []byte;var viewed time.Time;if err:=rows.Scan(&typ,&id,&metadata,&viewed);err!=nil{writeJSON(w,503,errorBody("HISTORY_READ_FAILED","Browsing history could not be loaded",rid));return};var decoded map[string]any;_ = json.Unmarshal(metadata,&decoded);items=append(items,map[string]any{"entity_type":typ,"entity_id":id,"metadata":decoded,"created_at":viewed})}
  if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("HISTORY_READ_FAILED","Browsing history could not be loaded",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items},"error":nil,"request_id":rid})
}
func (s *service) browsingHistoryClear(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialAuth(w,r);if !ok{return}
  tag,err:=s.db.Exec(r.Context(),`DELETE FROM testagram_local.browsing_history WHERE user_id=$1`,userID)
  if err!=nil{writeJSON(w,503,errorBody("HISTORY_CLEAR_FAILED","Browsing history could not be cleared",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"deleted":tag.RowsAffected()},"error":nil,"request_id":rid})
}
