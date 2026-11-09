package main

import (
  "context"
  "net/http"
  "strings"
  "time"
)

func (s *service) socialPostView(w http.ResponseWriter,r *http.Request) {
  userID,rid,ok:=s.socialOptionalReadAuth(w,r);if !ok{return}
  postID:=strings.ToLower(strings.TrimSpace(r.PathValue("post_id")))
  if !socialUUIDPattern.MatchString(postID){writeJSON(w,400,errorBody("INVALID_POST_ID","A valid post ID is required",rid));return}
  if userID!=""&&!s.socialWriteAllowed(w,r,userID,rid,"post-view",120){return}
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,503,errorBody("POST_VIEW_FAILED","Post view could not be recorded",rid));return};defer tx.Rollback(ctx)
  var viewer any;if userID!=""{viewer=userID}
  var views int64
  err=tx.QueryRow(ctx,`UPDATE testagram_local.posts
    SET views_count=views_count+1,updated_at=now()
    WHERE id=$1 AND deleted_at IS NULL
      AND (visibility='public' OR author_id=$2::uuid OR (visibility='followers' AND EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$2::uuid AND f.following_id=author_id AND f.status='accepted')))
    RETURNING views_count`,postID,viewer).Scan(&views)
  if err!=nil{writeJSON(w,404,errorBody("POST_NOT_FOUND","Post was not found",rid));return}
  if userID!=""{
    _,err=tx.Exec(ctx,`INSERT INTO testagram_local.browsing_history(user_id,entity_type,entity_id,metadata,viewed_at)
      VALUES($1,'post',$2,'{"surface":"video-player"}'::jsonb,now())
      ON CONFLICT(user_id,entity_type,entity_id) DO UPDATE SET metadata=EXCLUDED.metadata,viewed_at=now()`,userID,postID)
    if err!=nil{writeJSON(w,503,errorBody("POST_VIEW_FAILED","Post view history could not be saved",rid));return}
  }
  if err=tx.Commit(ctx);err!=nil{writeJSON(w,503,errorBody("POST_VIEW_FAILED","Post view could not be committed",rid));return}
  writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"post_id":postID,"views_count":views},"error":nil,"request_id":rid})
}
