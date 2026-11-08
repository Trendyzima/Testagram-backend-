package main

import (
 "net/http"
 "strings"
 "time"
 "github.com/jackc/pgx/v5"
)

type conversationCreateInput struct { RecipientID string `json:"recipient_id"` }
type directMessageInput struct { Content string `json:"content"` }

func (s *service) conversationCreate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MESSAGING_UNAVAILABLE","Messaging is unavailable",id));return}
 if !s.socialWriteAllowed(w,r,uid,id,"conversation_create",30){return}
 r.Body=http.MaxBytesReader(w,r.Body,4<<10);defer r.Body.Close();var in conversationCreateInput
 if err:=decodeSingleJSON(r.Body,&in);err!=nil||!socialUUIDPattern.MatchString(in.RecipientID)||in.RecipientID==uid{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_RECIPIENT","A valid recipient account is required",id));return}
 var blocked bool
 if err:=s.db.QueryRow(r.Context(),`SELECT EXISTS(SELECT 1 FROM testagram_local.blocks WHERE (blocker_id=$1::uuid AND blocked_id=$2::uuid) OR (blocker_id=$2::uuid AND blocked_id=$1::uuid))`,uid,in.RecipientID).Scan(&blocked);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CONVERSATION_CREATE_FAILED","Conversation could not be created",id));return}
 if blocked{writeJSON(w,http.StatusForbidden,errorBody("MESSAGING_BLOCKED","A block prevents this conversation",id));return}
 var conversationID string
 err:=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.conversations(participant_low,participant_high)
 SELECT LEAST($1::uuid,$2::uuid),GREATEST($1::uuid,$2::uuid)
 WHERE EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$1::uuid AND account_status='active')
 AND EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$2::uuid AND account_status='active')
 ON CONFLICT(participant_low,participant_high) DO UPDATE SET updated_at=now()
 RETURNING id::text`,uid,in.RecipientID).Scan(&conversationID)
 if err==pgx.ErrNoRows{http.NotFound(w,r);return};if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("CONVERSATION_CREATE_FAILED","Conversation could not be created",id));return}
 writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"id":conversationID,"conversation_id":conversationID,"recipient_id":in.RecipientID}})
}

func (s *service) directMessageCreate(w http.ResponseWriter,r *http.Request) {
 uid,id,ok:=s.socialAuth(w,r);if !ok{return};if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MESSAGING_UNAVAILABLE","Messaging is unavailable",id));return}
 if !s.socialWriteAllowed(w,r,uid,id,"direct_message_create",60){return}
 cid:=r.PathValue("conversation_id");if !socialUUIDPattern.MatchString(cid){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_CONVERSATION_ID","Conversation ID is invalid",id));return}
 r.Body=http.MaxBytesReader(w,r.Body,8<<10);defer r.Body.Close();var in directMessageInput
 if err:=decodeSingleJSON(r.Body,&in);err!=nil{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MESSAGE","Message payload is invalid",id));return}
 content:=strings.TrimSpace(in.Content);if content==""||len(content)>4000{writeJSON(w,http.StatusBadRequest,errorBody("INVALID_MESSAGE","Message content must be 1 to 4000 characters",id));return}
 ctx:=r.Context();tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MESSAGE_SEND_FAILED","Message could not be sent",id));return};defer tx.Rollback(ctx)
 var recipient string
 err=tx.QueryRow(ctx,`SELECT CASE WHEN participant_low=$2::uuid THEN participant_high::text ELSE participant_low::text END
 FROM testagram_local.conversations WHERE id=$1::uuid AND (participant_low=$2::uuid OR participant_high=$2::uuid) FOR UPDATE`,cid,uid).Scan(&recipient)
 if err==pgx.ErrNoRows{http.NotFound(w,r);return};if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MESSAGE_SEND_FAILED","Conversation could not be loaded",id));return}
 var blocked bool
 if err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.blocks WHERE (blocker_id=$1::uuid AND blocked_id=$2::uuid) OR (blocker_id=$2::uuid AND blocked_id=$1::uuid))`,uid,recipient).Scan(&blocked);err!=nil||blocked{writeJSON(w,http.StatusForbidden,errorBody("MESSAGING_BLOCKED","A block prevents this message",id));return}
 var messageID string;var created time.Time
 err=tx.QueryRow(ctx,`INSERT INTO testagram_local.direct_messages(conversation_id,sender_id,content) VALUES($1::uuid,$2::uuid,$3) RETURNING id::text,created_at`,cid,uid,content).Scan(&messageID,&created)
 if err==nil{_,err=tx.Exec(ctx,"UPDATE testagram_local.conversations SET last_message_at=$2,updated_at=now() WHERE id=$1::uuid",cid,created)}
 if err==nil{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,event_key,payload)
 VALUES($1::uuid,$2::uuid,'message',$3,jsonb_build_object('conversation_id',$4::text,'message_id',$5::text))
 ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`,recipient,uid,"message:"+messageID,cid,messageID)}
 if err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MESSAGE_SEND_FAILED","Message could not be saved",id));return}
 if err=tx.Commit(ctx);err!=nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("MESSAGE_SEND_FAILED","Message could not be committed",id));return}
 writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":messageID,"message_id":messageID,"conversation_id":cid,"sender_id":uid,"recipient_id":recipient,"content":content,"created_at":created}})
}
