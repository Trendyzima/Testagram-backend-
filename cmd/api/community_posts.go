package main

import (
	"encoding/json"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type communityPostInput struct {
	Text     string   `json:"text"`
	Content  string   `json:"content"`
	MediaIDs []string `json:"media_ids"`
	Language string   `json:"language"`
}

func (s *service) communityPostsList(w http.ResponseWriter, r *http.Request) {
	id:=requestID(r)
	if !s.cors(w,r) { writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",id)); return }
	if s.db==nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_DATABASE_UNAVAILABLE","Community data service is unavailable",id)); return }
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID) { writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id)); return }
	userID:=""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))),"bearer ") {
		var ok bool
		userID,_,ok=s.socialAuth(w,r)
		if !ok { return }
	}
	limit:=25
	if raw:=r.URL.Query().Get("limit"); raw!="" { if n,err:=strconv.Atoi(raw); err==nil { if n<1 {n=1}; if n>100 {n=100}; limit=n } }
	page:=0
	if raw:=r.URL.Query().Get("page"); raw!="" { if n,err:=strconv.Atoi(raw); err==nil && n>0 {page=n} }
	if page>10000/limit {page=10000/limit}
	rows,err:=s.db.Query(r.Context(),`SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,p.language,p.created_at,p.updated_at,
		a.username,a.display_name,a.avatar_key,
		(SELECT count(*) FROM testagram_local.post_reactions reaction WHERE reaction.post_id=p.id) AS likes_count,
		(SELECT count(*) FROM testagram_local.replies reply WHERE reply.post_id=p.id AND reply.deleted_at IS NULL) AS replies_count,
		EXISTS(SELECT 1 FROM testagram_local.post_reactions mine WHERE mine.post_id=p.id AND mine.user_id=NULLIF($2,'')::uuid) AS liked,
		poll.id::text,poll.question,poll.status,poll.ends_at,poll.total_votes
		FROM testagram_local.posts p
		JOIN testagram_local.accounts a ON a.user_id=p.author_id
		JOIN testagram_local.communities c ON c.id=p.community_id
		LEFT JOIN testagram_local.polls poll ON poll.post_id=p.id
		WHERE p.community_id=$1::uuid AND p.deleted_at IS NULL
		AND (p.visibility='public' OR p.author_id=NULLIF($2,'')::uuid OR EXISTS(
			SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'))
		AND (c.visibility='public' OR EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=NULLIF($2,'')::uuid AND m.status='active'))
		ORDER BY p.created_at DESC,p.id DESC LIMIT $3 OFFSET $4`,communityID,userID,limit,page*limit)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_POSTS_FAILED","Community posts could not be loaded",id));return}
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var postID,authorID,content,visibility,language,username,displayName,avatar string
		var mediaJSON []byte
		var createdAt,updatedAt time.Time
		var likes,replies int64
		var liked bool
		var pollID,pollQuestion,pollStatus sql.NullString
		var pollEndsAt sql.NullTime
		var pollTotalVotes int64
		if err=rows.Scan(&postID,&authorID,&content,&visibility,&mediaJSON,&language,&createdAt,&updatedAt,&username,&displayName,&avatar,&likes,&replies,&liked,&pollID,&pollQuestion,&pollStatus,&pollEndsAt,&pollTotalVotes);err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_POSTS_FAILED","Community post records could not be read",id));return
		}
		var mediaIDs []string
		_ = json.Unmarshal(mediaJSON,&mediaIDs)
		profile:=map[string]any{"id":authorID,"username":username,"display_name":displayName,"avatar_url":avatar}
		var poll any
		if pollID.Valid { poll=map[string]any{"id":pollID.String,"question":pollQuestion.String,"status":pollStatus.String,"ends_at":nil,"total_votes":pollTotalVotes}; if pollEndsAt.Valid { poll.(map[string]any)["ends_at"]=pollEndsAt.Time } }
		items=append(items,map[string]any{"poll":poll,"id":postID,"community_id":communityID,"author_id":authorID,"user_id":authorID,"text":content,"content":content,"visibility":visibility,"media_ids":mediaIDs,"language":language,"created_at":createdAt,"updated_at":updatedAt,"likes_count":likes,"replies_count":replies,"liked_by_me":liked,"user_profiles":profile,"profiles":profile})
	}
	if err=rows.Err();err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_POSTS_FAILED","Community post records could not be read",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"next_cursor":nil}})
}

func (s *service) communityPostsCreate(w http.ResponseWriter,r *http.Request) {
	if s.db==nil { writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_DATABASE_UNAVAILABLE","Community data service is unavailable",requestID(r))); return }
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_post_create",20){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,32<<10);defer r.Body.Close()
	var in communityPostInput
	if err:=decodeSingleJSON(r.Body,&in);err!=nil {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_POST","Post payload is invalid",id));return}
	content:=strings.TrimSpace(in.Text);if content=="" {content=strings.TrimSpace(in.Content)}
	if len(content)>10000||len(in.MediaIDs)>20||len(in.Language)>32||(content==""&&len(in.MediaIDs)==0) {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_POST","Post content or media is invalid",id));return}
	if in.MediaIDs==nil {in.MediaIDs=[]string{}}
	mediaJSON,_:=json.Marshal(in.MediaIDs)
	var postID string
	err:=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.posts(author_id,community_id,text,visibility,media_ids,language)
		SELECT $1::uuid,c.id,$3,'public',$4::jsonb,$5 FROM testagram_local.communities c
		WHERE c.id=$2::uuid AND EXISTS(SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$1::uuid AND m.status='active')
		RETURNING id::text`,userID,communityID,content,string(mediaJSON),in.Language).Scan(&postID)
	if err==pgx.ErrNoRows {writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before posting",id));return}
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_POST_CREATE_FAILED","Community post could not be saved",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":postID,"post_id":postID,"community_id":communityID,"author_id":userID,"user_id":userID,"text":content,"content":content,"media_ids":in.MediaIDs}})
}
