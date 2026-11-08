package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type communityEventInput struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
	Location    string     `json:"location"`
	EventURL    string     `json:"event_url"`
	Capacity    *int       `json:"capacity"`
}

func (s *service) communityEventsList(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_DATABASE_UNAVAILABLE", "Community data service is unavailable", id))
		return
	}
	userID, ok := s.communityIdentity(w, r)
	if !ok { return }
	communityID := r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_COMMUNITY_ID", "Community ID is invalid", id))
		return
	}
	allowed, _, err := s.communityCanView(r.Context(), communityID, userID)
	if err == pgx.ErrNoRows { http.NotFound(w, r); return }
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_ACCESS_FAILED", "Community access could not be verified", id))
		return
	}
	if !allowed { http.NotFound(w, r); return }

	rows, err := s.db.Query(r.Context(), `SELECT e.id::text,e.title,e.description,e.starts_at,e.ends_at,e.location,e.event_url,e.capacity,
		e.creator_id::text,e.created_at,
		(SELECT count(*) FROM testagram_local.community_event_rsvps r WHERE r.event_id=e.id AND r.status='going'),
		EXISTS(SELECT 1 FROM testagram_local.community_event_rsvps mine WHERE mine.event_id=e.id AND mine.user_id=NULLIF($2,'')::uuid AND mine.status='going')
		FROM testagram_local.community_events e WHERE e.community_id=$1::uuid
		AND ($3::boolean OR e.starts_at>=now()-interval '30 days')
		ORDER BY e.starts_at ASC,e.id LIMIT 100`, communityID, userID, r.URL.Query().Get("include_past")=="true")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_EVENTS_FAILED", "Community events could not be loaded", id))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var eventID,title,description,location,eventURL,creatorID string
		var startsAt time.Time
		var endsAt *time.Time
		var capacity *int
		var createdAt time.Time
		var attendeeCount int64
		var isAttending bool
		if err=rows.Scan(&eventID,&title,&description,&startsAt,&endsAt,&location,&eventURL,&capacity,&creatorID,&createdAt,&attendeeCount,&isAttending);err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENTS_FAILED","Community event records could not be read",id));return
		}
		items=append(items,map[string]any{"id":eventID,"community_id":communityID,"title":title,"description":description,
			"starts_at":startsAt,"ends_at":endsAt,"location":location,"event_url":eventURL,"capacity":capacity,
			"creator_id":creatorID,"created_at":createdAt,"attendee_count":attendeeCount,"is_attending":isAttending})
	}
	if err=rows.Err();err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENTS_FAILED","Community event records could not be read",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communityEventCreate(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_event_create",5){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	var role string
	err:=s.db.QueryRow(r.Context(),`SELECT role FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'`,communityID,userID).Scan(&role)
	if err!=nil || (role!="owner" && role!="admin" && role!="moderator"){writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MODERATOR_REQUIRED","Community moderator access is required",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,16<<10);defer r.Body.Close()
	var in communityEventInput
	if err=decodeSingleJSON(r.Body,&in);err!=nil || len(strings.TrimSpace(in.Title))<3 || len(in.Title)>120 || len(in.Description)>4000 || len(in.Location)>240 || len(in.EventURL)>2048 || in.StartsAt.IsZero() || (in.EndsAt!=nil&&!in.EndsAt.After(in.StartsAt)) || (in.Capacity!=nil&&(*in.Capacity<1||*in.Capacity>100000)){
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_EVENT","Community event fields are invalid",id));return
	}
	var eventID string
	err=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.community_events(community_id,creator_id,title,description,starts_at,ends_at,location,event_url,capacity)
		VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`,
		communityID,userID,strings.TrimSpace(in.Title),strings.TrimSpace(in.Description),in.StartsAt,in.EndsAt,strings.TrimSpace(in.Location),strings.TrimSpace(in.EventURL),in.Capacity).Scan(&eventID)
	if err!=nil {writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_EVENT_CREATE_FAILED","Community event could not be created",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":eventID,"community_id":communityID,"title":strings.TrimSpace(in.Title),"starts_at":in.StartsAt}})
}

func (s *service) communityEventRSVP(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_event_rsvp",30){return}
	eventID:=r.PathValue("event_id")
	if !socialUUIDPattern.MatchString(eventID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_EVENT_ID","Event ID is invalid",id));return}
	if r.Method==http.MethodDelete {
		_,err:=s.db.Exec(r.Context(),`DELETE FROM testagram_local.community_event_rsvps WHERE event_id=$1::uuid AND user_id=$2::uuid`,eventID,userID)
		if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENT_RSVP_FAILED","Event RSVP could not be cancelled",id));return}
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"event_id":eventID,"attending":false}})
		return
	}
	var communityID string
	err:=s.db.QueryRow(r.Context(),`SELECT e.community_id::text FROM testagram_local.community_events e
		JOIN testagram_local.communities c ON c.id=e.community_id
		WHERE e.id=$1::uuid AND (c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$2::uuid AND m.status='active'))`,eventID,userID).Scan(&communityID)
	if err==pgx.ErrNoRows {http.NotFound(w,r);return}
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_EVENT_LOOKUP_FAILED","Event is unavailable",id));return}
	tag,err:=s.db.Exec(r.Context(),`INSERT INTO testagram_local.community_event_rsvps(event_id,user_id,status)
		VALUES($1::uuid,$2::uuid,'going') ON CONFLICT(event_id,user_id) DO UPDATE SET status='going',updated_at=now()`,eventID,userID)
	if err!=nil {writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_EVENT_RSVP_FAILED","Event RSVP could not be saved",id));return}
	_ = tag
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"event_id":eventID,"community_id":communityID,"attending":true}})
}
