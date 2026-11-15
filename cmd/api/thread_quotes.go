package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type threadQuoteInput struct {
	Body string `json:"body"`
	Visibility string `json:"visibility"`
}

func threadQuoteHashes(idempotencyKey,sourceID,body,visibility string) (string,string) {
	keyDigest:=sha256.Sum256([]byte(idempotencyKey))
	requestDigest:=sha256.Sum256([]byte(sourceID+"\x00"+body+"\x00"+visibility))
	return hex.EncodeToString(keyDigest[:]),hex.EncodeToString(requestDigest[:])
}

func (s *service) threadQuote(w http.ResponseWriter,r *http.Request) {
	userID,rid,ok:=s.socialAuth(w,r)
	if !ok { return }
	if !s.socialWriteAllowed(w,r,userID,rid,"thread-quote",10) { return }
	sourceID:=strings.ToLower(strings.TrimSpace(r.PathValue("thread_id")))
	if !socialUUIDPattern.MatchString(sourceID) {
		writeJSON(w,400,errorBody("INVALID_THREAD_ID","A valid source thread ID is required",rid));return
	}
	var in threadQuoteInput
	if err:=decodeMediaJSON(w,r,&in);err!=nil {
		writeJSON(w,400,errorBody("INVALID_QUOTE","Quote payload is invalid",rid));return
	}
	in.Body=strings.TrimSpace(in.Body)
	if utf8.RuneCountInString(in.Body)>500 {
		writeJSON(w,400,errorBody("QUOTE_TOO_LONG","Quote text must be 500 characters or fewer",rid));return
	}
	if in.Visibility=="" { in.Visibility="public" }
	if in.Visibility!="public" && in.Visibility!="unlisted" && in.Visibility!="private" {
		writeJSON(w,400,errorBody("INVALID_VISIBILITY","Quote visibility is invalid",rid));return
	}
	idempotencyKey:=strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey)<16 || len(idempotencyKey)>128 || strings.ContainsAny(idempotencyKey," \t\r\n") {
		writeJSON(w,400,errorBody("IDEMPOTENCY_KEY_REQUIRED","A 16-128 character Idempotency-Key is required",rid));return
	}
	keyDigest:=sha256.Sum256([]byte(idempotencyKey))
	keyHash:=hex.EncodeToString(keyDigest[:])
	requestDigest:=sha256.Sum256([]byte(sourceID+"\x00"+in.Body+"\x00"+in.Visibility))
	requestHash:=hex.EncodeToString(requestDigest[:])

	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second);defer cancel()
	tx,err:=s.db.Begin(ctx)
	if err!=nil { writeJSON(w,503,errorBody("QUOTE_CREATE_FAILED","Quote could not be started",rid));return }
	defer tx.Rollback(context.Background())
	if _,err=tx.Exec(ctx,"SELECT pg_advisory_xact_lock(hashtextextended($1,0))",userID+":"+keyHash);err!=nil {
		writeJSON(w,503,errorBody("QUOTE_CREATE_FAILED","Quote retry could not be locked",rid));return
	}
	var existingID,existingHash string
	err=tx.QueryRow(ctx,`SELECT quote_thread_id::text,request_hash FROM testagram_local.thread_quotes
		WHERE owner_id=$1 AND idempotency_key_hash=$2`,userID,keyHash).Scan(&existingID,&existingHash)
	if err==nil {
		if existingHash!=requestHash { writeJSON(w,409,errorBody("IDEMPOTENCY_CONFLICT","This Idempotency-Key was already used for different quote content",rid));return }
		if err=tx.Commit(ctx);err!=nil { writeJSON(w,503,errorBody("QUOTE_CREATE_FAILED","Quote retry could not be confirmed",rid));return }
		writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"id":existingID,"quote_of_thread_id":sourceID,"idempotent_replay":true},"error":nil,"request_id":rid})
		return
	}
	if !errors.Is(err,pgx.ErrNoRows) { writeJSON(w,503,errorBody("QUOTE_LOOKUP_FAILED","Quote retry status is unavailable",rid));return }

	var sourceOwner string
	err=tx.QueryRow(ctx,`SELECT t.owner_id::text FROM testagram_local.threads t
		JOIN testagram_local.accounts a ON a.user_id=t.owner_id
		WHERE t.id=$1 AND t.visibility='public' AND t.deleted_at IS NULL AND a.account_status='active'
		FOR SHARE OF t`,sourceID).Scan(&sourceOwner)
	if errors.Is(err,pgx.ErrNoRows) {
		writeJSON(w,404,errorBody("SOURCE_THREAD_NOT_FOUND","Only an active public thread can be quoted",rid));return
	}
	if err!=nil { writeJSON(w,503,errorBody("SOURCE_THREAD_LOOKUP_FAILED","Source thread could not be verified",rid));return }

	var quoteID string
	var createdAt time.Time
	err=tx.QueryRow(ctx,`INSERT INTO testagram_local.threads(owner_id,body,visibility)
		VALUES($1,$2,$3) RETURNING id::text,created_at`,userID,in.Body,in.Visibility).Scan(&quoteID,&createdAt)
	if err!=nil { writeJSON(w,503,errorBody("QUOTE_CREATE_FAILED","Quote thread could not be created",rid));return }
	_,err=tx.Exec(ctx,`INSERT INTO testagram_local.thread_quotes
		(quote_thread_id,source_thread_id,owner_id,idempotency_key_hash,request_hash)
		VALUES($1,$2,$3,$4,$5)`,quoteID,sourceID,userID,keyHash,requestHash)
	if err!=nil { writeJSON(w,503,errorBody("QUOTE_CREATE_FAILED","Quote relationship could not be saved",rid));return }
	if err=tx.Commit(ctx);err!=nil { writeJSON(w,503,errorBody("QUOTE_CREATE_FAILED","Quote could not be committed",rid));return }
	writeJSON(w,201,map[string]any{"ok":true,"data":map[string]any{
		"id":quoteID,"owner_id":userID,"body":in.Body,"visibility":in.Visibility,
		"quote_of_thread_id":sourceID,"created_at":createdAt.UTC(),"idempotent_replay":false,
	},"error":nil,"request_id":rid})
}

func (s *service) threadQuotesList(w http.ResponseWriter,r *http.Request) {
	rid:=requestID(r)
	if !s.cors(w,r) { writeJSON(w,403,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",rid));return }
	if s.db==nil { writeJSON(w,503,errorBody("SOCIAL_DATABASE_UNAVAILABLE","Social data service is unavailable",rid));return }
	sourceID:=strings.ToLower(strings.TrimSpace(r.PathValue("thread_id")))
	if !socialUUIDPattern.MatchString(sourceID) { writeJSON(w,400,errorBody("INVALID_THREAD_ID","A valid source thread ID is required",rid));return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second);defer cancel()
	var sourceExists bool
	if err:=s.db.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_local.threads
		WHERE id=$1 AND visibility='public' AND deleted_at IS NULL)`,sourceID).Scan(&sourceExists);err!=nil {
		writeJSON(w,503,errorBody("QUOTE_LIST_FAILED","Source thread could not be checked",rid));return
	}
	if !sourceExists { writeJSON(w,404,errorBody("THREAD_NOT_FOUND","Source thread was not found",rid));return }
	rows,err:=s.db.Query(ctx,`SELECT t.id::text,t.owner_id::text,t.body,t.visibility,t.created_at,
		a.username,a.display_name,a.avatar_key,q.source_thread_id::text
		FROM testagram_local.thread_quotes q
		JOIN testagram_local.threads t ON t.id=q.quote_thread_id
		JOIN testagram_local.accounts a ON a.user_id=t.owner_id
		WHERE q.source_thread_id=$1 AND t.deleted_at IS NULL AND t.visibility='public' AND a.account_status='active'
		ORDER BY q.created_at DESC LIMIT 30`,sourceID)
	if err!=nil { writeJSON(w,503,errorBody("QUOTE_LIST_FAILED","Quotes could not be loaded",rid));return }
	defer rows.Close()
	items:=make([]map[string]any,0)
	for rows.Next() {
		var id,owner,body,visibility,username,displayName,avatarKey,quoteOf string
		var created time.Time
		if err:=rows.Scan(&id,&owner,&body,&visibility,&created,&username,&displayName,&avatarKey,&quoteOf);err!=nil {
			writeJSON(w,503,errorBody("QUOTE_LIST_FAILED","Quote rows could not be decoded",rid));return
		}
		avatarURL:="";if avatarKey!="" { avatarURL=publicMediaURL(avatarKey) }
		items=append(items,map[string]any{"id":id,"owner_id":owner,"body":body,"visibility":visibility,
			"created_at":created,"quote_of_thread_id":quoteOf,
			"profiles":map[string]any{"id":owner,"username":username,"display_name":displayName,"avatar_url":avatarURL,"verified":false}})
	}
	if err:=rows.Err();err!=nil { writeJSON(w,503,errorBody("QUOTE_LIST_FAILED","Quote list could not be read",rid));return }
	writeJSON(w,200,map[string]any{"ok":true,"data":map[string]any{"items":items,"count":len(items)},"error":nil,"request_id":rid})
}
