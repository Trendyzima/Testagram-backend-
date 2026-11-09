package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) socialPostInteractionsList(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok { return }
	postID := strings.ToLower(strings.TrimSpace(r.PathValue("post_id")))
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_POST_ID","A valid post ID is required",requestID));return
	}
	limit:=50
	if raw:=strings.TrimSpace(r.URL.Query().Get("limit"));raw!=""{
		n,err:=strconv.Atoi(raw);if err!=nil||n<1||n>100{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_LIMIT","Limit must be between 1 and 100",requestID));return};limit=n
	}
	var query string
	switch kind {
	case "likes":
		query=`SELECT r.id::text,r.user_id::text,''::text,r.created_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.post_reactions r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.post_id=$1 AND r.emoji='❤️' AND a.account_status='active'
			ORDER BY r.created_at DESC,r.id DESC LIMIT $2`
	case "reposts":
		query=`SELECT r.id::text,r.user_id::text,r.quote,r.created_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.reposts r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.post_id=$1 AND a.account_status='active'
			ORDER BY r.created_at DESC,r.id DESC LIMIT $2`
	case "quotes":
		query=`SELECT r.id::text,r.user_id::text,r.quote,r.created_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.reposts r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.post_id=$1 AND btrim(r.quote)<>'' AND a.account_status='active'
			ORDER BY r.created_at DESC,r.id DESC LIMIT $2`
	case "quote-likes":
		writeJSON(w,http.StatusNotImplemented,errorBody("QUOTE_LIKES_NOT_SUPPORTED","Quote likes are not part of the native social schema yet",requestID));return
	default:
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_INTERACTION_KIND","Interaction kind must be likes, reposts, quotes or quote-likes",requestID));return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("INTERACTIONS_UNAVAILABLE","Interactions could not be loaded",requestID));return};defer tx.Rollback(ctx)
	if _,err:=socialCanViewPostReadWith(ctx,tx,userID,postID);errors.Is(err,pgx.ErrNoRows){
		writeJSON(w,http.StatusNotFound,errorBody("POST_NOT_FOUND","The post is unavailable",requestID));return
	}else if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("POST_LOOKUP_FAILED","Post visibility could not be checked",requestID));return}
	rows,err:=tx.Query(ctx,query,postID,limit);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("INTERACTIONS_UNAVAILABLE","Interactions could not be loaded",requestID));return}
	items:=make([]map[string]any,0,limit)
	for rows.Next(){
		var id,actorID,quote,username,displayName,avatarKey string
		var createdAt time.Time
		if err:=rows.Scan(&id,&actorID,&quote,&createdAt,&username,&displayName,&avatarKey);err!=nil{rows.Close();writeJSON(w,http.StatusServiceUnavailable,errorBody("INTERACTIONS_UNAVAILABLE","Interactions could not be decoded",requestID));return}
		profile:=map[string]any{"id":actorID,"user_id":actorID,"username":username,"display_name":displayName,"avatar_url":avatarKey}
		item:=map[string]any{"id":id,"user_id":actorID,"post_id":postID,"created_at":createdAt,"profile":profile,"user_profiles":profile}
		if quote!=""{item["content"]=quote;item["quote"]=quote;item["quoted_post_id"]=postID}
		items=append(items,item)
	}
	if err:=rows.Err();err!=nil{rows.Close();writeJSON(w,http.StatusServiceUnavailable,errorBody("INTERACTIONS_UNAVAILABLE","Interactions could not be loaded",requestID));return}
	rows.Close()
	if err:=tx.Commit(ctx);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("INTERACTIONS_UNAVAILABLE","Interactions could not be confirmed",requestID));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items,"has_more":len(items)==limit},"error":nil,"request_id":requestID})
}
