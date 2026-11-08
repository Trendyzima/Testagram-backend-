package main

import (
	"context"
	"errors"
	"crypto/sha256"
	"encoding/hex"
		"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

func moneyAmount(value any) (string, float64, bool) {
	var raw string
	switch v:=value.(type) {
	case string: raw=strings.TrimSpace(v)
	case float64:
		if math.IsNaN(v)||math.IsInf(v,0)||math.Abs(v*100-math.Round(v*100))>0.000001{return "",0,false}
		raw=strconv.FormatFloat(v,'f',2,64)
	default:return "",0,false
	}
	if len(raw)==0||len(raw)>24||strings.ContainsAny(raw,"eE,+- ") { return "",0,false }
	n,err:=strconv.ParseFloat(raw,64)
	if err!=nil||n<=0||n>10000||math.Abs(n*100-math.Round(n*100))>0.000001{return "",0,false}
	return strconv.FormatFloat(n,'f',2,64),n,true
}

var errWalletIdempotencyConflict = errors.New("wallet idempotency key payload conflict")

func walletTransferTx(ctx context.Context, tx pgx.Tx, sender, recipient, amount, kind, note, key string) (string,bool,error) {
	_,err:=tx.Exec(ctx,"INSERT INTO testagram_local.wallets(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING",sender)
	if err!=nil{return "",false,err}
	var senderCurrency string
	if err=tx.QueryRow(ctx,"SELECT currency FROM testagram_local.wallets WHERE user_id=$1",sender).Scan(&senderCurrency);err!=nil{return "",false,err}
	senderCurrency=strings.TrimSpace(senderCurrency)
	if senderCurrency!="KES"&&senderCurrency!="USD"{return "",false,pgx.ErrNoRows}
	_,err=tx.Exec(ctx,"INSERT INTO testagram_local.wallets(user_id,currency) VALUES($1,$2) ON CONFLICT(user_id) DO NOTHING",recipient,senderCurrency)
	if err!=nil{return "",false,err}
	rows,err:=tx.Query(ctx,`SELECT w.user_id::text,w.balance::text,w.currency,w.status
		FROM testagram_local.wallets w WHERE w.user_id=$1 OR w.user_id=$2 ORDER BY w.user_id FOR UPDATE`,sender,recipient)
	if err!=nil{return "",false,err}
	balances:=map[string]float64{};currencies:=map[string]string{};statuses:=map[string]string{}
	for rows.Next(){var id,balance,currency,status string;if err=rows.Scan(&id,&balance,&currency,&status);err!=nil{rows.Close();return "",false,err};v,e:=strconv.ParseFloat(balance,64);if e!=nil{rows.Close();return "",false,e};balances[id]=v;currencies[id]=strings.TrimSpace(currency);statuses[id]=status}
	if err=rows.Err();err!=nil{rows.Close();return "",false,err};rows.Close()
	if len(balances)!=2||statuses[sender]!="active"||statuses[recipient]!="active"||currencies[sender]!=currencies[recipient]||currencies[sender]!=senderCurrency{return "",false,pgx.ErrNoRows}
	sum:=sha256.Sum256([]byte(strings.Join([]string{recipient,amount,senderCurrency,kind,note},"\x00")))
	requestHash:=hex.EncodeToString(sum[:])
	// Recheck after acquiring both wallet row locks so concurrent retries cannot
	// double-debit or create duplicate ledger entries.
	var existingID,existingHash string
	err=tx.QueryRow(ctx,"SELECT id::text,request_hash FROM testagram_local.wallet_transfers WHERE sender_id=$1 AND idempotency_key=$2",sender,key).Scan(&existingID,&existingHash)
	if err==nil{if existingHash!=requestHash{return "",false,errWalletIdempotencyConflict};return existingID,true,nil}
	if err!=pgx.ErrNoRows{return "",false,err}
	amountValue,_:=strconv.ParseFloat(amount,64)
	if balances[sender]+0.000001<amountValue{return "",false,pgx.ErrNoRows}
	var transferID string
	err=tx.QueryRow(ctx,`INSERT INTO testagram_local.wallet_transfers(sender_id,recipient_id,amount,currency,kind,note,idempotency_key,request_hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`,sender,recipient,amount,senderCurrency,kind,note,key,requestHash).Scan(&transferID)
	if err!=nil{return "",false,err}
	if _,err=tx.Exec(ctx,"UPDATE testagram_local.wallets SET balance=balance-$2,updated_at=now() WHERE user_id=$1",sender,amount);err!=nil{return "",false,err}
	if _,err=tx.Exec(ctx,"UPDATE testagram_local.wallets SET balance=balance+$2,updated_at=now() WHERE user_id=$1",recipient,amount);err!=nil{return "",false,err}
	if _,err=tx.Exec(ctx,`INSERT INTO testagram_local.wallet_ledger_entries(transfer_id,user_id,direction,amount,currency)
		VALUES($1,$2,'debit',$3,$5),($1,$4,'credit',$3,$5)`,transferID,sender,amount,recipient,senderCurrency);err!=nil{return "",false,err}
	outKind,inKind:="transfer_out","transfer_in"
	if kind=="tip"{outKind,inKind="tip_out","tip_in"}else if kind=="superchat"{outKind,inKind="superchat_out","superchat_in"}
	if _,err=tx.Exec(ctx,`INSERT INTO testagram_local.wallet_transactions(user_id,transfer_id,kind,amount,currency)
		VALUES($1,$2,$3,$4,$7),($5,$2,$6,$4,$7)`,sender,transferID,outKind,amount,recipient,inKind,senderCurrency);err!=nil{return "",false,err}
	if _,err=tx.Exec(ctx,`INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,payload,event_key)
		VALUES($1,$2,'wallet.transfer.received',jsonb_build_object('transfer_id',$3,'amount',$4,'currency',$5,'kind',$6),'wallet-transfer:'||$3)
		ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`,recipient,sender,transferID,amount,senderCurrency,kind);err!=nil{return "",false,err}
	return transferID,false,nil
}

func (s *service) nativeWalletSpaceCapability(w http.ResponseWriter,r *http.Request,requestID string,body capabilityRequest) bool {
	name:=strings.TrimSpace(body.Capability)
	switch name {
	case "testagram.wallet.balance","testagram.wallet.transactions.list","testagram.wallet.transfer","testagram.wallet.tip",
		"testagram.audio_spaces.list_live","testagram.audio_spaces.pending_speaker_requests","testagram.audio_spaces.get","testagram.audio_spaces.my_status","testagram.audio_spaces.create","testagram.audio_spaces.join",
		"testagram.audio_spaces.leave","testagram.audio_spaces.request_speaker","testagram.audio_spaces.decide_speaker",
		"testagram.audio_spaces.end","testagram.audio_spaces.superchat":
	default:return false
	}
	if s.db==nil{writeJSON(w,http.StatusServiceUnavailable,errorBody("PRODUCT_DATABASE_UNAVAILABLE","Wallet and Spaces services are unavailable",requestID));return true}
	userID,status,err:=s.authenticatedMediaUser(r)
	if err!=nil||!socialUUIDPattern.MatchString(userID){if err==nil{status=http.StatusUnauthorized};writeJSON(w,status,errorBody("AUTH_REQUIRED","A valid authenticated session is required",requestID));return true}
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second);defer cancel()
	var active bool
	if err=s.db.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$1 AND account_status='active')",userID).Scan(&active);err!=nil||!active{writeJSON(w,http.StatusForbidden,errorBody("ACCOUNT_NOT_ACTIVE","This account cannot use wallets or Spaces",requestID));return true}
	in:=body.Input
	str:=func(key string)string{v,_:=in[key].(string);return strings.TrimSpace(v)}
	fail:=func(code,msg string,status int){writeJSON(w,status,errorBody(code,msg,requestID))}
	switch name {
	case "testagram.wallet.balance":
		requestedCurrency:=strings.ToUpper(str("currency"));if requestedCurrency==""{requestedCurrency="KES"};if requestedCurrency!="KES"&&requestedCurrency!="USD"{fail("INVALID_CURRENCY","Wallet currency must be KES or USD",http.StatusBadRequest);return true};_,err=s.db.Exec(ctx,"INSERT INTO testagram_local.wallets(user_id,currency) VALUES($1,$2) ON CONFLICT(user_id) DO NOTHING",userID,requestedCurrency)
		if err!=nil{fail("WALLET_READ_FAILED","Could not load wallet",http.StatusServiceUnavailable);return true}
		var balance,walletCurrency,state string
		err=s.db.QueryRow(ctx,"SELECT balance::text,currency,status FROM testagram_local.wallets WHERE user_id=$1",userID).Scan(&balance,&walletCurrency,&state)
		if err!=nil{fail("WALLET_READ_FAILED","Could not load wallet",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusOK,map[string]any{"id":userID,"user_id":userID,"balance":balance,"currency":strings.TrimSpace(walletCurrency),"status":state});return true
	case "testagram.wallet.transactions.list":
		limit:=50;if v,ok:=in["limit"].(float64);ok{limit=int(v)};if limit<1||limit>100{fail("INVALID_LIMIT","Limit must be between 1 and 100",http.StatusBadRequest);return true}
		rows,e:=s.db.Query(ctx,`SELECT t.id::text,t.kind,t.amount::text,t.currency,t.created_at,
			COALESCE(x.id::text,top.id::text,''),COALESCE(x.sender_id::text,''),COALESCE(x.recipient_id::text,''),COALESCE(x.note,top.receipt_number,'')
			FROM testagram_local.wallet_transactions t
			LEFT JOIN testagram_local.wallet_transfers x ON x.id=t.transfer_id
			LEFT JOIN testagram_local.wallet_topups top ON top.id=t.topup_id
			WHERE t.user_id=$1 ORDER BY t.created_at DESC,t.id DESC LIMIT $2`,userID,limit)
		if e!=nil{fail("WALLET_HISTORY_FAILED","Could not load wallet history",http.StatusServiceUnavailable);return true}
		defer rows.Close();items:=make([]map[string]any,0)
		for rows.Next(){var id,kind,amount,currency,transferID,sender,recipient,note string;var created time.Time;if rows.Scan(&id,&kind,&amount,&currency,&created,&transferID,&sender,&recipient,&note)!=nil{fail("WALLET_HISTORY_FAILED","Could not read wallet history",http.StatusServiceUnavailable);return true};items=append(items,map[string]any{"id":id,"kind":kind,"amount":amount,"currency":strings.TrimSpace(currency),"created_at":created,"transfer_id":transferID,"sender_id":sender,"recipient_id":recipient,"note":note})}
		if rows.Err()!=nil{fail("WALLET_HISTORY_FAILED","Could not read wallet history",http.StatusServiceUnavailable);return true};writeJSON(w,http.StatusOK,map[string]any{"items":items});return true
	case "testagram.wallet.transfer","testagram.wallet.tip":
		recipient:=str("recipient_id");amount,_,ok:=moneyAmount(in["amount"]);key:=str("idempotency_key");note:=str("note")
		if !socialUUIDPattern.MatchString(recipient)||recipient==userID||!ok||len(key)<8||len(key)>128||len(note)>280{fail("INVALID_TRANSFER","Recipient, amount, note or idempotency key is invalid",http.StatusBadRequest);return true}
		var recipientActive bool;if e:=s.db.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$1 AND account_status='active')",recipient).Scan(&recipientActive);e!=nil||!recipientActive{fail("RECIPIENT_UNAVAILABLE","Recipient account is unavailable",http.StatusBadRequest);return true}
		tx,e:=s.db.Begin(ctx);if e!=nil{fail("TRANSFER_FAILED","Could not begin wallet transfer",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		kind:="transfer";if name=="testagram.wallet.tip"{kind="tip"}
		id,duplicate,e:=walletTransferTx(ctx,tx,userID,recipient,amount,kind,note,key)
		if e==pgx.ErrNoRows{fail("INSUFFICIENT_FUNDS_OR_WALLET_UNAVAILABLE","Wallets are unavailable, frozen, or insufficiently funded",http.StatusConflict);return true}
		if e!=nil{if e==errWalletIdempotencyConflict{fail("IDEMPOTENCY_CONFLICT","Idempotency key was already used for a different transfer",http.StatusConflict)}else{fail("TRANSFER_FAILED","Wallet transfer could not be completed",http.StatusServiceUnavailable)};return true}
		if e=tx.Commit(ctx);e!=nil{fail("TRANSFER_FAILED","Wallet transfer could not be committed",http.StatusServiceUnavailable);return true}
		var walletCurrency string;_ = s.db.QueryRow(ctx,"SELECT currency FROM testagram_local.wallets WHERE user_id=$1",userID).Scan(&walletCurrency)
		writeJSON(w,http.StatusOK,map[string]any{"transfer_id":id,"status":"completed","duplicate":duplicate,"amount":amount,"currency":strings.TrimSpace(walletCurrency)});return true
	case "testagram.audio_spaces.list_live":
		rows,e:=s.db.Query(ctx,`SELECT s.id::text,s.host_id::text,s.title,s.description,s.listener_count,s.max_audience,s.started_at,a.username,a.display_name,a.avatar_key,
			EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=s.host_id AND f.status='accepted')
			FROM testagram_local.audio_spaces s JOIN testagram_local.accounts a ON a.user_id=s.host_id
			WHERE s.status='live' AND a.account_status='active'
			  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b WHERE
			    (b.blocker_id=$1 AND b.blocked_id=s.host_id) OR (b.blocker_id=s.host_id AND b.blocked_id=$1))
			ORDER BY s.listener_count DESC,s.started_at DESC LIMIT 50`,userID)
		if e!=nil{fail("SPACES_LIST_FAILED","Could not load live Spaces",http.StatusServiceUnavailable);return true}
		defer rows.Close();items:=make([]map[string]any,0);candidates:=make([]algorithmclient.Candidate,0)
		for rows.Next(){var id,host,title,description,username,display,avatar string;var count,max int;var started *time.Time;var following bool
			if rows.Scan(&id,&host,&title,&description,&count,&max,&started,&username,&display,&avatar,&following)!=nil{fail("SPACES_LIST_FAILED","Could not read live Spaces",http.StatusServiceUnavailable);return true}
			created:=time.Now().UTC();if started!=nil{created=*started}
			items=append(items,map[string]any{"id":id,"host_id":host,"title":title,"description":description,"listener_count":count,"max_audience":max,"started_at":started,"is_live":true,"host":map[string]any{"id":host,"username":username,"display_name":display,"avatar_url":avatar}})
			quality:=0.0;if max>0{quality=math.Min(1,float64(count)/float64(max))}
			candidates=append(candidates,algorithmclient.Candidate{ID:id,AuthorID:host,CreatedAt:created,Visible:true,Features:algorithmclient.Features{Following:following,InterestAffinity:quality,Quality:quality,Conversation:math.Min(1,float64(count)/10),ReportRisk:0}})
		}
		if rows.Err()!=nil{fail("SPACES_LIST_FAILED","Could not read live Spaces",http.StatusServiceUnavailable);return true}
		// Algorithm ranks only this already-authorized candidate set. Backend keeps
		// ownership of privacy filtering and hydrates the returned IDs from SQL.
		if s.algorithm!=nil&&len(candidates)>0{
			s.algorithmRequests.Add(1)
			ranked,rankErr:=s.algorithm.Rank(ctx,algorithmclient.Request{Now:time.Now().UTC(),Candidates:candidates,Limit:min(50,len(candidates)),AllowSensitive:false})
			if rankErr==nil{byID:=make(map[string]map[string]any,len(items));for _,item:=range items{byID[item["id"].(string)]=item};ordered:=make([]map[string]any,0,len(ranked.Items));for _,rankedItem:=range ranked.Items{if item,ok:=byID[rankedItem.ID];ok{item["ranking_score"]=rankedItem.Score;item["ranking_reasons"]=rankedItem.Reasons;ordered=append(ordered,item)}};if len(ordered)>0{items=ordered}}else{s.algorithmFailures.Add(1)}
		}
		writeJSON(w,http.StatusOK,map[string]any{"items":items});return true
	case "testagram.audio_spaces.pending_speaker_requests":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		var host bool;if e:=s.db.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.audio_spaces WHERE id=$1 AND host_id=$2 AND status='live')",id,userID).Scan(&host);e!=nil||!host{fail("HOST_ONLY","Only the host can view speaker requests",http.StatusForbidden);return true}
		rows,e:=s.db.Query(ctx,`SELECT r.user_id::text,r.status,r.requested_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.audio_space_speaker_requests r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.space_id=$1 AND r.status='pending' ORDER BY r.requested_at ASC LIMIT 100`,id)
		if e!=nil{fail("SPEAKER_REQUESTS_FAILED","Could not load speaker requests",http.StatusServiceUnavailable);return true}
		defer rows.Close();items:=make([]map[string]any,0)
		for rows.Next(){var userID,status,username,display,avatar string;var requested time.Time;if rows.Scan(&userID,&status,&requested,&username,&display,&avatar)!=nil{fail("SPEAKER_REQUESTS_FAILED","Could not read speaker requests",http.StatusServiceUnavailable);return true};items=append(items,map[string]any{"user_id":userID,"status":status,"requested_at":requested,"profiles":map[string]any{"id":userID,"username":username,"display_name":display,"avatar_url":avatar}})}
		if rows.Err()!=nil{fail("SPEAKER_REQUESTS_FAILED","Could not read speaker requests",http.StatusServiceUnavailable);return true};writeJSON(w,http.StatusOK,map[string]any{"items":items});return true
	case "testagram.audio_spaces.get":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		var host,title,description,state string;var count,max int;var started,ended *time.Time
		e:=s.db.QueryRow(ctx,`SELECT s.host_id::text,s.title,s.description,s.status,s.listener_count,s.max_audience,s.started_at,s.ended_at
			FROM testagram_local.audio_spaces s WHERE s.id=$1`,id).Scan(&host,&title,&description,&state,&count,&max,&started,&ended)
		if e!=nil{fail("SPACE_NOT_FOUND","Audio Space was not found",http.StatusNotFound);return true}
		var username,display,avatar string
		_ = s.db.QueryRow(ctx,"SELECT username,display_name,avatar_key FROM testagram_local.accounts WHERE user_id=$1",host).Scan(&username,&display,&avatar)
		writeJSON(w,http.StatusOK,map[string]any{"id":id,"host_id":host,"title":title,"description":description,"status":state,"is_live":state=="live","listener_count":count,"max_audience":max,"started_at":started,"ended_at":ended,"host":map[string]any{"id":host,"username":username,"display_name":display,"avatar_url":avatar}});return true
	case "testagram.audio_spaces.my_status":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		var role,requestStatus string
		e:=s.db.QueryRow(ctx,`SELECT COALESCE(p.role,''),COALESCE(r.status,'none')
			FROM testagram_local.audio_spaces s
			LEFT JOIN testagram_local.audio_space_participants p ON p.space_id=s.id AND p.user_id=$2 AND p.left_at IS NULL
			LEFT JOIN testagram_local.audio_space_speaker_requests r ON r.space_id=s.id AND r.user_id=$2
			WHERE s.id=$1`,id,userID).Scan(&role,&requestStatus)
		if e!=nil{fail("SPACE_STATUS_FAILED","Could not load Space membership",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusOK,map[string]any{"role":role,"speaker_request_status":requestStatus,"joined":role!=""});return true
	case "testagram.audio_spaces.create":
		title:=str("title");description:=str("description");if len(title)==0||len(title)>120||len(description)>2000{fail("INVALID_SPACE","Space title or description is invalid",http.StatusBadRequest);return true}
		max:=1000;if v,ok:=in["max_audience"].(float64);ok{max=int(v)};if max<2||max>10000{fail("INVALID_SPACE","Audience limit must be between 2 and 10000",http.StatusBadRequest);return true}
		tx,e:=s.db.Begin(ctx);if e!=nil{fail("SPACE_CREATE_FAILED","Could not create audio Space",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		var id string;e=tx.QueryRow(ctx,`INSERT INTO testagram_local.audio_spaces(host_id,title,description,status,started_at,max_audience)
			VALUES($1,$2,$3,'live',now(),$4) RETURNING id::text`,userID,title,description,max).Scan(&id)
		if e==nil{_,e=tx.Exec(ctx,"INSERT INTO testagram_local.audio_space_participants(space_id,user_id,role) VALUES($1,$2,'host')",id,userID)}
		if e==nil{e=tx.Commit(ctx)};if e!=nil{fail("SPACE_CREATE_FAILED","Could not create audio Space",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusCreated,map[string]any{"space_id":id,"status":"live","host_id":userID});return true
	case "testagram.audio_spaces.join":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		tx,e:=s.db.Begin(ctx);if e!=nil{fail("SPACE_JOIN_FAILED","Could not join Space",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		var max int;var state string;e=tx.QueryRow(ctx,"SELECT max_audience,status FROM testagram_local.audio_spaces WHERE id=$1 FOR UPDATE",id).Scan(&max,&state)
		if e!=nil||state!="live"{fail("SPACE_NOT_LIVE","Space is not live",http.StatusConflict);return true}
		var joined bool;_ = tx.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.audio_space_participants WHERE space_id=$1 AND user_id=$2 AND left_at IS NULL)",id,userID).Scan(&joined)
		if !joined{var count int;_ = tx.QueryRow(ctx,"SELECT count(*) FROM testagram_local.audio_space_participants WHERE space_id=$1 AND left_at IS NULL",id).Scan(&count);if count>=max{fail("SPACE_AUDIENCE_LIMIT","Space audience limit reached",http.StatusConflict);return true}}
		_,e=tx.Exec(ctx,`INSERT INTO testagram_local.audio_space_participants(space_id,user_id,role) VALUES($1,$2,'listener')
			ON CONFLICT(space_id,user_id) DO UPDATE SET joined_at=now(),left_at=NULL,role=CASE WHEN testagram_local.audio_space_participants.role='host' THEN 'host' ELSE 'listener' END`,id,userID)
		if e==nil{_,e=tx.Exec(ctx,"UPDATE testagram_local.audio_spaces SET listener_count=(SELECT count(*) FROM testagram_local.audio_space_participants WHERE space_id=$1 AND left_at IS NULL),updated_at=now() WHERE id=$1",id)}
		if e==nil{e=tx.Commit(ctx)};if e!=nil{fail("SPACE_JOIN_FAILED","Could not join Space",http.StatusServiceUnavailable);return true};writeJSON(w,http.StatusOK,map[string]any{"space_id":id,"joined":true});return true
	case "testagram.audio_spaces.leave":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		_,err=s.db.Exec(ctx,"UPDATE testagram_local.audio_space_participants SET left_at=now() WHERE space_id=$1 AND user_id=$2 AND left_at IS NULL AND role<>'host'",id,userID)
		if err==nil{_,err=s.db.Exec(ctx,"UPDATE testagram_local.audio_spaces SET listener_count=(SELECT count(*) FROM testagram_local.audio_space_participants WHERE space_id=$1 AND left_at IS NULL),updated_at=now() WHERE id=$1",id)}
		if err!=nil{fail("SPACE_LEAVE_FAILED","Could not leave Space",http.StatusServiceUnavailable);return true};writeJSON(w,http.StatusOK,map[string]any{"space_id":id,"left":true});return true
	case "testagram.audio_spaces.request_speaker":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		tag,e:=s.db.Exec(ctx,`INSERT INTO testagram_local.audio_space_speaker_requests(space_id,user_id,status)
			SELECT $1,$2,'pending' WHERE EXISTS(SELECT 1 FROM testagram_local.audio_spaces s JOIN testagram_local.audio_space_participants p ON p.space_id=s.id AND p.user_id=$2 AND p.left_at IS NULL WHERE s.id=$1 AND s.status='live' AND s.listener_requests_enabled=TRUE)
			ON CONFLICT(space_id,user_id) DO UPDATE SET status='pending',requested_at=now(),decided_at=NULL,decided_by=NULL`,id,userID)
		if e!=nil{fail("SPEAKER_REQUEST_FAILED","Could not request speaker access",http.StatusServiceUnavailable);return true};if tag.RowsAffected()!=1{fail("SPEAKER_REQUEST_NOT_ALLOWED","Join the live Space before requesting speaker access",http.StatusForbidden);return true};writeJSON(w,http.StatusOK,map[string]any{"space_id":id,"status":"pending"});return true
	case "testagram.audio_spaces.decide_speaker":
		id:=str("space_id");target:=str("user_id");decision:=str("decision");if !socialUUIDPattern.MatchString(id)||!socialUUIDPattern.MatchString(target)||(decision!="approved"&&decision!="denied"){fail("INVALID_SPEAKER_DECISION","Speaker decision is invalid",http.StatusBadRequest);return true}
		tx,e:=s.db.Begin(ctx);if e!=nil{fail("SPEAKER_DECISION_FAILED","Could not decide speaker request",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		var host string;if e=tx.QueryRow(ctx,"SELECT host_id::text FROM testagram_local.audio_spaces WHERE id=$1 FOR UPDATE",id).Scan(&host);e!=nil||host!=userID{fail("HOST_ONLY","Only the Space host can decide speaker requests",http.StatusForbidden);return true}
		tag,e:=tx.Exec(ctx,"UPDATE testagram_local.audio_space_speaker_requests SET status=$3,decided_at=now(),decided_by=$4 WHERE space_id=$1 AND user_id=$2 AND status='pending'",id,target,decision,userID)
		if e!=nil||tag.RowsAffected()!=1{fail("SPEAKER_REQUEST_NOT_PENDING","Speaker request is not pending",http.StatusConflict);return true}
		role:="listener";if decision=="approved"{role="speaker"}
		participantTag,e:=tx.Exec(ctx,"UPDATE testagram_local.audio_space_participants SET role=$3 WHERE space_id=$1 AND user_id=$2 AND left_at IS NULL",id,target,role)
		if e==nil&&participantTag.RowsAffected()!=1{e=pgx.ErrNoRows}
		if e==nil{e=tx.Commit(ctx)};if e!=nil{fail("SPEAKER_DECISION_FAILED","Could not update speaker role",http.StatusServiceUnavailable);return true};writeJSON(w,http.StatusOK,map[string]any{"space_id":id,"user_id":target,"status":decision});return true
	case "testagram.audio_spaces.end":
		id:=str("space_id");if !socialUUIDPattern.MatchString(id){fail("INVALID_SPACE","A valid Space ID is required",http.StatusBadRequest);return true}
		tx,e:=s.db.Begin(ctx);if e!=nil{fail("SPACE_END_FAILED","Could not end Space",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		var host,state string;e=tx.QueryRow(ctx,"SELECT host_id::text,status FROM testagram_local.audio_spaces WHERE id=$1 FOR UPDATE",id).Scan(&host,&state)
		if e!=nil||host!=userID||state!="live"{fail("HOST_ONLY","Only the host can end a live Space",http.StatusForbidden);return true}
		_,e=tx.Exec(ctx,"UPDATE testagram_local.audio_spaces SET status='ended',ended_at=now(),updated_at=now() WHERE id=$1",id)
		if e==nil{_,e=tx.Exec(ctx,"UPDATE testagram_local.audio_space_participants SET left_at=COALESCE(left_at,now()) WHERE space_id=$1",id)}
		if e==nil{e=tx.Commit(ctx)};if e!=nil{fail("SPACE_END_FAILED","Could not end Space",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusOK,map[string]any{"space_id":id,"ended":true});return true
	case "testagram.audio_spaces.superchat":
		id:=str("space_id");message:=str("message");amount,_,ok:=moneyAmount(in["amount"]);key:=str("idempotency_key")
		if !socialUUIDPattern.MatchString(id)||len(message)==0||len(message)>280||!ok||len(key)<8{fail("INVALID_SUPERCHAT","Space, amount, message or idempotency key is invalid",http.StatusBadRequest);return true}
		tx,e:=s.db.Begin(ctx);if e!=nil{fail("SUPERCHAT_FAILED","Could not send Super Chat",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		var host,state string;e=tx.QueryRow(ctx,"SELECT host_id::text,status FROM testagram_local.audio_spaces WHERE id=$1 FOR SHARE",id).Scan(&host,&state)
		if e!=nil||state!="live"||host==userID{fail("SPACE_NOT_AVAILABLE","Space is not live or recipient is invalid",http.StatusConflict);return true}
		transferID,duplicate,e:=walletTransferTx(ctx,tx,userID,host,amount,"superchat", "Audio Space Super Chat",key)
		if e!=nil{fail("SUPERCHAT_PAYMENT_FAILED","Wallet payment could not be completed",http.StatusConflict);return true}
		if !duplicate{_,e=tx.Exec(ctx,"INSERT INTO testagram_local.audio_space_superchats(space_id,sender_id,transfer_id,message,amount,pinned_until) VALUES($1,$2,$3,$4,$5,now()+interval '60 seconds')",id,userID,transferID,message,amount);if e!=nil{fail("SUPERCHAT_FAILED","Could not record Super Chat",http.StatusServiceUnavailable);return true}}
		if e=tx.Commit(ctx);e!=nil{fail("SUPERCHAT_FAILED","Could not commit Super Chat",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusOK,map[string]any{"transfer_id":transferID,"status":"completed","duplicate":duplicate});return true
	}
	return true
}

