package main

import (
  "context"
  "encoding/json"
  "net/http"
  "strings"
  "time"

  "github.com/jackc/pgx/v5"
)

type iptvSocialRequest struct {
  Action string `json:"action"`
  Payload map[string]json.RawMessage `json:"payload"`
}
func iptvString(payload map[string]json.RawMessage,key string) string {
  var value string
  _=json.Unmarshal(payload[key],&value)
  return strings.TrimSpace(value)
}
func iptvChannel(payload map[string]json.RawMessage)(string,map[string]any,error){
  raw:=payload["channel"];if len(raw)==0{return "",nil,pgx.ErrNoRows}
  var channel map[string]any
  if err:=json.Unmarshal(raw,&channel);err!=nil||channel==nil{return "",nil,pgx.ErrNoRows}
  id,_:=channel["id"].(string);id=strings.TrimSpace(id)
  if id==""||len(id)>256{return "",nil,pgx.ErrNoRows}
  safe:=map[string]any{"id":id}
  for _,key:=range []string{"name","logo","country","countryCode","categories","language","group","description","website","streamType"} {
    if value,ok:=channel[key];ok{safe[key]=value}
  }
  if value,ok:=safe["countryCode"];ok{safe["country_code"]=value}
  if value,ok:=safe["name"];ok{safe["channel_name"]=value}
  if value,ok:=safe["logo"];ok{safe["channel_logo"]=value}
  return id,safe,nil
}
func iptvJSON(v any) []byte { b,_:=json.Marshal(v);return b }

func (s *service) iptvSocial(w http.ResponseWriter,r *http.Request) {
  var in iptvSocialRequest
  rid:=requestID(r)
  if !s.cors(w,r){writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",rid));return}
  if err:=decodeMediaJSON(w,r,&in);err!=nil{writeJSON(w,400,errorBody("INVALID_IPTV_SOCIAL_REQUEST","IPTV social request is invalid",rid));return}
  action:=strings.ToLower(strings.TrimSpace(in.Action))
  publicRead:=action=="trending"||action=="comments_list"||action=="comments_count"||action=="reactions_state"
  userID:=""
  if publicRead {
    var ok bool
    userID,rid,ok=s.socialOptionalReadAuth(w,r);if !ok{return}
  } else {
    var ok bool
    userID,rid,ok=s.socialAuth(w,r);if !ok{return}
  }
  ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
  switch action {
  case "reactions_state":
    channelID:=iptvString(in.Payload,"channelId");if channelID==""||len(channelID)>256{writeJSON(w,400,errorBody("INVALID_CHANNEL","Channel ID is required",rid));return}
    var count int64;var liked bool
    if err:=s.db.QueryRow(ctx,`SELECT count(*) FROM testagram_local.iptv_channel_reactions WHERE channel_id=$1`,channelID).Scan(&count);err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction state is unavailable",rid));return}
    if userID!=""{if err:=s.db.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.iptv_channel_reactions WHERE channel_id=$1 AND user_id=$2)`,channelID,userID).Scan(&liked);err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction state is unavailable",rid));return}}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"liked":liked,"likeCount":count},"error":nil,"request_id":rid})
  case "reaction_toggle":
    if !s.socialWriteAllowed(w,r,userID,rid,"iptv-reaction",60){return}
    channelID:=iptvString(in.Payload,"channelId");if channelID==""||len(channelID)>256{writeJSON(w,400,errorBody("INVALID_CHANNEL","Channel ID is required",rid));return}
    tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction could not be updated",rid));return};defer tx.Rollback(ctx)
    _,err=tx.Exec(ctx,`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,userID+":"+channelID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction could not be locked",rid));return}
    var exists bool
    if err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.iptv_channel_reactions WHERE channel_id=$1 AND user_id=$2)`,channelID,userID).Scan(&exists);err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction state could not be loaded",rid));return}
    liked:=!exists
    if liked{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.iptv_channel_reactions(channel_id,user_id) VALUES($1,$2)`,channelID,userID)}else{_,err=tx.Exec(ctx,`DELETE FROM testagram_local.iptv_channel_reactions WHERE channel_id=$1 AND user_id=$2`,channelID,userID)}
    if err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction could not be updated",rid));return}
    if err=tx.Commit(ctx);err!=nil{writeJSON(w,503,errorBody("IPTV_REACTION_FAILED","Reaction could not be committed",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"liked":liked},"error":nil,"request_id":rid})
  case "comments_list","comments_count":
    channelID:=iptvString(in.Payload,"channelId");if channelID==""||len(channelID)>256{writeJSON(w,400,errorBody("INVALID_CHANNEL","Channel ID is required",rid));return}
    if action=="comments_count"{var count int64;err:=s.db.QueryRow(ctx,`SELECT count(*) FROM testagram_local.iptv_channel_comments WHERE channel_id=$1 AND deleted_at IS NULL AND expires_at>now()`,channelID).Scan(&count);if err!=nil{writeJSON(w,503,errorBody("IPTV_COMMENTS_FAILED","Comment count is unavailable",rid));return};writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"count":count},"error":nil,"request_id":rid});return}
    rows,err:=s.db.Query(ctx,`SELECT c.id::text,c.user_id::text,a.username,c.content,c.created_at,c.expires_at
      FROM testagram_local.iptv_channel_comments c JOIN testagram_local.accounts a ON a.user_id=c.user_id
      WHERE c.channel_id=$1 AND c.deleted_at IS NULL AND c.expires_at>now()
      ORDER BY c.created_at DESC LIMIT 100`,channelID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_COMMENTS_FAILED","Comments could not be loaded",rid));return}
    defer rows.Close();items:=make([]map[string]any,0)
    for rows.Next(){var id,uid,username,content string;var created,expires time.Time;if err:=rows.Scan(&id,&uid,&username,&content,&created,&expires);err!=nil{writeJSON(w,503,errorBody("IPTV_COMMENTS_FAILED","Comments could not be loaded",rid));return};items=append(items,map[string]any{"id":id,"user_id":uid,"username":username,"content":content,"created_at":created,"expires_at":expires})}
    if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("IPTV_COMMENTS_FAILED","Comments could not be loaded",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"comments":items},"error":nil,"request_id":rid})
  case "comment_add":
    if !s.socialWriteAllowed(w,r,userID,rid,"iptv-comment",20){return}
    channelID:=iptvString(in.Payload,"channelId");content:=iptvString(in.Payload,"content")
    if channelID==""||len(channelID)>256||len([]rune(content))<1||len([]rune(content))>280{writeJSON(w,400,errorBody("INVALID_COMMENT","Channel ID and a comment of 1–280 characters are required",rid));return}
    var id,username string;var created,expires time.Time
    err:=s.db.QueryRow(ctx,`INSERT INTO testagram_local.iptv_channel_comments(channel_id,user_id,content)
      VALUES($1,$2,$3) RETURNING id::text,created_at,expires_at`,channelID,userID,content).Scan(&id,&created,&expires)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_COMMENT_FAILED","Comment could not be saved",rid));return}
    _=s.db.QueryRow(ctx,`SELECT username FROM testagram_local.accounts WHERE user_id=$1`,userID).Scan(&username)
    writeJSON(w,201,map[string]any{"ok":true,"data":map[string]any{"comment":map[string]any{"id":id,"user_id":userID,"username":username,"content":content,"created_at":created,"expires_at":expires}},"error":nil,"request_id":rid})
  case "comment_delete":
    if !s.socialWriteAllowed(w,r,userID,rid,"iptv-comment-delete",30){return}
    id:=iptvString(in.Payload,"id");if !socialUUIDPattern.MatchString(id){writeJSON(w,400,errorBody("INVALID_COMMENT_ID","A valid comment ID is required",rid));return}
    tag,err:=s.db.Exec(ctx,`UPDATE testagram_local.iptv_channel_comments SET deleted_at=now() WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`,id,userID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_COMMENT_FAILED","Comment could not be deleted",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"deleted":tag.RowsAffected()>0},"error":nil,"request_id":rid})
  case "notifications":
    lastCheck:=iptvString(in.Payload,"lastCheck")
    since:=time.Now().UTC().Add(-time.Hour)
    if parsed,parseErr:=time.Parse(time.RFC3339Nano,lastCheck);parseErr==nil&&!parsed.After(time.Now().Add(time.Minute)){since=parsed}
    reactRows,err:=s.db.Query(ctx,`SELECT r.channel_id FROM testagram_local.iptv_channel_reactions r
      WHERE r.user_id<>$1 AND r.created_at >= $2
        AND EXISTS(SELECT 1 FROM testagram_local.iptv_channel_favorites f WHERE f.user_id=$1 AND f.channel_id=r.channel_id)
      ORDER BY r.created_at DESC LIMIT 500`,userID,since)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_NOTIFICATIONS_FAILED","Notifications could not be loaded",rid));return}
    reacts:=make([]map[string]any,0)
    for reactRows.Next(){var channelID string;if err:=reactRows.Scan(&channelID);err!=nil{reactRows.Close();writeJSON(w,503,errorBody("IPTV_NOTIFICATIONS_FAILED","Notifications could not be loaded",rid));return};reacts=append(reacts,map[string]any{"channel_id":channelID})}
    err=reactRows.Err();reactRows.Close();if err!=nil{writeJSON(w,503,errorBody("IPTV_NOTIFICATIONS_FAILED","Notifications could not be loaded",rid));return}
    commentRows,err:=s.db.Query(ctx,`SELECT c.channel_id FROM testagram_local.iptv_channel_comments c
      WHERE c.user_id<>$1 AND c.created_at >= $2 AND c.deleted_at IS NULL AND c.expires_at>now()
        AND EXISTS(SELECT 1 FROM testagram_local.iptv_channel_favorites f WHERE f.user_id=$1 AND f.channel_id=c.channel_id)
      ORDER BY c.created_at DESC LIMIT 500`,userID,since)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_NOTIFICATIONS_FAILED","Notifications could not be loaded",rid));return}
    comms:=make([]map[string]any,0)
    for commentRows.Next(){var channelID string;if err:=commentRows.Scan(&channelID);err!=nil{commentRows.Close();writeJSON(w,503,errorBody("IPTV_NOTIFICATIONS_FAILED","Notifications could not be loaded",rid));return};comms=append(comms,map[string]any{"channel_id":channelID})}
    err=commentRows.Err();commentRows.Close();if err!=nil{writeJSON(w,503,errorBody("IPTV_NOTIFICATIONS_FAILED","Notifications could not be loaded",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"reacts":reacts,"comms":comms},"error":nil,"request_id":rid})
  case "bookmark_list":
    rows,err:=s.db.Query(ctx,`SELECT channel_id,channel_data,created_at FROM testagram_local.iptv_channel_favorites WHERE user_id=$1 ORDER BY created_at DESC LIMIT 200`,userID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorites could not be loaded",rid));return}
    defer rows.Close();items:=make([]map[string]any,0)
    for rows.Next(){var id string;var data []byte;var created time.Time;if err:=rows.Scan(&id,&data,&created);err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorites could not be loaded",rid));return};var channel map[string]any;_=json.Unmarshal(data,&channel);items=append(items,map[string]any{"channel_id":id,"channel_data":channel,"created_at":created})}
    if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorites could not be loaded",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"bookmarks":items},"error":nil,"request_id":rid})
  case "bookmark_toggle":
    channelID,channel,err:=iptvChannel(in.Payload);if err!=nil{writeJSON(w,400,errorBody("INVALID_CHANNEL","Channel metadata with an ID is required",rid));return}
    data:=iptvJSON(channel);if len(data)>32768{writeJSON(w,400,errorBody("CHANNEL_METADATA_TOO_LARGE","Channel metadata is too large",rid));return}
    tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorite could not be updated",rid));return};defer tx.Rollback(ctx)
    if _,err=tx.Exec(ctx,`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,userID+":"+channelID);err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorite could not be locked",rid));return}
    var exists bool;if err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.iptv_channel_favorites WHERE user_id=$1 AND channel_id=$2)`,userID,channelID).Scan(&exists);err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorite state could not be loaded",rid));return}
    active:=!exists
    if active{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.iptv_channel_favorites(user_id,channel_id,channel_data) VALUES($1,$2,$3::jsonb)`,userID,channelID,data)}else{_,err=tx.Exec(ctx,`DELETE FROM testagram_local.iptv_channel_favorites WHERE user_id=$1 AND channel_id=$2`,userID,channelID)}
    if err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorite could not be updated",rid));return}
    if err=tx.Commit(ctx);err!=nil{writeJSON(w,503,errorBody("IPTV_FAVORITES_FAILED","Favorite could not be committed",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"bookmarked":active},"error":nil,"request_id":rid})
  case "history_list":
    rows,err:=s.db.Query(ctx,`SELECT channel_id,channel_data,watched_at FROM testagram_local.iptv_watch_history WHERE user_id=$1 ORDER BY watched_at DESC LIMIT 200`,userID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_HISTORY_FAILED","Watch history could not be loaded",rid));return}
    defer rows.Close();items:=make([]map[string]any,0)
    for rows.Next(){var id string;var data []byte;var watched time.Time;if err:=rows.Scan(&id,&data,&watched);err!=nil{writeJSON(w,503,errorBody("IPTV_HISTORY_FAILED","Watch history could not be loaded",rid));return};var channel map[string]any;_=json.Unmarshal(data,&channel);if channel==nil{channel=map[string]any{}};channel["channel_id"]=id;channel["watched_at"]=watched;items=append(items,channel)}
    if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("IPTV_HISTORY_FAILED","Watch history could not be loaded",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"history":items},"error":nil,"request_id":rid})
  case "history_upsert":
    channelID,channel,err:=iptvChannel(in.Payload);if err!=nil{writeJSON(w,400,errorBody("INVALID_CHANNEL","Channel metadata with an ID is required",rid));return}
    data:=iptvJSON(channel);if len(data)>32768{writeJSON(w,400,errorBody("CHANNEL_METADATA_TOO_LARGE","Channel metadata is too large",rid));return}
    _,err=s.db.Exec(ctx,`INSERT INTO testagram_local.iptv_watch_history(user_id,channel_id,channel_data,watched_at)
      VALUES($1,$2,$3::jsonb,now()) ON CONFLICT(user_id,channel_id) DO UPDATE SET channel_data=EXCLUDED.channel_data,watched_at=now()`,userID,channelID,data)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_HISTORY_FAILED","Watch history could not be saved",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"recorded":true},"error":nil,"request_id":rid})
  case "history_delete":
    channelID:=iptvString(in.Payload,"channelId");if channelID==""{writeJSON(w,400,errorBody("INVALID_CHANNEL","Channel ID is required",rid));return}
    tag,err:=s.db.Exec(ctx,`DELETE FROM testagram_local.iptv_watch_history WHERE user_id=$1 AND channel_id=$2`,userID,channelID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_HISTORY_FAILED","Watch history item could not be removed",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"deleted":tag.RowsAffected()},"error":nil,"request_id":rid})
  case "history_clear":
    tag,err:=s.db.Exec(ctx,`DELETE FROM testagram_local.iptv_watch_history WHERE user_id=$1`,userID)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_HISTORY_FAILED","Watch history could not be cleared",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"deleted":tag.RowsAffected()},"error":nil,"request_id":rid})
  case "trending":
    rows,err:=s.db.Query(ctx,`SELECT channel_id,(count(*)::bigint) AS score FROM (
      SELECT channel_id FROM testagram_local.iptv_channel_reactions
      UNION ALL SELECT channel_id FROM testagram_local.iptv_channel_comments WHERE deleted_at IS NULL AND expires_at>now()
      UNION ALL SELECT channel_id FROM testagram_local.iptv_channel_favorites
    ) activity GROUP BY channel_id ORDER BY score DESC,channel_id LIMIT 100`)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_TRENDING_FAILED","Trending channels could not be loaded",rid));return}
    defer rows.Close();scores:=make([][]any,0)
    for rows.Next(){var id string;var score int64;if err:=rows.Scan(&id,&score);err!=nil{writeJSON(w,503,errorBody("IPTV_TRENDING_FAILED","Trending channels could not be loaded",rid));return};scores=append(scores,[]any{id,score})}
    if err:=rows.Err();err!=nil{writeJSON(w,503,errorBody("IPTV_TRENDING_FAILED","Trending channels could not be loaded",rid));return}
    writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"scores":scores},"error":nil,"request_id":rid})
  case "report":
    if !s.socialWriteAllowed(w,r,userID,rid,"iptv-report",5){return}
    channelID:=iptvString(in.Payload,"channelId");reason:=iptvString(in.Payload,"reason")
    if channelID==""||len(channelID)>256||len([]rune(reason))<1||len([]rune(reason))>1000{writeJSON(w,400,errorBody("INVALID_REPORT","Channel ID and report reason are required",rid));return}
    _,err:=s.db.Exec(ctx,`INSERT INTO testagram_local.iptv_channel_reports(channel_id,user_id,reason) VALUES($1,$2,$3)`,channelID,userID,reason)
    if err!=nil{writeJSON(w,503,errorBody("IPTV_REPORT_FAILED","Report could not be submitted",rid));return}
    writeJSON(w,201,map[string]any{"ok":true,"data":map[string]any{"reported":true},"error":nil,"request_id":rid})
  default:
    writeJSON(w,400,errorBody("UNSUPPORTED_IPTV_ACTION","This IPTV social action is not supported",rid))
  }
}
