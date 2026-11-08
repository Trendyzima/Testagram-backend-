package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) nativeCommunicationCapability(w http.ResponseWriter, r *http.Request, requestID string, body capabilityRequest) bool {
	name := strings.TrimSpace(body.Capability)
	switch name {
	case "testagram.conversations.list", "testagram.conversations.create",
		"testagram.messages.list", "testagram.messages.send_encrypted",
		"testagram.messages.edit_encrypted", "testagram.messages.delete",
		"testagram.messages.mark_read", "testagram.messages.react",
		"testagram.messages.attach", "testagram.calls.create",
		"testagram.calls.join", "testagram.calls.end":
	default:
		return false
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNICATION_DATABASE_UNAVAILABLE", "Communication service is unavailable", requestID))
		return true
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil || !socialUUIDPattern.MatchString(userID) {
		if err == nil { status = http.StatusUnauthorized }
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 { code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable" }
		writeJSON(w, status, errorBody(code, message, requestID))
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var accountStatus string
	if err := s.db.QueryRow(ctx, "SELECT account_status FROM testagram_local.accounts WHERE user_id=$1", strings.ToLower(userID)).Scan(&accountStatus); err != nil || accountStatus != "active" {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot use communications", requestID))
		return true
	}
	in := body.Input
	str := func(key string) string { value, _ := in[key].(string); return strings.TrimSpace(value) }
	intVal := func(key string, fallback, min, max int) int {
		value, ok := in[key]
		if !ok { return fallback }
		switch v := value.(type) {
		case float64: n:=int(v); if float64(n)==v && n>=min && n<=max { return n }
		case string: n,e:=strconv.Atoi(strings.TrimSpace(v)); if e==nil && n>=min && n<=max { return n }
		}
		return -1
	}
	fail := func(code, message string, httpStatus int) { writeJSON(w,httpStatus,errorBody(code,message,requestID)) }
	switch name {
	case "testagram.conversations.list":
		limit:=intVal("limit",50,1,100); if limit<1 { fail("INVALID_LIMIT","Limit must be between 1 and 100",http.StatusBadRequest); return true }
		rows,e:=s.db.Query(ctx,`SELECT c.id::text,c.kind,c.title,c.created_at,c.updated_at,
			COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.user_id,'username',a.username,'display_name',a.display_name,'avatar_url',a.avatar_key) ORDER BY a.username)
			 FROM testagram_local.conversation_members cm JOIN testagram_local.accounts a ON a.user_id=cm.user_id
			 WHERE cm.conversation_id=c.id AND cm.left_at IS NULL),'[]'::jsonb),
			(SELECT count(*) FROM testagram_local.messages m WHERE m.conversation_id=c.id AND m.sender_id<>$1 AND m.deleted_at IS NULL AND m.created_at>COALESCE(me.last_read_at,'epoch'::timestamptz)),
			(SELECT jsonb_build_object('id',m.id,'sender_id',m.sender_id,'created_at',m.created_at,'ciphertext',m.ciphertext,'media_type',m.media_type)
			 FROM testagram_local.messages m WHERE m.conversation_id=c.id AND m.deleted_at IS NULL ORDER BY m.created_at DESC,m.id DESC LIMIT 1)
			FROM testagram_local.conversations c JOIN testagram_local.conversation_members me ON me.conversation_id=c.id
			WHERE me.user_id=$1 AND me.left_at IS NULL AND me.archived_at IS NULL
			ORDER BY c.updated_at DESC,c.id DESC LIMIT $2`,userID,limit)
		if e!=nil { fail("CONVERSATIONS_LIST_FAILED","Could not load conversations",http.StatusServiceUnavailable); return true }
		defer rows.Close(); items:=make([]map[string]any,0)
		for rows.Next(){ var id,kind,title string;var created,updated time.Time;var members,latest []byte;var unread int64
			if rows.Scan(&id,&kind,&title,&created,&updated,&members,&unread,&latest)!=nil { fail("CONVERSATIONS_LIST_FAILED","Could not read conversations",http.StatusServiceUnavailable);return true }
			var m any;var l any;_ = json.Unmarshal(members,&m);if len(latest)>0{_ = json.Unmarshal(latest,&l)}
			items=append(items,map[string]any{"id":id,"kind":kind,"title":title,"created_at":created,"updated_at":updated,"members":m,"unread_count":unread,"latest_message":l})
		}
		if rows.Err()!=nil { fail("CONVERSATIONS_LIST_FAILED","Could not read conversations",http.StatusServiceUnavailable);return true }
		writeJSON(w,http.StatusOK,map[string]any{"items":items,"next_cursor":nil});return true
	case "testagram.conversations.create":
		raw,ok:=in["member_ids"].([]any);if !ok||len(raw)<1||len(raw)>49 { fail("INVALID_MEMBERS","Provide between 1 and 49 conversation members",http.StatusBadRequest);return true }
		members:=[]string{userID};seen:=map[string]bool{userID:true}
		for _,v:=range raw { id,ok:=v.(string);if !ok||!socialUUIDPattern.MatchString(id)||seen[strings.ToLower(id)] { fail("INVALID_MEMBERS","Conversation member IDs must be unique valid account IDs",http.StatusBadRequest);return true };id=strings.ToLower(id);seen[id]=true;members=append(members,id) }
		tx,e:=s.db.Begin(ctx);if e!=nil { fail("CONVERSATION_CREATE_FAILED","Could not create conversation",http.StatusServiceUnavailable);return true };defer func(){_ = tx.Rollback(context.Background())}()
		for _,member:=range members { var active bool;if tx.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$1 AND account_status='active')",member).Scan(&active)!=nil||!active { fail("MEMBER_UNAVAILABLE","One or more conversation members are unavailable",http.StatusBadRequest);return true } }
		kind:="group";if len(members)==2{kind="direct"}
		var conversationID string
		if kind=="direct" { _=tx.QueryRow(ctx,`SELECT c.id::text FROM testagram_local.conversations c
			JOIN testagram_local.conversation_members a ON a.conversation_id=c.id AND a.user_id=$1 AND a.left_at IS NULL
			JOIN testagram_local.conversation_members b ON b.conversation_id=c.id AND b.user_id=$2 AND b.left_at IS NULL
			WHERE c.kind='direct' AND (SELECT count(*) FROM testagram_local.conversation_members x WHERE x.conversation_id=c.id AND x.left_at IS NULL)=2 LIMIT 1`,members[0],members[1]).Scan(&conversationID) }
		if conversationID=="" { title:=str("title");if len(title)>120{fail("INVALID_TITLE","Conversation title is too long",http.StatusBadRequest);return true}
			e=tx.QueryRow(ctx,"INSERT INTO testagram_local.conversations(kind,created_by,title) VALUES($1,$2,$3) RETURNING id::text",kind,userID,title).Scan(&conversationID)
			if e==nil { for i,member:=range members { role:="member";if i==0{role="owner"};_,e=tx.Exec(ctx,"INSERT INTO testagram_local.conversation_members(conversation_id,user_id,role) VALUES($1,$2,$3)",conversationID,member,role);if e!=nil{break} } }
		}
		if e!=nil||tx.Commit(ctx)!=nil { fail("CONVERSATION_CREATE_FAILED","Could not safely create conversation",http.StatusServiceUnavailable);return true }
		writeJSON(w,http.StatusCreated,map[string]any{"conversation_id":conversationID});return true
	case "testagram.messages.list":
		conversationID:=str("conversation_id");if !socialUUIDPattern.MatchString(conversationID){fail("INVALID_CONVERSATION","A valid conversation ID is required",http.StatusBadRequest);return true}
		limit:=intVal("limit",50,1,100);if limit<1{fail("INVALID_LIMIT","Limit must be between 1 and 100",http.StatusBadRequest);return true}
		cursor:=str("cursor")
		rows,e:=s.db.Query(ctx,`SELECT m.id::text,m.conversation_id::text,m.sender_id::text,m.body,m.created_at,m.edited_at,m.deleted_at,m.reply_to_message_id::text,m.shared_post_id::text,m.client_message_id,m.delivered_at,m.read_at,m.e2ee_enabled,m.e2ee_version,m.e2ee_epoch,m.ciphertext,m.nonce,m.aad,m.attachment_metadata,
			jsonb_build_object('id',a.user_id,'username',a.username,'display_name',a.display_name,'avatar_url',a.avatar_key)
			FROM testagram_local.messages m JOIN testagram_local.conversation_members cm ON cm.conversation_id=m.conversation_id AND cm.user_id=$1 AND cm.left_at IS NULL
			JOIN testagram_local.accounts a ON a.user_id=m.sender_id
			WHERE m.conversation_id=$2 AND m.deleted_at IS NULL AND ($3='' OR m.created_at<$3::timestamptz)
			ORDER BY m.created_at DESC,m.id DESC LIMIT $4`,userID,conversationID,cursor,limit)
		if e!=nil{fail("MESSAGES_LIST_FAILED","Could not load conversation messages",http.StatusServiceUnavailable);return true}
		defer rows.Close();items:=make([]map[string]any,0)
		for rows.Next(){var id,cid,sender,body,version,ciphertext,nonce,aad string;var created time.Time;var edited,deleted,delivered,read *time.Time;var reply,shared,client *string;var e2ee bool;var epoch *int64;var attachments,senderJSON []byte
			if rows.Scan(&id,&cid,&sender,&body,&created,&edited,&deleted,&reply,&shared,&client,&delivered,&read,&e2ee,&version,&epoch,&ciphertext,&nonce,&aad,&attachments,&senderJSON)!=nil{fail("MESSAGES_LIST_FAILED","Could not read conversation messages",http.StatusServiceUnavailable);return true}
			var attachmentValue any;var senderValue any;_ = json.Unmarshal(attachments,&attachmentValue);_ = json.Unmarshal(senderJSON,&senderValue)
			items=append(items,map[string]any{"id":id,"conversation_id":cid,"sender_id":sender,"body":body,"created_at":created,"edited_at":edited,"deleted_at":deleted,"reply_to_message_id":reply,"shared_post_id":shared,"client_message_id":client,"delivered_at":delivered,"read_at":read,"e2ee_enabled":e2ee,"e2ee_version":version,"e2ee_epoch":epoch,"ciphertext":ciphertext,"nonce":nonce,"aad":aad,"attachment_metadata":attachmentValue,"sender":senderValue})
		}
		if rows.Err()!=nil{fail("MESSAGES_LIST_FAILED","Could not read conversation messages",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusOK,map[string]any{"items":items,"next_cursor":nil});return true
	case "testagram.messages.send_encrypted":
		conversationID:=str("conversation_id");ciphertext:=str("ciphertext");nonce:=str("nonce");clientID:=str("client_message_id");epoch:=intVal("e2ee_epoch",1,1,1<<30)
		if !socialUUIDPattern.MatchString(conversationID)||ciphertext==""||len(ciphertext)>32768||nonce==""||len(nonce)>512||epoch<1||clientID==""||len(clientID)>128{fail("INVALID_MESSAGE","Encrypted message fields are invalid",http.StatusBadRequest);return true}
		var id string;e:=s.db.QueryRow(ctx,`INSERT INTO testagram_local.messages(conversation_id,sender_id,body,client_message_id,reply_to_message_id,shared_post_id,ciphertext,nonce,aad,e2ee_enabled,e2ee_version,e2ee_epoch)
			SELECT $1,$2,'',$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,$8,TRUE,'v1',$9
			WHERE EXISTS(SELECT 1 FROM testagram_local.conversation_members WHERE conversation_id=$1 AND user_id=$2 AND left_at IS NULL)
			ON CONFLICT(sender_id,client_message_id) DO UPDATE SET client_message_id=EXCLUDED.client_message_id RETURNING id::text`,
			conversationID,userID,clientID,str("reply_to_message_id"),str("shared_post_id"),ciphertext,nonce,str("aad"),epoch).Scan(&id)
		if errors.Is(e,pgx.ErrNoRows){fail("CONVERSATION_ACCESS_DENIED","Conversation membership is required",http.StatusForbidden);return true}
		if e!=nil{fail("MESSAGE_SEND_FAILED","Could not save encrypted message",http.StatusServiceUnavailable);return true}
		_,_=s.db.Exec(ctx,"UPDATE testagram_local.conversations SET updated_at=now() WHERE id=$1",conversationID)
		_,_=s.db.Exec(ctx,`INSERT INTO testagram_local.communication_outbox(event_key,event_type,conversation_id,message_id,payload)
			VALUES($1,'message.created',$2,$3,jsonb_build_object('message_id',$3,'conversation_id',$2,'sender_id',$4,'encrypted',true))
			ON CONFLICT(event_key) DO NOTHING`,"message:"+id,conversationID,id,userID)
		writeJSON(w,http.StatusCreated,map[string]any{"message_id":id,"encrypted":true});return true
	case "testagram.messages.edit_encrypted":
		id:=str("message_id");ciphertext:=str("ciphertext");nonce:=str("nonce");epoch:=intVal("e2ee_epoch",1,1,1<<30)
		if !socialUUIDPattern.MatchString(id)||ciphertext==""||nonce==""||epoch<1{fail("INVALID_MESSAGE","Encrypted message fields are invalid",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,`UPDATE testagram_local.messages SET ciphertext=$3,nonce=$4,aad=$5,e2ee_epoch=$6,e2ee_enabled=TRUE,e2ee_version='v1',edited_at=now()
			WHERE id=$1 AND sender_id=$2 AND deleted_at IS NULL`,id,userID,ciphertext,nonce,str("aad"),epoch)
		if e!=nil{fail("MESSAGE_EDIT_FAILED","Could not update encrypted message",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("MESSAGE_NOT_FOUND","Message was not found or cannot be edited",http.StatusNotFound);return true}
		writeJSON(w,http.StatusOK,map[string]any{"message_id":id,"edited":true});return true
	case "testagram.messages.delete":
		id:=str("message_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_MESSAGE","A valid message ID is required",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,"UPDATE testagram_local.messages SET deleted_at=now(),body='',ciphertext=NULL,nonce=NULL,aad=NULL WHERE id=$1 AND sender_id=$2 AND deleted_at IS NULL",id,userID)
		if e!=nil{fail("MESSAGE_DELETE_FAILED","Could not delete message",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("MESSAGE_NOT_FOUND","Message was not found or cannot be deleted",http.StatusNotFound);return true}
		writeJSON(w,http.StatusOK,map[string]any{"message_id":id,"deleted":true});return true
	case "testagram.messages.mark_read":
		id:=str("message_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_MESSAGE","A valid message ID is required",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,`UPDATE testagram_local.conversation_members cm SET last_read_at=GREATEST(COALESCE(cm.last_read_at,'epoch'::timestamptz),m.created_at)
			FROM testagram_local.messages m WHERE m.id=$1 AND cm.conversation_id=m.conversation_id AND cm.user_id=$2 AND cm.left_at IS NULL`,id,userID)
		if e!=nil{fail("MESSAGE_READ_FAILED","Could not mark message read",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("MESSAGE_NOT_FOUND","Message was not found in an active conversation",http.StatusNotFound);return true}
		writeJSON(w,http.StatusOK,map[string]any{"message_id":id,"read":true});return true
	case "testagram.messages.react":
		id:=str("message_id");emoji:=str("reaction");if emoji==""{emoji="❤️"};if !socialUUIDPattern.MatchString(id)||len(emoji)>32{fail("INVALID_REACTION","Message or reaction is invalid",http.StatusBadRequest);return true}
		remove,_:=in["remove"].(bool)
		if remove{_,err=s.db.Exec(ctx,"DELETE FROM testagram_local.message_reactions WHERE message_id=$1 AND user_id=$2 AND emoji=$3 AND EXISTS(SELECT 1 FROM testagram_local.messages m JOIN testagram_local.conversation_members cm ON cm.conversation_id=m.conversation_id WHERE m.id=$1 AND cm.user_id=$2 AND cm.left_at IS NULL)",id,userID,emoji)}else{_,err=s.db.Exec(ctx,`INSERT INTO testagram_local.message_reactions(message_id,user_id,emoji)
			SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM testagram_local.messages m JOIN testagram_local.conversation_members cm ON cm.conversation_id=m.conversation_id WHERE m.id=$1 AND cm.user_id=$2 AND cm.left_at IS NULL)
			ON CONFLICT(message_id,user_id) DO UPDATE SET emoji=EXCLUDED.emoji`,id,userID,emoji)}
		if err!=nil{fail("MESSAGE_REACTION_FAILED","Could not update message reaction",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusOK,map[string]any{"message_id":id,"reaction":emoji,"removed":remove});return true
	case "testagram.messages.attach":
		id:=str("message_id");attachment,ok:=in["attachment_metadata"].(map[string]any);if !socialUUIDPattern.MatchString(id)||!ok{fail("INVALID_ATTACHMENT","Attachment metadata is invalid",http.StatusBadRequest);return true}
		raw,_:=json.Marshal([]any{attachment});if len(raw)>16384{fail("INVALID_ATTACHMENT","Attachment metadata is too large",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,`UPDATE testagram_local.messages m SET attachment_metadata=$3::jsonb
			WHERE m.id=$1 AND EXISTS(SELECT 1 FROM testagram_local.conversation_members cm WHERE cm.conversation_id=m.conversation_id AND cm.user_id=$2 AND cm.left_at IS NULL)`,id,userID,string(raw))
		if e!=nil{fail("MESSAGE_ATTACHMENT_FAILED","Could not attach media to message",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("MESSAGE_NOT_FOUND","Message was not found in an active conversation",http.StatusNotFound);return true}
		writeJSON(w,http.StatusOK,map[string]any{"attachment":attachment});return true
	case "testagram.calls.create":
		conversationID:=str("conversation_id");kind:=str("kind");if kind==""{kind="video"};if !socialUUIDPattern.MatchString(conversationID)||(kind!="voice"&&kind!="video"&&kind!="screen"){fail("INVALID_CALL","Call details are invalid",http.StatusBadRequest);return true}
		room:="testagram-"+strings.ReplaceAll(newUUID(),"-","")
		var id string;e:=s.db.QueryRow(ctx,`INSERT INTO testagram_local.call_sessions(conversation_id,created_by,room_name,kind)
			SELECT $1,$2,$3,$4 WHERE EXISTS(SELECT 1 FROM testagram_local.conversation_members WHERE conversation_id=$1 AND user_id=$2 AND left_at IS NULL) RETURNING id::text`,conversationID,userID,room,kind).Scan(&id)
		if errors.Is(e,pgx.ErrNoRows){fail("CONVERSATION_ACCESS_DENIED","Conversation membership is required",http.StatusForbidden);return true};if e!=nil{fail("CALL_CREATE_FAILED","Could not create call session",http.StatusServiceUnavailable);return true}
		_,_=s.db.Exec(ctx,"INSERT INTO testagram_local.call_participants(call_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING",id,userID)
		writeJSON(w,http.StatusCreated,map[string]any{"call_id":id,"provider":"testagram-p2p","room_name":room,"kind":kind,"status":"ringing","conversation_id":conversationID,"e2ee_enabled":true});return true
	case "testagram.calls.join":
		id:=str("call_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_CALL","A valid call ID is required",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,`UPDATE testagram_local.call_sessions c SET status=CASE WHEN c.status='ringing' THEN 'active' ELSE c.status END,started_at=COALESCE(c.started_at,now())
			WHERE c.id=$1 AND c.status IN ('ringing','active') AND EXISTS(SELECT 1 FROM testagram_local.conversation_members cm WHERE cm.conversation_id=c.conversation_id AND cm.user_id=$2 AND cm.left_at IS NULL)`,id,userID)
		if e!=nil{fail("CALL_JOIN_FAILED","Could not join call",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("CALL_NOT_AVAILABLE","Call is unavailable or access is denied",http.StatusNotFound);return true}
		_,_=s.db.Exec(ctx,"INSERT INTO testagram_local.call_participants(call_id,user_id) VALUES($1,$2) ON CONFLICT(call_id,user_id) DO UPDATE SET joined_at=now(),left_at=NULL",id,userID)
		writeJSON(w,http.StatusOK,map[string]any{"call_id":id,"joined":true});return true
	case "testagram.calls.end":
		id:=str("call_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_CALL","A valid call ID is required",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,`UPDATE testagram_local.call_sessions c SET status='ended',ended_at=COALESCE(ended_at,now())
			WHERE c.id=$1 AND (c.created_by=$2 OR EXISTS(SELECT 1 FROM testagram_local.call_participants cp WHERE cp.call_id=c.id AND cp.user_id=$2))`,id,userID)
		if e!=nil{fail("CALL_END_FAILED","Could not end call",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("CALL_NOT_FOUND","Call was not found or access is denied",http.StatusNotFound);return true}
		_,_=s.db.Exec(ctx,"UPDATE testagram_local.call_participants SET left_at=COALESCE(left_at,now()) WHERE call_id=$1 AND user_id=$2",id,userID)
		writeJSON(w,http.StatusOK,map[string]any{"call_id":id,"ended":true});return true
	}
	return true
}
