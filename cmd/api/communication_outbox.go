package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/realtime"
)

type realtimePublishEvent struct {
	EventID string `json:"event_id"`
	ConversationID string `json:"conversation_id"`
	EventType string `json:"event_type"`
	OccurredAt time.Time `json:"occurred_at"`
	Payload json.RawMessage `json:"payload"`
}

type communicationOutboxRow struct {
	ID int64
	EventKey string
	EventType string
	ConversationID string
	Payload []byte
	CreatedAt time.Time
	Attempts int
}

func (s *service) runCommunicationOutbox(ctx context.Context) {
	base:=strings.TrimRight(strings.TrimSpace(os.Getenv("REALTIME_PUBLISH_URL")),"/")
	secret:=strings.TrimSpace(os.Getenv("REALTIME_PUBLISH_SECRET"))
	if base==""||secret==""||len([]byte(secret))<32 {
		log.Printf("communication realtime outbox worker disabled: publish URL or secret is not configured")
		return
	}
	if !strings.HasSuffix(base,"/internal/v1/realtime/publish"){base+="/internal/v1/realtime/publish"}
	client:=&http.Client{Timeout:5*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
	ticker:=time.NewTicker(2*time.Second);defer ticker.Stop()
	workerID:="backend-"+strings.TrimPrefix(newCallRoom(),"testagram-")
	for {
		select {
		case <-ctx.Done(): return
		case <-ticker.C:
			batchCtx,cancel:=context.WithTimeout(ctx,8*time.Second)
			rows,err:=s.claimCommunicationOutbox(batchCtx,workerID,25)
			if err!=nil { log.Printf("communication outbox claim failed");cancel();continue }
			for _,row:=range rows {
				if err:=s.publishCommunicationEvent(batchCtx,client,base,secret,row);err!=nil {
					s.failCommunicationOutbox(batchCtx,workerID,row.ID,row.Attempts)
				}else{
					s.completeCommunicationOutbox(batchCtx,workerID,row.ID)
				}
			}
			cancel()
		}
	}
}

func (s *service) claimCommunicationOutbox(ctx context.Context,workerID string,limit int)([]communicationOutboxRow,error){
	rows,err:=s.db.Query(ctx,`WITH due AS (
		SELECT id FROM testagram_local.communication_outbox
		WHERE status IN ('pending','failed') AND next_attempt_at<=now()
		  AND (lease_until IS NULL OR lease_until<now())
		ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT $2
	)
	UPDATE testagram_local.communication_outbox o
	SET status='processing',claimed_by=$1,lease_until=now()+interval '30 seconds',attempts=o.attempts+1
	FROM due WHERE o.id=due.id
	RETURNING o.id,o.event_key,o.event_type,o.conversation_id::text,o.payload::text,o.created_at,o.attempts`,workerID,limit)
	if err!=nil{return nil,err}
	defer rows.Close();result:=make([]communicationOutboxRow,0)
	for rows.Next(){var row communicationOutboxRow;var payload string
		if err:=rows.Scan(&row.ID,&row.EventKey,&row.EventType,&row.ConversationID,&payload,&row.CreatedAt,&row.Attempts);err!=nil{return nil,err}
		row.Payload=[]byte(payload);result=append(result,row)
	}
	if err:=rows.Err();err!=nil{return nil,err}
	return result,nil
}

func (s *service) publishCommunicationEvent(ctx context.Context,client *http.Client,endpoint,secret string,row communicationOutboxRow)error{
	if row.EventType!="message.created"&&row.EventType!="message.updated"&&row.EventType!="message.deleted"&&row.EventType!="conversation.updated"{return io.ErrUnexpectedEOF}
	if !json.Valid(row.Payload){return io.ErrUnexpectedEOF}
	body,err:=json.Marshal(realtimePublishEvent{EventID:row.EventKey,ConversationID:row.ConversationID,EventType:row.EventType,OccurredAt:row.CreatedAt.UTC(),Payload:json.RawMessage(row.Payload)})
	if err!=nil{return err}
	signature,err:=realtime.SignPublish(secret,body);if err!=nil{return err}
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,endpoint,bytes.NewReader(body));if err!=nil{return err}
	req.Header.Set("Content-Type","application/json");req.Header.Set("x-testagram-signature",signature)
	resp,err:=client.Do(req);if err!=nil{return err};defer resp.Body.Close()
	_,_=io.Copy(io.Discard,io.LimitReader(resp.Body,4096))
	if resp.StatusCode<200||resp.StatusCode>=300{return io.ErrUnexpectedEOF}
	return nil
}

func (s *service) completeCommunicationOutbox(ctx context.Context,workerID string,id int64){
	_,err:=s.db.Exec(ctx,`UPDATE testagram_local.communication_outbox
		SET status='sent',sent_at=now(),claimed_by=NULL,lease_until=NULL,last_error=''
		WHERE id=$1 AND status='processing' AND claimed_by=$2`,id,workerID)
	if err!=nil{log.Printf("communication outbox acknowledgement failed")}
}

func (s *service) failCommunicationOutbox(ctx context.Context,workerID string,id int64,attempts int){
	_,err:=s.db.Exec(ctx,`UPDATE testagram_local.communication_outbox
		SET status=CASE WHEN attempts>=10 THEN 'dead' ELSE 'failed' END,
			next_attempt_at=now()+make_interval(secs=>LEAST(300,power(2,LEAST(attempts,8))::integer)),
			claimed_by=NULL,lease_until=NULL,last_error='realtime publish failed'
		WHERE id=$1 AND status='processing' AND claimed_by=$2`,id,workerID)
	if err!=nil{log.Printf("communication outbox retry scheduling failed")}
}
