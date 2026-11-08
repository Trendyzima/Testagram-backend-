package main

import (
 "net/http"
 "strings"
 "time"
 "net/url"
 "github.com/jackc/pgx/v5"
)

type storyCreateInput struct { MediaURL string `json:"media_url"`; MediaType string `json:"media_type"`; Caption string `json:"caption"`; ExpiresAt *time.Time `json:"expires_at"` }

func (s *service) storiesExplore(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("STORIES_UNAVAILABLE","Stories are unavailable",id));return}
 limit:=30;if raw:=r.URL.Query().Get("limit");raw!=""{if n,e:=strconv.Atoi(raw);e==nil{if n<1{n=1};if n>100{n=100};limit=n}}
 rows,err:=s.db.Query(r.Context(),`SELECT st.id::text,st.user_id::text,st.media_url,st.media_type,st.caption,st.views_count,st.expires_at,st.created_at,a.username,a.display_name,a.avatar_key,a.verified_tier
 FROM testagram_local.stories st JOIN testagram_local.accounts a ON a.user_id=st.user_id
 WHERE st.deleted_at IS NULL AND st.expires_at>now() AND a.account_status='active' AND a.discoverable_by_username AND NOT a.protected_account
 ORDER BY st.views_count DESC,st.created_at DESC LIMIT $1`,limit)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("STORIES_LIST_FAILED","Stories could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var sid,uid,mediaURL,mediaType,caption,username,display,avatar,tier string;var views int64;var expires,created time.Time
  if err=rows.Scan(&sid,&uid,&mediaURL,&mediaType,&caption,&views,&expires,&created,&username,&display,&avatar,&tier);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("STORIES_LIST_FAILED","Story records could not be read",id));return}
  items=append(items,map[string]any{"id":sid,"user_id":uid,"media_url":mediaURL,"media_type":mediaType,"caption":caption,"views_count":views,"expires_at":expires,"created_at":created,"user_profiles":map[string]any{"id":uid,"username":username,"display_name":display,"avatar_url":avatar,"verified_tier":tier}})
 }
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) storyCreate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"story_create",10){return}
 r.Body=http.MaxBytesReader(w,r.Body,8<<10);defer r.Body.Close();var in storyCreateInput
 if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_STORY","Story payload is invalid",id));return}
 parsed,err:=url.Parse(strings.TrimSpace(in.MediaURL))
 if err!=nil||parsed.Scheme!="https"||parsed.Host==""||(in.MediaType!="image"&&in.MediaType!="video")||len(in.Caption)>1000{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_STORY","Story media or caption is invalid",id));return}
 expires:=time.Now().UTC().Add(24*time.Hour);if in.ExpiresAt!=nil{expires=in.ExpiresAt.UTC()}
 if !expires.After(time.Now().UTC())||expires.After(time.Now().UTC().Add(48*time.Hour)){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_STORY_EXPIRY","Story expiry must be within 48 hours",id));return}
 var sid string
 err=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.stories(user_id,media_url,media_type,caption,expires_at) VALUES($1::uuid,$2,$3,$4,$5) RETURNING id::text`,uid,strings.TrimSpace(in.MediaURL),in.MediaType,strings.TrimSpace(in.Caption),expires).Scan(&sid)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("STORY_CREATE_FAILED","Story could not be created",id));return}
 writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":sid,"user_id":uid,"media_url":in.MediaURL,"media_type":in.MediaType,"caption":in.Caption,"views_count":0,"expires_at":expires}})
}

func (s *service) storyViewCreate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"story_view",120){return}
 sid:=r.PathValue("story_id");if !socialUUIDPattern.MatchString(sid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_STORY_ID","Story ID is invalid",id));return}
 tag,err:=s.db.Exec(r.Context(),`INSERT INTO testagram_local.story_views(story_id,viewer_id)
 SELECT id,$2::uuid FROM testagram_local.stories WHERE id=$1::uuid AND deleted_at IS NULL AND expires_at>now()
 ON CONFLICT DO NOTHING`,sid,uid)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("STORY_VIEW_FAILED","Story view could not be recorded",id));return}
 if tag.RowsAffected()==0{var exists bool;_ = s.db.QueryRow(r.Context(),"SELECT EXISTS(SELECT 1 FROM testagram_local.stories WHERE id=$1::uuid AND deleted_at IS NULL AND expires_at>now())",sid).Scan(&exists);if !exists{http.NotFound(w,r);return}}
 var count int64;if err=s.db.QueryRow(r.Context(),"SELECT views_count FROM testagram_local.stories WHERE id=$1::uuid",sid).Scan(&count);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("STORY_VIEW_FAILED","Story view count could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"story_id":sid,"viewed":true,"created":tag.RowsAffected()>0,"views_count":count}})
}

var _ = pgx.ErrNoRows
