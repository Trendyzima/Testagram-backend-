package main

import (
  "context"
  "net/http"
  "regexp"
  "strings"
  "time"
)

var hashtagTagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

func (s *service) nativeHashtagCapability(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
  name := strings.TrimSpace(body.Capability)
  switch name {
  case "testagram.hashtags.resolve", "testagram.hashtags.follow.state", "testagram.hashtags.follow.toggle",
    "testagram.hashtags.follow.list", "testagram.hashtags.trending":
  default:
    return false
  }
  if s.db == nil {
    writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_DATABASE_UNAVAILABLE","Hashtag service is unavailable",requestID))
    return true
  }
  userID,status,err := s.authenticatedMediaUser(r)
  if err != nil || !socialUUIDPattern.MatchString(userID) {
    if err == nil { status = http.StatusUnauthorized }
    writeJSON(w,status,errorBody("AUTH_REQUIRED","A valid authenticated session is required",requestID))
    return true
  }
  ctx,cancel := context.WithTimeout(r.Context(),3*time.Second)
  defer cancel()
  input := body.Input
  tag := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(toString(input["tag"])),"#"))
  hashtagID := strings.TrimSpace(toString(input["hashtag_id"]))
  switch name {
  case "testagram.hashtags.resolve":
    if !hashtagTagPattern.MatchString(tag) {
      writeJSON(w,http.StatusBadRequest,errorBody("INVALID_HASHTAG","Hashtag must be 2-64 lowercase letters, numbers, underscores or hyphens",requestID))
      return true
    }
    var result map[string]any
    var id,savedTag string
    var usage,posts,followers int64
    err = s.db.QueryRow(ctx,`INSERT INTO testagram_local.hashtags(tag,last_used_at)
      VALUES($1,now()) ON CONFLICT(tag) DO UPDATE SET last_used_at=now()
      RETURNING id::text,tag,usage_count,post_count,follower_count`,tag).Scan(&id,&savedTag,&usage,&posts,&followers)
    if err != nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_RESOLVE_FAILED","Could not resolve hashtag",requestID)); return true }
    result=map[string]any{"id":id,"tag":savedTag,"usage_count":usage,"post_count":posts,"follower_count":followers}
    writeJSON(w,http.StatusOK,result)
    return true
  case "testagram.hashtags.follow.state":
    if !socialUUIDPattern.MatchString(hashtagID) { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_HASHTAG_ID","A valid hashtag ID is required",requestID)); return true }
    var following bool
    err=s.db.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.hashtag_follows WHERE user_id=$1 AND hashtag_id=$2)",userID,hashtagID).Scan(&following)
    if err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_FOLLOW_READ_FAILED","Could not load hashtag follow state",requestID)); return true }
    writeJSON(w,http.StatusOK,map[string]any{"following":following})
    return true
  case "testagram.hashtags.follow.toggle":
    if !socialUUIDPattern.MatchString(hashtagID) { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_HASHTAG_ID","A valid hashtag ID is required",requestID)); return true }
    following,_:=input["following"].(bool)
    if following {
      _,err=s.db.Exec(ctx,`WITH inserted AS (INSERT INTO testagram_local.hashtag_follows(user_id,hashtag_id) VALUES($1,$2) ON CONFLICT(user_id,hashtag_id) DO NOTHING RETURNING hashtag_id) UPDATE testagram_local.hashtags h SET follower_count=follower_count+1 FROM inserted i WHERE h.id=i.hashtag_id`,userID,hashtagID)
    } else {
      _,err=s.db.Exec(ctx,`WITH removed AS (DELETE FROM testagram_local.hashtag_follows WHERE user_id=$1 AND hashtag_id=$2 RETURNING hashtag_id) UPDATE testagram_local.hashtags h SET follower_count=GREATEST(0,follower_count-1) FROM removed r WHERE h.id=r.hashtag_id`,userID,hashtagID)
    }
    if err!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_FOLLOW_WRITE_FAILED","Could not update hashtag follow",requestID)); return true }
    writeJSON(w,http.StatusOK,map[string]any{"following":following})
    return true
  case "testagram.hashtags.follow.list":
    rows,e:=s.db.Query(ctx,`SELECT h.id::text,h.tag,h.usage_count,h.post_count,h.follower_count
      FROM testagram_local.hashtag_follows f JOIN testagram_local.hashtags h ON h.id=f.hashtag_id
      WHERE f.user_id=$1 ORDER BY f.created_at DESC LIMIT 100`,userID)
    if e!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_FOLLOW_LIST_FAILED","Could not load followed hashtags",requestID)); return true }
    defer rows.Close()
    items:=make([]map[string]any,0)
    for rows.Next(){var id,t string;var usage,posts,followers int64;if rows.Scan(&id,&t,&usage,&posts,&followers)!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_FOLLOW_LIST_FAILED","Could not read followed hashtags",requestID));return true};items=append(items,map[string]any{"id":id,"tag":t,"usage_count":usage,"post_count":posts,"follower_count":followers})}
    if rows.Err()!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_FOLLOW_LIST_FAILED","Could not read followed hashtags",requestID)); return true }
    writeJSON(w,http.StatusOK,map[string]any{"items":items})
    return true
  case "testagram.hashtags.trending":
    limit:=20
    if value,ok:=input["limit"].(float64);ok&&int(value)>0 { limit=int(value) }
    if limit>100 { limit=100 }
    rows,e:=s.db.Query(ctx,`SELECT id::text,tag,usage_count,post_count,follower_count,last_used_at
      FROM testagram_local.hashtags ORDER BY last_used_at DESC,usage_count DESC LIMIT $1`,limit)
    if e!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_TRENDING_FAILED","Could not load trending hashtags",requestID)); return true }
    defer rows.Close()
    items:=make([]map[string]any,0)
    for rows.Next(){var id,t string;var usage,posts,followers int64;var lastUsed time.Time;if rows.Scan(&id,&t,&usage,&posts,&followers,&lastUsed)!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_TRENDING_FAILED","Could not read trending hashtags",requestID));return true};items=append(items,map[string]any{"id":id,"tag":t,"usage_count":usage,"post_count":posts,"follower_count":followers,"last_used_at":lastUsed})}
    if rows.Err()!=nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("HASHTAG_TRENDING_FAILED","Could not read trending hashtags",requestID)); return true }
    writeJSON(w,http.StatusOK,map[string]any{"items":items})
    return true
  }
  return false
}
