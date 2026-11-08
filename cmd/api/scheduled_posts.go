package main

import (
	"net/http"
	"strings"
	"time"

)

type scheduledPostInput struct {
	Content string `json:"content"`
	ScheduledFor time.Time `json:"scheduled_for"`
	CommunityID string `json:"community_id"`
}

func (s *service) scheduledPostCreate(w http.ResponseWriter,r *http.Request) {
	if s.db==nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POSTS_UNAVAILABLE","Scheduling service is unavailable",requestID(r))); return }
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
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POST_CREATE_FAILED","Scheduled post could not be saved",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"scheduled_post_id":scheduledID,"user_id":userID,"community_id":communityID,"content":content,"scheduled_for":in.ScheduledFor.UTC(),"status":"pending","scheduled":true}})
}

func (s *service) scheduledPostsList(w http.ResponseWriter,r *http.Request) {
	if s.db==nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POSTS_UNAVAILABLE","Scheduling service is unavailable",requestID(r))); return }
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	rows,err:=s.db.Query(r.Context(),`SELECT id::text,community_id::text,content,scheduled_for,status,created_at FROM testagram_local.scheduled_posts WHERE user_id=$1::uuid AND status IN ('pending','failed') ORDER BY scheduled_for ASC,id ASC LIMIT 100`,userID)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POST_LIST_FAILED","Scheduled posts could not be loaded",id));return}
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next(){var sid,content,status string;var communityID *string;var scheduledFor,createdAt time.Time
		if err=rows.Scan(&sid,&communityID,&content,&scheduledFor,&status,&createdAt);err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POST_LIST_FAILED","Scheduled post records could not be read",id));return}
		items=append(items,map[string]any{"id":sid,"scheduled_post_id":sid,"community_id":communityID,"content":content,"scheduled_for":scheduledFor,"status":status,"created_at":createdAt})
	}
	if err=rows.Err();err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POST_LIST_FAILED","Scheduled post records could not be read",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items}})
}

func (s *service) scheduledPostCancel(w http.ResponseWriter,r *http.Request) {
	if s.db==nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POSTS_UNAVAILABLE","Scheduling service is unavailable",requestID(r))); return }
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	scheduledID:=r.PathValue("scheduled_post_id")
	if !socialUUIDPattern.MatchString(scheduledID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_SCHEDULED_POST_ID","Scheduled post ID is invalid",id));return}
	tag,err:=s.db.Exec(r.Context(),`UPDATE testagram_local.scheduled_posts SET status='cancelled',updated_at=now() WHERE id=$1::uuid AND user_id=$2::uuid AND status='pending'`,scheduledID,userID)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("SCHEDULED_POST_CANCEL_FAILED","Scheduled post could not be cancelled",id));return}
	if tag.RowsAffected()==0 {writeJSON(w,http.StatusConflict,errorBody("SCHEDULED_POST_NOT_PENDING","Scheduled post is no longer pending",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"scheduled_post_id":scheduledID,"cancelled":true}})
}
