package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type communityChatInput struct { Message string `json:"message"` }
type communityChatReactionInput struct { Emoji string `json:"emoji"` }

func (s *service) communityChatList(w http.ResponseWriter,r *http.Request) {
	id:=requestID(r); userID,_,ok:=s.socialAuth(w,r);if !ok{return}
	communityID:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	var member bool
	if err:=s.db.QueryRow(r.Context(),`SELECT EXISTS(SELECT 1 FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active')`,communityID,userID).Scan(&member);err!=nil||!member{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community to read its chat",id));return}
	rows,err:=s.db.Query(r.Context(),`SELECT m.id::text,m.user_id::text,m.message,m.created_at,a.username,a.display_name,a.avatar_key,
		COALESCE((SELECT jsonb_object_agg(x.emoji,x.total) FROM (SELECT emoji,count(*) AS total FROM testagram_local.community_chat_reactions WHERE message_id=m.id GROUP BY emoji) x),'{}'::jsonb),
		EXISTS(SELECT 1 FROM testagram_local.community_chat_pins p WHERE p.community_id=m.community_id AND p.message_id=m.id)
		FROM testagram_local.community_chat_messages m JOIN testagram_local.accounts a ON a.user_id=m.user_id
		WHERE m.community_id=$1::uuid AND m.deleted_at IS NULL ORDER BY m.created_at ASC,m.id ASC LIMIT 100`,communityID)
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_CHAT_FAILED","Community chat could not be loaded",id));return};defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next(){var mid,uid,message,username,displayName,avatar string;var created time.Time;var reactions []byte;var pinned bool
		if err=rows.Scan(&mid,&uid,&message,&created,&username,&displayName,&avatar,&reactions,&pinned);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_CHAT_FAILED","Chat records could not be read",id));return}
		profile:=map[string]any{"id":uid,"username":username,"display_name":displayName,"avatar_url":avatar}
		items=append(items,map[string]any{"id":mid,"community_id":communityID,"user_id":uid,"message":message,"created_at":created,"profiles":profile,"user_profiles":profile,"reactions":jsonRaw(reactions),"is_pinned":pinned})
	}
	if err=rows.Err();err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_CHAT_FAILED","Chat records could not be read",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"items":items}})
}

func jsonRaw(raw []byte) any { if len(raw)==0{return map[string]any{}};return string(raw) }

func (s *service) communityChatSend(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_chat_send",30){return}
	communityID:=r.PathValue("community_id");if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,8<<10);defer r.Body.Close();var in communityChatInput
	if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHAT_MESSAGE","Chat message is invalid",id));return}
	message:=strings.TrimSpace(in.Message);if message==""||len(message)>2000{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHAT_MESSAGE","Chat message must contain 1 to 2000 characters",id));return}
	var mid string
	err:=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.community_chat_messages(community_id,user_id,message)
		SELECT c.id,$2::uuid,$3 FROM testagram_local.communities c WHERE c.id=$1::uuid AND EXISTS(
			SELECT 1 FROM testagram_local.community_memberships m WHERE m.community_id=c.id AND m.user_id=$2::uuid AND m.status='active')
		RETURNING id::text`,communityID,userID,message).Scan(&mid)
	if err==pgx.ErrNoRows{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Join this community before sending chat messages",id));return}
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_CHAT_SEND_FAILED","Chat message could not be saved",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":mid,"community_id":communityID,"user_id":userID,"message":message}})
}

func (s *service) communityChatReact(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_chat_react",60){return}
	communityID:=r.PathValue("community_id");messageID:=r.PathValue("message_id")
	if !socialUUIDPattern.MatchString(communityID)||!socialUUIDPattern.MatchString(messageID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHAT_MESSAGE_ID","Chat message ID is invalid",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,2<<10);defer r.Body.Close();var in communityChatReactionInput
	if err:=decodeSingleJSON(r.Body,&in);err!=nil||len(in.Emoji)==0||len(in.Emoji)>16{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHAT_REACTION","Reaction emoji is invalid",id));return}
	var member bool
	err:=s.db.QueryRow(r.Context(),`SELECT EXISTS(SELECT 1 FROM testagram_local.community_memberships m JOIN testagram_local.community_chat_messages c ON c.community_id=m.community_id WHERE m.community_id=$1::uuid AND c.id=$2::uuid AND m.user_id=$3::uuid AND m.status='active')`,communityID,messageID,userID).Scan(&member)
	if err!=nil||!member{writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MEMBERSHIP_REQUIRED","Community membership is required",id));return}
	tag,err:=s.db.Exec(r.Context(),`DELETE FROM testagram_local.community_chat_reactions WHERE message_id=$1::uuid AND user_id=$2::uuid AND emoji=$3`,messageID,userID,in.Emoji)
	active:=false
	if err==nil&&tag.RowsAffected()==0{_,err=s.db.Exec(r.Context(),`INSERT INTO testagram_local.community_chat_reactions(message_id,user_id,emoji) VALUES($1::uuid,$2::uuid,$3) ON CONFLICT DO NOTHING`,messageID,userID,in.Emoji);active=err==nil}
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHAT_REACTION_FAILED","Reaction could not be updated",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"message_id":messageID,"emoji":in.Emoji,"active":active}})
}

func (s *service) communityChatPin(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_chat_pin",20){return}
	communityID:=r.PathValue("community_id");messageID:=r.PathValue("message_id")
	if !socialUUIDPattern.MatchString(communityID)||!socialUUIDPattern.MatchString(messageID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CHAT_MESSAGE_ID","Chat message ID is invalid",id));return}
	var role string
	err:=s.db.QueryRow(r.Context(),`SELECT role FROM testagram_local.community_memberships WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'`,communityID,userID).Scan(&role)
	if err!=nil||!(role=="owner"||role=="admin"||role=="moderator"){writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MODERATOR_REQUIRED","Only community moderators can pin messages",id));return}
	tag,err:=s.db.Exec(r.Context(),`DELETE FROM testagram_local.community_chat_pins WHERE community_id=$1::uuid AND message_id=$2::uuid`,communityID,messageID)
	pinned:=false
	if err==nil&&tag.RowsAffected()==0{_,err=s.db.Exec(r.Context(),`INSERT INTO testagram_local.community_chat_pins(community_id,message_id,pinned_by) SELECT $1::uuid,m.id,$3::uuid FROM testagram_local.community_chat_messages m WHERE m.id=$2::uuid AND m.community_id=$1::uuid ON CONFLICT DO NOTHING`,communityID,messageID,userID);pinned=err==nil}
	if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CHAT_PIN_FAILED","Message pin could not be updated",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"message_id":messageID,"pinned":pinned}})
}
