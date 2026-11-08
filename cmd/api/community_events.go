package main

import (
 "net/http"
 "strings"
 "time"
 "github.com/jackc/pgx/v5"
)

type communityEventInput struct { Title string `json:"title"`; Description string `json:"description"`; StartsAt time.Time `json:"starts_at"` }

func (s *service) communityEventsList(w http.ResponseWriter,r *http.Request) {
 id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
 if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENTS_UNAVAILABLE","Community events are unavailable",id));return}
 cid:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(cid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 uid:="";if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;uid,_,ok=s.socialAuth(w,r);if !ok{return}}
 rows,err:=s.db.Query(r.Context(),`SELECT e.id::text,e.community_id::text,e.creator_id::text,e.title,e.description,e.starts_at,e.created_at,
 EXISTS(SELECT 1 FROM testagram_local.community_event_rsvps r WHERE r.event_id=e.id AND r.user_id=NULLIF($2,'')::uuid),
 (SELECT count(*) FROM testagram_local.community_event_rsvps r WHERE r.event_id=e.id)
 FROM testagram_local.community_events e JOIN testagram_local.communities c ON c.id=e.community_id
 WHERE e.community_id=$1::uuid AND (c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'))
 ORDER BY e.starts_at ASC,e.id ASC LIMIT 100`,cid,uid)
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENTS_FAILED","Community events could not be loaded",id));return};defer rows.Close()
 items:=make([]map[string]any,0)
 for rows.Next(){var eid,community,creator,title,description string;var starts,created time.Time;var attending bool;var count int64
  if err=rows.Scan(&eid,&community,&creator,&title,&description,&starts,&created,&attending,&count);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENTS_FAILED","Community event records could not be read",id));return}
  items=append(items,map[string]any{"id":eid,"community_id":community,"creator_id":creator,"title":title,"description":description,"starts_at":starts,"created_at":created,"is_attending":attending,"attendee_count":count})
 }
 if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENTS_FAILED","Community event records could not be read",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communityEventCreate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"community_event_create",10){return}
 cid:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(cid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
 r.Body=http.MaxBytesReader(w,r.Body,12<<10);defer r.Body.Close();var in communityEventInput
 if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_EVENT","Event payload is invalid",id));return}
 in.Title=strings.TrimSpace(in.Title);in.Description=strings.TrimSpace(in.Description)
 if len(in.Title)<3||len(in.Title)>160||len(in.Description)>4000||in.StartsAt.IsZero()||!in.StartsAt.After(time.Now().UTC())||in.StartsAt.After(time.Now().UTC().AddDate(2,0,0)){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_EVENT","Event title, description or start time is invalid",id));return}
 var eventID string
 err:=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.community_events(community_id,creator_id,title,description,starts_at)
 SELECT c.id,$2::uuid,$3,$4,$5 FROM testagram_local.communities c WHERE c.id=$1::uuid AND EXISTS(
 SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$2::uuid AND m.status='active')
 RETURNING id::text`,cid,uid,in.Title,in.Description,in.StartsAt.UTC()).Scan(&eventID)
 if err==pgx.ErrNoRows{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before creating events",id));return}
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENT_CREATE_FAILED","Community event could not be saved",id));return}
 writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":eventID,"community_id":cid,"creator_id":uid,"title":in.Title,"description":in.Description,"starts_at":in.StartsAt.UTC()}})
}

func (s *service) communityEventRSVP(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if !s.socialWriteAllowed(w,r,uid,id,"community_event_rsvp",30){return}
 eid:=r.PathValue("event_id");if !socialUUIDPattern.MatchString(eid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_EVENT_ID","Event ID is invalid",id));return}
 if r.Method==http.MethodPost {
  tag,err:=s.db.Exec(r.Context(),`INSERT INTO testagram_local.community_event_rsvps(event_id,user_id)
  SELECT e.id,$2::uuid FROM testagram_local.community_events e JOIN testagram_local.community_memberships m ON m.community_id=e.community_id
  WHERE e.id=$1::uuid AND m.user_id=$2::uuid AND m.status='active' ON CONFLICT DO NOTHING`,eid,uid)
  if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("EVENT_RSVP_FAILED","Event RSVP could not be saved",id));return}
  _=tag
 } else if r.Method==http.MethodDelete {
  _,err:=s.db.Exec(r.Context(),"DELETE FROM testagram_local.community_event_rsvps WHERE event_id=$1::uuid AND user_id=$2::uuid",eid,uid)
  if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("EVENT_RSVP_FAILED","Event RSVP could not be cancelled",id));return}
 } else {http.Error(w,"method not allowed",http.StatusMethodNotAllowed);return}
 var attending bool
 if err:=s.db.QueryRow(r.Context(),"SELECT EXISTS(SELECT 1 FROM testagram_local.community_event_rsvps WHERE event_id=$1::uuid AND user_id=$2::uuid)",eid,uid).Scan(&attending);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("EVENT_RSVP_FAILED","Event RSVP state could not be read",id));return}
 if r.Method==http.MethodPost&&!attending{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before RSVPing",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"event_id":eid,"attending":attending}})
}
