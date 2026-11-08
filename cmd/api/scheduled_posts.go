package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type scheduledPostInput struct {
	Content string `json:"content"`
	ScheduledFor time.Time `json:"scheduled_for"`
	CommunityID string `json:"community_id"`
}

func (s *service) scheduledPostCreate(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"scheduled_post_create",20){return}
	r.Body=http.MaxBytesReader(w,r.Body,16<<10);defer r.Body.Close()
	var in scheduledPostInput
	if err:=decodeSingleJSON(r.Body,&in);err!=nil {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_SCHEDULED_POST","Scheduled post payload is invalid",id));return}
	content:=strings.TrimSpace(in.Content)
	now:=time.Now().UTC()
	if content==""||len(content)>10000||in.ScheduledFor.IsZero()||!in.ScheduledFor.After(now)||in.ScheduledFor.After(now.AddDate(1,0,0)) {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_SCHEDULED_POST","Content is required and scheduled time must be within the next year",id));return
	}
	var communityID any
	if strings.TrimSpace(in.CommunityID)!="" {
		if !socialUUIDPattern.MatchString(in.CommunityID) {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
		var allowed bool
		err:=s.db.QueryRow(r.Context(),`SELECT EXISTS(SELECT 1 FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active')`,in.CommunityID,userID).Scan(&allowed)
		if err!=nil||!allowed {writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before scheduling a community post",id));return}
		communityID=in.CommunityID
	}
	var scheduledID string
	err:=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.scheduled_posts(user_id,community_id,content,scheduled_for)
		VALUES($1::uuid,$2::uuid,$3,$4) RETURNING id::text`,userID,communityID,content,in.ScheduledFor.UTC()).Scan(&scheduledID)
	if err==pgx.ErrNoRows||err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POST_CREATE_FAILED","Scheduled post could not be saved",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"scheduled_post_id":scheduledID,"user_id":userID,"community_id":communityID,"content":content,"scheduled_for":in.ScheduledFor.UTC(),"status":"pending","scheduled":true}})
}
