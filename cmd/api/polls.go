package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type communityPollInput struct {
	Question string `json:"question"`
	Options []string `json:"options"`
	DurationMinutes int `json:"duration_minutes"`
	Description string `json:"description"`
}

func (s *service) communityPollCreate(w http.ResponseWriter,r *http.Request) {
	if s.db==nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_SERVICE_UNAVAILABLE","Poll service is unavailable",requestID(r)));return}
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_poll_create",10){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,16<<10);defer r.Body.Close()
	var in communityPollInput
	if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL","Poll payload is invalid",id));return}
	question:=strings.TrimSpace(in.Question)
	if question==""||len(question)>500||len(in.Options)<2||len(in.Options)>10||len(in.Description)>1000 {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL","A poll needs a question and 2 to 10 valid options",id));return}
	for i:=range in.Options {in.Options[i]=strings.TrimSpace(in.Options[i]);if in.Options[i]==""||len(in.Options[i])>160 {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL_OPTION","Poll options must contain 1 to 160 characters",id));return}}
	duration:=in.DurationMinutes;if duration==0{duration=1440};if duration<5||duration>43200 {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL_DURATION","Poll duration must be between 5 minutes and 30 days",id));return}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_CREATE_FAILED","Poll could not be created",id));return};defer tx.Rollback(ctx)
	var postID string
	err=tx.QueryRow(ctx,`INSERT INTO testagram_local.posts(author_id,community_id,text,visibility,media_ids,language)
		SELECT $1::uuid,c.id,$3,'public','[]'::jsonb,'en' FROM testagram_local.communities c
		WHERE c.id=$2::uuid AND EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$1::uuid AND m.status='active')
		RETURNING id::text`,userID,communityID,question).Scan(&postID)
	if err==pgx.ErrNoRows {writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before creating a poll",id));return}
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_CREATE_FAILED","Poll post could not be created",id));return}
	ends:=time.Now().UTC().Add(time.Duration(duration)*time.Minute)
	var pollID string
	err=tx.QueryRow(ctx,`INSERT INTO testagram_local.polls(post_id,question,description,ends_at) VALUES($1::uuid,$2,$3,$4) RETURNING id::text`,postID,question,strings.TrimSpace(in.Description),ends).Scan(&pollID)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_CREATE_FAILED","Poll could not be saved",id));return}
	options:=make([]map[string]any,0,len(in.Options))
	for i,label:=range in.Options {var optionID string
		if err=tx.QueryRow(ctx,`INSERT INTO testagram_local.poll_options(poll_id,label,position) VALUES($1::uuid,$2,$3) RETURNING id::text`,pollID,label,i).Scan(&optionID);err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_CREATE_FAILED","Poll options could not be saved",id));return}
		options=append(options,map[string]any{"id":optionID,"label":label,"position":i,"votes":0})
	}
	if err=tx.Commit(ctx);err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_CREATE_FAILED","Poll could not be committed",id));return}
	poll:=map[string]any{"id":pollID,"question":question,"description":strings.TrimSpace(in.Description),"status":"open","ends_at":ends,"total_votes":0,"options":options}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"post_id":postID,"poll":poll}})
}

func (s *service) pollGet(w http.ResponseWriter,r *http.Request) {
	id:=requestID(r);if !s.cors(w,r){writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id));return}
	if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_SERVICE_UNAVAILABLE","Poll service is unavailable",id));return}
	pollID:=r.PathValue("poll_id");if !socialUUIDPattern.MatchString(pollID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL_ID","Poll ID is invalid",id));return}
	userID:=""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer "){var ok bool;userID,_,ok=s.socialAuth(w,r);if !ok{return}}
	var poll map[string]any
	var postCommunity string
	err:=s.db.QueryRow(r.Context(),`SELECT jsonb_build_object('id',p.id,'question',p.question,'description',p.description,'status',CASE WHEN p.status='open' AND (p.ends_at IS NULL OR p.ends_at>now()) THEN 'open' ELSE 'closed' END,'ends_at',p.ends_at,'total_votes',p.total_votes,
	'options',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',o.id,'label',o.label,'position',o.position,'votes',o.votes) ORDER BY o.position) FROM testagram_local.poll_options o WHERE o.poll_id=p.id),'[]'::jsonb)),
	COALESCE(c.id::text,'')
	FROM testagram_local.polls p JOIN testagram_local.posts post ON post.id=p.post_id LEFT JOIN testagram_local.communities c ON c.id=post.community_id
	WHERE p.id=$1::uuid AND post.deleted_at IS NULL
	AND (post.visibility='public' OR post.author_id=NULLIF($2,'')::uuid OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'))`,pollID,userID).Scan(&poll,&postCommunity)
	if err==pgx.ErrNoRows{http.NotFound(w,r);return};if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_READ_FAILED","Poll could not be loaded",id));return}
	if userID!="" {var selected string;err=s.db.QueryRow(r.Context(),`SELECT option_id::text FROM testagram_local.poll_votes WHERE poll_id=$1::uuid AND voter_id=$2::uuid`,pollID,userID).Scan(&selected);if err==nil{poll["selected_option_id"]=selected}else if err!=pgx.ErrNoRows{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_READ_FAILED","Your poll vote could not be loaded",id));return}}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"poll":poll}})
}

func (s *service) pollVote(w http.ResponseWriter,r *http.Request) {
	if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_SERVICE_UNAVAILABLE","Poll service is unavailable",requestID(r)));return}
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"poll_vote",30){return}
	pollID:=r.PathValue("poll_id");if !socialUUIDPattern.MatchString(pollID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL_ID","Poll ID is invalid",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,2<<10);defer r.Body.Close()
	var in struct{OptionID string `json:"option_id"`}
	if err:=decodeSingleJSON(r.Body,&in);err!=nil||!socialUUIDPattern.MatchString(in.OptionID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POLL_OPTION","Poll option is invalid",id));return}
	ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second);defer cancel()
	tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_VOTE_FAILED","Vote could not be recorded",id));return};defer tx.Rollback(ctx)
	var open bool
	err=tx.QueryRow(ctx,`SELECT (p.status='open' AND (p.ends_at IS NULL OR p.ends_at>now()) AND (c.id IS NULL OR c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$2::uuid AND m.status='active')))
	FROM testagram_local.polls p JOIN testagram_local.posts post ON post.id=p.post_id LEFT JOIN testagram_local.communities c ON c.id=post.community_id
	WHERE p.id=$1::uuid FOR UPDATE OF p`,pollID,userID).Scan(&open)
	if err==pgx.ErrNoRows||!open{writeJSON(w,http.StatusConflict,errorBody("POLL_CLOSED","Poll is unavailable or voting has closed",id));return}
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_VOTE_FAILED","Poll could not be checked",id));return}
	_,err=tx.Exec(ctx,`INSERT INTO testagram_local.poll_votes(poll_id,option_id,voter_id) VALUES($1::uuid,$2::uuid,$3::uuid)`,pollID,in.OptionID,userID)
	if err!=nil{writeJSON(w,http.StatusConflict,errorBody("POLL_ALREADY_VOTED","You have already voted or the selected option is invalid",id));return}
	_,err=tx.Exec(ctx,`UPDATE testagram_local.poll_options SET votes=votes+1 WHERE poll_id=$1::uuid AND id=$2::uuid`,pollID,in.OptionID)
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_VOTE_FAILED","Vote count could not be updated",id));return}
	_,err=tx.Exec(ctx,`UPDATE testagram_local.polls SET total_votes=total_votes+1 WHERE id=$1::uuid`,pollID)
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_VOTE_FAILED","Poll count could not be updated",id));return}
	if err=tx.Commit(ctx);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_VOTE_FAILED","Vote could not be committed",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"poll_id":pollID,"option_id":in.OptionID,"voted":true}})
}

func (s *service) pollGetByPost(w http.ResponseWriter,r *http.Request) {
	postID:=r.PathValue("post_id")
	if !socialUUIDPattern.MatchString(postID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POST_ID","Post ID is invalid",requestID(r)));return}
	var pollID string
	err:=s.db.QueryRow(r.Context(),`SELECT id::text FROM testagram_local.polls WHERE post_id=$1::uuid`,postID).Scan(&pollID)
	if err==pgx.ErrNoRows{http.NotFound(w,r);return}
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POLL_READ_FAILED","Poll could not be loaded",requestID(r)));return}
	r.SetPathValue("poll_id",pollID)
	s.pollGet(w,r)
}
