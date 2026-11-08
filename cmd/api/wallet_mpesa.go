package main

import (
	"context"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type mpesaCallbackEnvelope struct {
	Body struct {
		Callback struct {
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode int `json:"ResultCode"`
			ResultDesc string `json:"ResultDesc"`
			Metadata struct { Items []struct { Name string `json:"Name"`; Value any `json:"Value"` } `json:"Item"` } `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}

func mpesaConfiguration() (base,shortcode,passkey,key,secret,callback string,err error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MPESA_ENV"))) {
	case "sandbox": base="https://sandbox.safaricom.co.ke"
	case "live": base="https://api.safaricom.co.ke"
	default: err=errors.New("MPESA_ENV must be sandbox or live");return
	}
	shortcode=strings.TrimSpace(os.Getenv("MPESA_SHORTCODE"));passkey=strings.TrimSpace(os.Getenv("MPESA_PASSKEY"))
	key=strings.TrimSpace(os.Getenv("MPESA_CONSUMER_KEY"));secret=strings.TrimSpace(os.Getenv("MPESA_CONSUMER_SECRET"))
	callback=strings.TrimSpace(os.Getenv("MPESA_CALLBACK_URL"))
	u,e:=url.Parse(callback)
	if shortcode==""||passkey==""||key==""||secret==""||e!=nil||u.Scheme!="https"||u.Host==""||u.User!=nil||u.Fragment!="" {
		err=errors.New("M-Pesa STK Push credentials or HTTPS callback URL are not configured")
	}
	return
}

func mpesaAccessToken(ctx context.Context,base,key,secret string)(string,error){
	req,err:=http.NewRequestWithContext(ctx,http.MethodGet,base+"/oauth/v1/generate?grant_type=client_credentials",nil);if err!=nil{return "",err}
	req.Header.Set("Authorization","Basic "+base64.StdEncoding.EncodeToString([]byte(key+":"+secret)))
	client:=&http.Client{Timeout:8*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
	resp,err:=client.Do(req);if err!=nil{return "",err};defer resp.Body.Close()
	raw,err:=io.ReadAll(io.LimitReader(resp.Body,32<<10));if err!=nil{return "",err}
	if resp.StatusCode<200||resp.StatusCode>=300{return "",fmt.Errorf("M-Pesa OAuth returned HTTP %d",resp.StatusCode)}
	var payload struct{AccessToken string `json:"access_token"`};if json.Unmarshal(raw,&payload)!=nil||payload.AccessToken==""{return "",errors.New("M-Pesa OAuth response did not contain an access token")}
	return payload.AccessToken,nil
}

func normalizeMpesaPhone(raw string)(string,bool){
	digits:=strings.Map(func(r rune)rune{if r>='0'&&r<='9'{return r};return -1},raw)
	if strings.HasPrefix(digits,"254")&&len(digits)==12&&(digits[3]=='7'||digits[3]=='1'){return digits,true}
	if strings.HasPrefix(digits,"0")&&len(digits)==10&&(digits[1]=='7'||digits[1]=='1'){return "254"+digits[1:],true}
	if len(digits)==9&&(digits[0]=='7'||digits[0]=='1'){return "254"+digits,true}
	return "",false
}

func mpesaTimestamp() string { return time.Now().In(time.FixedZone("EAT",3*60*60)).Format("20060102150405") }

func (s *service) nativeMPesaCapability(w http.ResponseWriter,r *http.Request,requestID string,body capabilityRequest) bool {
	name:=strings.TrimSpace(body.Capability)
	switch name {case "testagram.wallet.mpesa.stk_push","testagram.wallet.mpesa.status":default:return false}
	userID,status,authErr:=s.authenticatedMediaUser(r)
	if authErr!=nil||!socialUUIDPattern.MatchString(userID){if authErr==nil{status=http.StatusUnauthorized};writeJSON(w,status,errorBody("AUTH_REQUIRED","A valid authenticated session is required",requestID));return true}
	ctx,cancel:=context.WithTimeout(r.Context(),15*time.Second);defer cancel()
	var active bool
	if err:=s.db.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$1 AND account_status='active')",userID).Scan(&active);err!=nil||!active{writeJSON(w,http.StatusForbidden,errorBody("ACCOUNT_NOT_ACTIVE","This account cannot use M-Pesa",requestID));return true}
	input:=body.Input
	str:=func(key string)string{v,_:=input[key].(string);return strings.TrimSpace(v)}
	fail:=func(code,msg string,status int){writeJSON(w,status,errorBody(code,msg,requestID))}
	switch name {
	case "testagram.wallet.mpesa.status":
		checkout:=str("checkout_request_id");if checkout==""||len(checkout)>128{fail("INVALID_CHECKOUT_REQUEST","A valid checkout request ID is required",http.StatusBadRequest);return true}
		var state,receipt,amount string;var created time.Time
		err:=s.db.QueryRow(ctx,`SELECT status,COALESCE(receipt_number,''),amount_kes::text,created_at FROM testagram_local.wallet_topups WHERE checkout_request_id=$1 AND user_id=$2`,checkout,userID).Scan(&state,&receipt,&amount,&created)
		if err!=nil{fail("TOPUP_NOT_FOUND","M-Pesa top-up was not found",http.StatusNotFound);return true}
		writeJSON(w,http.StatusOK,map[string]any{"status":state,"checkout_request_id":checkout,"receipt_number":receipt,"amount_kes":amount,"created_at":created});return true
	case "testagram.wallet.mpesa.stk_push":
		phone,ok:=normalizeMpesaPhone(str("phone"));if !ok{fail("INVALID_PHONE","Enter a valid Kenyan Safaricom number",http.StatusBadRequest);return true}
		amountRaw:=input["amount_kes"];var amount float64
		switch v:=amountRaw.(type){case float64:amount=v;case string:amount,_=strconv.ParseFloat(strings.TrimSpace(v),64)}
		if amount<10||amount>150000||amount!=mathFloor(amount){fail("INVALID_AMOUNT","M-Pesa top-ups must be whole KES amounts between 10 and 150,000",http.StatusBadRequest);return true}
		key:=str("idempotency_key");if key==""{key=strings.TrimSpace(r.Header.Get("Idempotency-Key"))};if len(key)<8||len(key)>128{fail("INVALID_IDEMPOTENCY_KEY","A valid idempotency key is required",http.StatusBadRequest);return true}
		base,shortcode,passkey,consumerKey,consumerSecret,callback,configErr:=mpesaConfiguration();if configErr!=nil{fail("MPESA_NOT_CONFIGURED","M-Pesa STK Push is not configured on Backend",http.StatusServiceUnavailable);return true}
		if _,err:=s.db.Exec(ctx,"INSERT INTO testagram_local.wallets(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING",userID);err!=nil{fail("WALLET_UNAVAILABLE","Wallet is unavailable",http.StatusServiceUnavailable);return true}
		var walletStatus,currency string
		if err:=s.db.QueryRow(ctx,"SELECT status,currency FROM testagram_local.wallets WHERE user_id=$1",userID).Scan(&walletStatus,&currency);err!=nil||walletStatus!="active"||strings.TrimSpace(currency)!="KES"{fail("WALLET_UNAVAILABLE","An active KES wallet is required for M-Pesa top-ups",http.StatusConflict);return true}
		reference:="TS"+strings.TrimPrefix(newCallRoom(),"testagram-")[:18]
		var topupID,requestReference,state string;var checkout *string
		err:=s.db.QueryRow(ctx,`INSERT INTO testagram_local.wallet_topups(user_id,amount_kes,phone_e164,idempotency_key,request_reference)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id,idempotency_key) DO NOTHING
			RETURNING id::text,request_reference,status,checkout_request_id`,userID,amount,phone,key,reference).Scan(&topupID,&requestReference,&state,&checkout)
		if err==pgx.ErrNoRows{
			err=s.db.QueryRow(ctx,"SELECT id::text,request_reference,status,checkout_request_id FROM testagram_local.wallet_topups WHERE user_id=$1 AND idempotency_key=$2",userID,key).Scan(&topupID,&requestReference,&state,&checkout)
			if err!=nil{fail("TOPUP_IDEMPOTENCY_FAILED","Could not recover the prior M-Pesa request",http.StatusServiceUnavailable);return true}
			if checkout!=nil&&*checkout!=""{writeJSON(w,http.StatusOK,map[string]any{"status":state,"checkout_request_id":*checkout,"request_reference":requestReference,"duplicate":true});return true}
			fail("TOPUP_RECONCILIATION_PENDING","This M-Pesa request is still being reconciled. Do not submit it again.",http.StatusConflict);return true
		}
		if err!=nil{fail("TOPUP_CREATE_FAILED","Could not create M-Pesa top-up request",http.StatusServiceUnavailable);return true}
		access,err:=mpesaAccessToken(ctx,base,consumerKey,consumerSecret);if err!=nil{_,_=s.db.Exec(ctx,"UPDATE testagram_local.wallet_topups SET status='failed',result_description='provider authentication failed',updated_at=now() WHERE id=$1",topupID);fail("MPESA_PROVIDER_UNAVAILABLE","M-Pesa authentication failed",http.StatusBadGateway);return true}
		timestamp:=mpesaTimestamp();password:=base64.StdEncoding.EncodeToString([]byte(shortcode+passkey+timestamp))
		payload:=map[string]any{"BusinessShortCode":shortcode,"Password":password,"Timestamp":timestamp,"TransactionType":"CustomerPayBillOnline","Amount":int64(amount),"PartyA":phone,"PartyB":shortcode,"PhoneNumber":phone,"CallBackURL":callback,"AccountReference":requestReference,"TransactionDesc":"Testagram wallet top-up"}
		raw,_:=json.Marshal(payload);req,err:=http.NewRequestWithContext(ctx,http.MethodPost,base+"/mpesa/stkpush/v1/processrequest",strings.NewReader(string(raw)));if err!=nil{fail("MPESA_REQUEST_FAILED","Could not prepare M-Pesa request",http.StatusBadGateway);return true}
		req.Header.Set("Authorization","Bearer "+access);req.Header.Set("Content-Type","application/json")
		client:=&http.Client{Timeout:10*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
		resp,err:=client.Do(req);if err!=nil{fail("MPESA_PROVIDER_UNAVAILABLE","M-Pesa request could not be reached; check top-up status before retrying",http.StatusBadGateway);return true};defer resp.Body.Close()
		responseBody,_:=io.ReadAll(io.LimitReader(resp.Body,32<<10));var provider map[string]any;_ = json.Unmarshal(responseBody,&provider)
		checkoutID,_:=provider["CheckoutRequestID"].(string);merchantID,_:=provider["MerchantRequestID"].(string);responseCode:=fmt.Sprint(provider["ResponseCode"])
		if resp.StatusCode<200||resp.StatusCode>=300||responseCode!="0"||checkoutID==""{
			_,_=s.db.Exec(ctx,"UPDATE testagram_local.wallet_topups SET status='failed',result_description=$2,provider_response=$3::jsonb,updated_at=now() WHERE id=$1",topupID,"M-Pesa STK request was rejected",string(responseBody))
			fail("MPESA_REQUEST_REJECTED","M-Pesa did not accept the STK Push request",http.StatusBadGateway);return true
		}
		tx,err:=s.db.Begin(ctx);if err!=nil{fail("TOPUP_PERSIST_FAILED","M-Pesa accepted the request but Backend could not persist it",http.StatusServiceUnavailable);return true};defer func(){_ = tx.Rollback(context.Background())}()
		_,err=tx.Exec(ctx,`UPDATE testagram_local.wallet_topups SET checkout_request_id=$2,merchant_request_id=$3,status='pending',provider_response=$4::jsonb,updated_at=now() WHERE id=$1`,topupID,checkoutID,merchantID,string(responseBody))
		if err==nil{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.wallet_transactions(user_id,topup_id,kind,amount,currency,status,provider,provider_order_id,provider_reference,provider_status,metadata)
			VALUES($1,$2,'deposit',$3,'KES','pending','mpesa',$4,$5,'PENDING',jsonb_build_object('phone',$6,'amount_kes',$3)) ON CONFLICT DO NOTHING`,userID,topupID,amount,checkoutID,requestReference,phone)}
		if err==nil{err=tx.Commit(ctx)}
		if err!=nil{fail("TOPUP_PERSIST_FAILED","M-Pesa accepted the request but Backend could not persist the settlement record; do not retry",http.StatusServiceUnavailable);return true}
		writeJSON(w,http.StatusAccepted,map[string]any{"status":"pending","checkout_request_id":checkoutID,"merchant_request_id":merchantID,"request_reference":requestReference,"amount_kes":amount,"currency":"KES","duplicate":false});return true
	}
	return true
}

func mathFloor(value float64) float64 { if value<0{return -1};return float64(int64(value)) }

func (s *service) mpesaCallback(w http.ResponseWriter,r *http.Request) {
	r.Body=http.MaxBytesReader(w,r.Body,32<<10)
	rawBody,readErr:=io.ReadAll(r.Body)
	if readErr!=nil{writeJSON(w,http.StatusBadRequest,map[string]any{"ResultCode":1,"ResultDesc":"Invalid callback"});return}
	var input mpesaCallbackEnvelope
	decoder:=json.NewDecoder(bytes.NewReader(rawBody));decoder.UseNumber()
	if err:=decoder.Decode(&input);err!=nil{writeJSON(w,http.StatusBadRequest,map[string]any{"ResultCode":1,"ResultDesc":"Invalid callback"});return}
	cb:=input.Body.Callback;checkout:=strings.TrimSpace(cb.CheckoutRequestID)
	if checkout==""{writeJSON(w,http.StatusBadRequest,map[string]any{"ResultCode":1,"ResultDesc":"Missing CheckoutRequestID"});return}
	ctx,cancel:=context.WithTimeout(r.Context(),15*time.Second);defer cancel()
	var userID,phone,amountRaw,state string;var topupID string
	err:=s.db.QueryRow(ctx,`SELECT id::text,user_id::text,phone_e164,amount_kes::text,status FROM testagram_local.wallet_topups WHERE checkout_request_id=$1`,checkout).Scan(&topupID,&userID,&phone,&amountRaw,&state)
	if err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]any{"ResultCode":1,"ResultDesc":"Top-up record unavailable; retry callback"});return}
	if state=="settled"{writeJSON(w,http.StatusOK,map[string]any{"ResultCode":0,"ResultDesc":"Already settled"});return}
	if cb.ResultCode!=0{
		tx,e:=s.db.Begin(ctx);if e==nil{_,e=tx.Exec(ctx,"UPDATE testagram_local.wallet_topups SET status='failed',result_code=$2,result_description=$3,callback_data=$4::jsonb,updated_at=now() WHERE id=$1 AND status='pending'",topupID,cb.ResultCode,cb.ResultDesc,string(rawBody));if e==nil{_,e=tx.Exec(ctx,"UPDATE testagram_local.wallet_transactions SET status='failed',provider_status=$2 WHERE topup_id=$1",topupID,cb.ResultDesc)};if e==nil{e=tx.Commit(ctx)};_ = tx.Rollback(context.Background())}
		if e!=nil{writeJSON(w,http.StatusInternalServerError,map[string]any{"ResultCode":1,"ResultDesc":"Failed payment update; retry callback"});return}
		writeJSON(w,http.StatusOK,map[string]any{"ResultCode":0,"ResultDesc":"Accepted"});return
	}
	var receipt,callbackPhone string;var callbackAmount float64
	for _,item:=range cb.Metadata.Items{switch item.Name{case "MpesaReceiptNumber":receipt=fmt.Sprint(item.Value);case "Amount":callbackAmount,_=strconv.ParseFloat(fmt.Sprint(item.Value),64);case "PhoneNumber":callbackPhone=fmt.Sprint(item.Value)}}
	normalizedPhone,phoneOK:=normalizeMpesaPhone(callbackPhone);storedAmount,_:=strconv.ParseFloat(amountRaw,64)
	if receipt==""||!phoneOK||normalizedPhone!=phone||callbackAmount!=storedAmount{writeJSON(w,http.StatusBadRequest,map[string]any{"ResultCode":1,"ResultDesc":"Callback amount, phone or receipt mismatch"});return}
	if err:=verifyMpesaSuccess(ctx,checkout);err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]any{"ResultCode":1,"ResultDesc":"Provider payment not yet verified; retry callback"});return}
	tx,err:=s.db.Begin(ctx);if err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]any{"ResultCode":1,"ResultDesc":"Settlement unavailable"});return};defer func(){_ = tx.Rollback(context.Background())}()
	var lockedState string
	if err=tx.QueryRow(ctx,"SELECT status FROM testagram_local.wallet_topups WHERE id=$1 FOR UPDATE",topupID).Scan(&lockedState);err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]any{"ResultCode":1,"ResultDesc":"Settlement unavailable"});return}
	if lockedState=="settled"{writeJSON(w,http.StatusOK,map[string]any{"ResultCode":0,"ResultDesc":"Already settled"});return}
	if lockedState!="pending"{writeJSON(w,http.StatusConflict,map[string]any{"ResultCode":1,"ResultDesc":"Top-up is not pending"});return}
	_,err=tx.Exec(ctx,"UPDATE testagram_local.wallet_topups SET status='settled',receipt_number=$2,result_code=0,result_description=$3,callback_data=$4::jsonb,settled_at=now(),updated_at=now() WHERE id=$1",topupID,receipt,cb.ResultDesc,callbackRaw(r))
	if err==nil{
		var walletTag pgconn.CommandTag
		walletTag,err=tx.Exec(ctx,"UPDATE testagram_local.wallets SET balance=balance+$2,updated_at=now() WHERE user_id=$1 AND status='active'",userID,amountRaw)
		if err==nil&&walletTag.RowsAffected()!=1{err=errors.New("wallet is unavailable for settlement")}
	}
	if err==nil{_,err=tx.Exec(ctx,"INSERT INTO testagram_local.wallet_ledger_entries(transfer_id,topup_id,user_id,direction,amount,currency) VALUES(NULL,$1,$2,'credit',$3,'KES')",topupID,userID,amountRaw)}
	if err==nil{_,err=tx.Exec(ctx,"UPDATE testagram_local.wallet_transactions SET status='completed',provider_status='SUCCESS',metadata=metadata||jsonb_build_object('receipt_number',$2) WHERE topup_id=$1",topupID,receipt)}
	if err==nil{_,err=tx.Exec(ctx,`INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,payload,event_key)
		VALUES($1,NULL,'wallet.topup.completed',jsonb_build_object('topup_id',$2,'amount_kes',$3,'receipt_number',$4),'wallet-topup:'||$2)
		ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`,userID,topupID,amountRaw,receipt)}
	if err==nil{err=tx.Commit(ctx)}
	if err!=nil{writeJSON(w,http.StatusInternalServerError,map[string]any{"ResultCode":1,"ResultDesc":"Settlement failed; retry callback"});return}
	writeJSON(w,http.StatusOK,map[string]any{"ResultCode":0,"ResultDesc":"Accepted"})
}


func verifyMpesaSuccess(ctx context.Context,checkout string) error {
	base,shortcode,passkey,key,secret,_,err:=mpesaConfiguration();if err!=nil{return err}
	access,err:=mpesaAccessToken(ctx,base,key,secret);if err!=nil{return err}
	timestamp:=mpesaTimestamp();password:=base64.StdEncoding.EncodeToString([]byte(shortcode+passkey+timestamp))
	body,_:=json.Marshal(map[string]string{"BusinessShortCode":shortcode,"Password":password,"Timestamp":timestamp,"CheckoutRequestID":checkout})
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,base+"/mpesa/stkpushquery/v1/query",strings.NewReader(string(body)));if err!=nil{return err}
	req.Header.Set("Authorization","Bearer "+access);req.Header.Set("Content-Type","application/json")
	client:=&http.Client{Timeout:8*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
	resp,err:=client.Do(req);if err!=nil{return err};defer resp.Body.Close()
	raw,err:=io.ReadAll(io.LimitReader(resp.Body,32<<10));if err!=nil{return err}
	var result map[string]any;if json.Unmarshal(raw,&result)!=nil||resp.StatusCode<200||resp.StatusCode>=300||fmt.Sprint(result["ResponseCode"])!="0"||fmt.Sprint(result["ResultCode"])!="0"{return errors.New("Safaricom did not confirm successful payment")}
	return nil
}
