package main

import (
 "context"
 "encoding/json"
 "net/http"
 "strconv"
 "strings"
 "time"
)

func (s *service) socialListFeed(w http.ResponseWriter, r *http.Request) {
 viewerID, requestID, ok := s.socialAuth(w, r)
 if !ok { return }
 listID := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
 if !socialUUIDPattern.MatchString(listID) {
  writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid list ID is required", requestID)); return
 }
 limit := 20
 if raw := r.URL.Query().Get("limit"); raw != "" {
  n, err := strconv.Atoi(raw)
  if err != nil || n < 1 || n > 50 { writeJSON(w,400,errorBody("INVALID_LIMIT","List feed limit must be between 1 and 50",requestID)); return }
  limit = n
 }
 cursor, err := decodeHomeFeedCursor(r.URL.Query().Get("before"))
 if err != nil || !nativeSingleHomeFeedCursor(cursor) {
  writeJSON(w,400,errorBody("INVALID_CURSOR","List feed cursor is invalid",requestID)); return
 }
 var ownerID,name,description,visibility string
 err = s.db.QueryRow(r.Context(), `
SELECT owner_id::text,name,description,visibility
FROM testagram_local.social_lists
WHERE id=$1::uuid AND deleted_at IS NULL
 AND (owner_id=$2::uuid OR visibility IN ('public','unlisted'))`, listID,viewerID).Scan(&ownerID,&name,&description,&visibility)
 if err != nil { writeJSON(w,404,errorBody("LIST_NOT_FOUND","List is unavailable",requestID)); return }
 var before,beforeID any
 if cursor != nil { before,beforeID=cursor.CreatedAt,cursor.ID }
 ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second); defer cancel()
 rows,err:=s.db.Query(ctx, `
SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,
 p.language,p.created_at,p.likes_count,p.replies_count,p.reposts_count,
 a.username,a.display_name,a.avatar_key,a.protected_account
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
JOIN testagram_local.social_list_members lm ON lm.list_id=$2::uuid AND lm.member_user_id=p.author_id
WHERE p.deleted_at IS NULL AND a.account_status='active'
 AND (p.author_id=$1::uuid
  OR (p.visibility='public' AND NOT a.protected_account)
  OR (p.visibility IN ('public','followers') AND EXISTS(
   SELECT 1 FROM testagram_local.follows f
   WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted')))
 AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
  WHERE (b.blocker_id=$1::uuid AND b.blocked_id=p.author_id)
   OR (b.blocker_id=p.author_id AND b.blocked_id=$1::uuid))
 AND NOT EXISTS(SELECT 1 FROM testagram_local.mutes m
  WHERE m.muter_id=$1::uuid AND m.muted_id=p.author_id)
 AND ($3::timestamptz IS NULL OR (p.created_at,p.id)<($3::timestamptz,$4::uuid))
ORDER BY p.created_at DESC,p.id DESC LIMIT $5`,viewerID,listID,before,beforeID,limit+1)
 if err != nil { writeJSON(w,503,errorBody("LIST_FEED_UNAVAILABLE","List feed could not be loaded",requestID)); return }
 defer rows.Close()
 posts:=make([]homeFeedPost,0,limit); var next homeFeedCursor; hasMore:=false
 for rows.Next() {
  var post homeFeedPost; var mediaRaw []byte; var username,displayName,avatarKey string; var protected bool
  if err:=rows.Scan(&post.ID,&post.UserID,&post.Content,&post.Visibility,&mediaRaw,&post.Language,&post.CreatedAt,&post.LikesCount,&post.RepliesCount,&post.RepostsCount,&username,&displayName,&avatarKey,&protected);err!=nil {
   writeJSON(w,503,errorBody("LIST_FEED_UNAVAILABLE","List feed data could not be decoded",requestID));return
  }
  if len(posts)==limit { hasMore=true; break }
  post.MediaIDs=[]string{}; _=json.Unmarshal(mediaRaw,&post.MediaIDs)
  post.Author=map[string]any{"id":post.UserID,"username":username,"display_name":displayName,"avatar_key":avatarKey,"avatar_url":publicMediaURL(avatarKey),"protected_account":protected}
  post.Origin="local"; posts=append(posts,post); next=homeFeedCursor{CreatedAt:post.CreatedAt,ID:post.ID}
 }
 if err:=rows.Err();err!=nil { writeJSON(w,503,errorBody("LIST_FEED_UNAVAILABLE","List feed could not be loaded",requestID));return }
 rows.Close()
 if err:=s.attachHomeFeedMediaURLs(ctx,viewerID,posts);err!=nil { writeJSON(w,503,errorBody("LIST_FEED_MEDIA_UNAVAILABLE","List feed media could not be authorized",requestID));return }
 nextCursor:=""; if hasMore&&len(posts)>0 { nextCursor=encodeHomeFeedCursor(next) }
 writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{
  "list":map[string]any{"id":listID,"owner_id":ownerID,"name":name,"description":description,"visibility":visibility,"is_owner":ownerID==viewerID},
  "posts":posts,"has_more":hasMore,"next_cursor":nextCursor,"ranking_mode":"chronological",
 },"error":nil,"request_id":requestID})
}
