package main

import (
 "bytes"
 "context"
 "crypto/rand"
 "crypto/sha256"
 "encoding/base64"
 "encoding/json"
 "crypto/hmac"
 "io"
 "errors"
 "net/http"
 "os"
 "strings"
 "time"

 "github.com/go-webauthn/webauthn/protocol"
 "github.com/go-webauthn/webauthn/webauthn"
 "github.com/jackc/pgx/v5"
)

type staffWebAuthnUser struct {
 id string
 name string
 handle []byte
 credentials []webauthn.Credential
}
func (u *staffWebAuthnUser) WebAuthnID() []byte { return u.handle }
func (u *staffWebAuthnUser) WebAuthnName() string { return u.name }
func (u *staffWebAuthnUser) WebAuthnDisplayName() string { return u.name }
func (u *staffWebAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (a *app) staffWebAuthn() (*webauthn.WebAuthn,string,error) {
 rpid:=strings.TrimSpace(os.Getenv("TESTAGRAM_WEBAUTHN_RPID"))
 origins:=strings.FieldsFunc(os.Getenv("TESTAGRAM_WEBAUTHN_ORIGINS"),func(r rune)bool{return r==','||r==' '||r=='\n'})
 if rpid==""||len(origins)==0{return nil,"",errors.New("explicit WebAuthn RP ID and origins are required")}
 for _,origin:=range origins { if !strings.HasPrefix(origin,"https://")&&!(strings.HasPrefix(origin,"http://localhost")||strings.HasPrefix(origin,"http://127.0.0.1")){return nil,"",errors.New("WebAuthn origins must use HTTPS outside localhost")} }
 wa,err:=webauthn.New(&webauthn.Config{
  RPID:rpid,RPDisplayName:"Testagram Staff",RPOrigins:origins,
  AuthenticatorSelection:protocol.AuthenticatorSelection{UserVerification:protocol.VerificationRequired},
  Timeouts:webauthn.TimeoutsConfig{Registration:webauthn.TimeoutConfig{Enforce:true,Timeout:2*time.Minute},Login:webauthn.TimeoutConfig{Enforce:true,Timeout:90*time.Second}},
 })
 return wa,rpid,err
}

func (a *app) loadStaffWebAuthnUser(ctx context.Context,id,rpid string)(*staffWebAuthnUser,error){
 u:=&staffWebAuthnUser{id:id}
 if err:=a.database.QueryRow(ctx,"SELECT COALESCE(email,user_id::text) FROM testagram_auth.staff_principals WHERE user_id=$1::uuid AND enabled=TRUE",id).Scan(&u.name);err!=nil{return nil,err}
 if err:=a.database.QueryRow(ctx,"INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle) VALUES($1,$2::uuid,gen_random_bytes(32)) ON CONFLICT(rpid,user_id) DO UPDATE SET user_id=EXCLUDED.user_id RETURNING user_handle",rpid,id).Scan(&u.handle);err!=nil{return nil,err}
 rows,err:=a.database.Query(ctx,"SELECT credential FROM testagram_auth.staff_webauthn_credentials WHERE rpid=$1 AND user_id=$2::uuid AND revoked_at IS NULL ORDER BY id",rpid,id)
 if err!=nil{return nil,err};defer rows.Close()
 for rows.Next(){
  var encrypted []byte
  if err:=rows.Scan(&encrypted);err!=nil{return nil,err}
  if len(encrypted)<a.aead.NonceSize(){return nil,errors.New("invalid encrypted credential")}
  plain,err:=a.aead.Open(nil,encrypted[:a.aead.NonceSize()],encrypted[a.aead.NonceSize():],[]byte("testagram-staff-webauthn-credential-v1"))
  if err!=nil{return nil,errors.New("stored credential integrity verification failed")}
  var c webauthn.Credential
  if err:=json.Unmarshal(plain,&c);err!=nil{return nil,err}
  u.credentials=append(u.credentials,c)
 }
 return u,rows.Err()
}
func (a *app) encryptStaffCredential(c *webauthn.Credential)([]byte,error){
 plain,err:=json.Marshal(c);if err!=nil{return nil,err}
 nonce:=make([]byte,a.aead.NonceSize());if _,err=rand.Read(nonce);err!=nil{return nil,err}
 return append(nonce,a.aead.Seal(nil,nonce,plain,[]byte("testagram-staff-webauthn-credential-v1"))...),nil
}
func (a *app) saveStaffChallenge(ctx context.Context,userID,kind string,sd *webauthn.SessionData)error{
 raw,err:=json.Marshal(sd);if err!=nil{return err}
 sum:=sha256.Sum256([]byte(sd.Challenge))
 _,err=a.database.Exec(ctx,"INSERT INTO testagram_auth.staff_webauthn_challenges(challenge_hash,user_id,ceremony,session_data,expires_at) VALUES($1,$2::uuid,$3,$4::jsonb,LEAST($5::timestamptz,now()+interval '2 minutes'))",sum[:],userID,kind,raw,sd.Expires)
 return err
}
func (a *app) handleStaffWebAuthnRegisterBegin(w http.ResponseWriter,r *http.Request){
 _,user,ok:=a.requireStaff(w,r);if !ok{return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil{writeJSON(w,503,map[string]string{"error":"Canonical staff identity or credential store unavailable"});return}
 if len(u.credentials)>0{writeJSON(w,409,map[string]string{"error":"An authenticator is already enrolled; authenticate before adding another."});return}
 options,sd,err:=wa.BeginRegistration(u,webauthn.WithRegistrationOrigin(a.publicOrigin))
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not create registration ceremony"});return}
 if err=a.saveStaffChallenge(r.Context(),user.ID,"registration",sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist registration challenge"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"publicKey":options.Response,"challenge":sd.Challenge})
}
func (a *app) handleStaffWebAuthnRegisterFinish(w http.ResponseWriter,r *http.Request){
 _,user,ok:=a.requireStaff(w,r);if !ok{return}
 var input struct{Challenge string `json:"challenge"`;Response json.RawMessage `json:"response"`}
 if !decodeBody(w,r,&input){return}
 if input.Challenge==""||len(input.Response)==0||len(input.Response)>1<<20{writeJSON(w,400,map[string]string{"error":"Invalid registration response"});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 tx,err:=a.database.Begin(r.Context());if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential store unavailable"});return};defer tx.Rollback(r.Context())
 sum:=sha256.Sum256([]byte(input.Challenge))
 var raw []byte
 err=tx.QueryRow(r.Context(),"SELECT session_data FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1 AND user_id=$2::uuid AND ceremony='registration' AND expires_at>now() FOR UPDATE",sum[:],user.ID).Scan(&raw)
 if err!=nil{writeJSON(w,401,map[string]string{"error":"Registration challenge expired, invalid or already used"});return}
 var sd webauthn.SessionData
 if err=json.Unmarshal(raw,&sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Stored registration state is invalid"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil||len(u.credentials)>0{writeJSON(w,403,map[string]string{"error":"Staff enrollment is not authorized"});return}
 req:=r.Clone(r.Context());req.Body=io.NopCloser(bytes.NewReader(input.Response));req.ContentLength=int64(len(input.Response))
 credential,err:=wa.FinishRegistration(u,sd,req)
 if err!=nil{writeJSON(w,401,map[string]string{"error":"Cryptographic registration verification failed"});return}
 encrypted,err:=a.encryptStaffCredential(credential);if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential encryption failed"});return}
 _,err=tx.Exec(r.Context(),"INSERT INTO testagram_auth.staff_webauthn_credentials(rpid,user_id,credential_id,credential,sign_count,user_verified,backup_eligible,backup_state) VALUES($1,$2::uuid,$3,$4,$5,$6,$7,$8)",rpid,user.ID,credential.ID,encrypted,credential.Authenticator.SignCount,credential.Flags.UserVerified,credential.Flags.BackupEligible,credential.Flags.BackupState)
 if err!=nil{writeJSON(w,409,map[string]string{"error":"Credential already exists or could not be stored"});return}
 tag,err:=tx.Exec(r.Context(),"UPDATE testagram_auth.staff_principals SET mfa_enrolled_at=COALESCE(mfa_enrolled_at,now()) WHERE user_id=$1::uuid AND enabled=TRUE",user.ID)
 if err!=nil||tag.RowsAffected()!=1{writeJSON(w,503,map[string]string{"error":"Could not activate staff MFA enrollment"});return}
 if _,err=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not consume registration challenge"});return}
 if err=tx.Commit(r.Context());err!=nil{writeJSON(w,503,map[string]string{"error":"Could not commit WebAuthn enrollment"});return}
 writeJSON(w,200,map[string]any{"ok":true,"credential_id":base64.RawURLEncoding.EncodeToString(credential.ID)})
}
func (a *app) handleStaffWebAuthnLoginBegin(w http.ResponseWriter,r *http.Request){
 _,user,ok:=a.requireStaff(w,r);if !ok{return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil||len(u.credentials)==0{writeJSON(w,403,map[string]string{"error":"No registered authenticator exists"});return}
 options,sd,err:=wa.BeginLogin(u,webauthn.WithUserVerification(protocol.VerificationRequired),webauthn.WithLoginOrigin(a.publicOrigin))
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not create assertion ceremony"});return}
 if err=a.saveStaffChallenge(r.Context(),user.ID,"assertion",sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist assertion challenge"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"publicKey":options.Response,"challenge":sd.Challenge})
}
func (a *app) mintStaffElevation(user authUser)(string,int64,error){
 secret:=[]byte(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")));if len(secret)<32{return "",0,errors.New("elevation signing key unavailable")}
 now:=time.Now().Unix();exp:=now+5*60
 header,_:=json.Marshal(map[string]string{"alg":"HS256","typ":"JWT"})
 claims,_:=json.Marshal(map[string]any{"iss":"testagram-admin-console","aud":"testagram-admin-sensitive","sub":user.ID,"staff_role":user.StaffRole,"iat":now,"exp":exp,"mfa_at":now,"jti":base64.RawURLEncoding.EncodeToString(make([]byte,16))})
 enc:=base64.RawURLEncoding
 unsigned:=enc.EncodeToString(header)+"."+enc.EncodeToString(claims)
 mac:=hmac.New(sha256.New,secret);_,_=mac.Write([]byte(unsigned))
 return unsigned+"."+enc.EncodeToString(mac.Sum(nil)),exp,nil
}
func (a *app) handleStaffWebAuthnLoginFinish(w http.ResponseWriter,r *http.Request){
 s,user,ok:=a.requireStaff(w,r);if !ok{return}
 var input struct{Challenge string `json:"challenge"`;Response json.RawMessage `json:"response"`}
 if !decodeBody(w,r,&input){return}
 if input.Challenge==""||len(input.Response)==0||len(input.Response)>1<<20{writeJSON(w,400,map[string]string{"error":"Invalid assertion response"});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 tx,err:=a.database.Begin(r.Context());if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential store unavailable"});return};defer tx.Rollback(r.Context())
 sum:=sha256.Sum256([]byte(input.Challenge));var raw []byte
 err=tx.QueryRow(r.Context(),"SELECT session_data FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1 AND user_id=$2::uuid AND ceremony='assertion' AND expires_at>now() FOR UPDATE",sum[:],user.ID).Scan(&raw)
 if err!=nil{writeJSON(w,401,map[string]string{"error":"Assertion challenge expired, invalid or already used"});return}
 var sd webauthn.SessionData;if err=json.Unmarshal(raw,&sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Stored assertion state is invalid"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil||len(u.credentials)==0{writeJSON(w,403,map[string]string{"error":"No registered authenticator exists"});return}
 req:=r.Clone(r.Context());req.Body=io.NopCloser(bytes.NewReader(input.Response));req.ContentLength=int64(len(input.Response))
 credential,err:=wa.FinishLogin(u,sd,req)
 if err!=nil||credential.Authenticator.CloneWarning||!credential.Flags.UserVerified{writeJSON(w,401,map[string]string{"error":"WebAuthn assertion signature, origin, RP ID, user verification or counter validation failed"});return}
 encrypted,err:=a.encryptStaffCredential(credential);if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential update encryption failed"});return}
 tag,err:=tx.Exec(r.Context(),"UPDATE testagram_auth.staff_webauthn_credentials SET credential=$1,sign_count=$2,clone_warning=$3,user_verified=$4,backup_eligible=$5,backup_state=$6,last_used_at=now() WHERE rpid=$7 AND user_id=$8::uuid AND credential_id=$9 AND revoked_at IS NULL AND sign_count<=$2",encrypted,credential.Authenticator.SignCount,credential.Authenticator.CloneWarning,credential.Flags.UserVerified,credential.Flags.BackupEligible,credential.Flags.BackupState,rpid,user.ID,credential.ID)
 if err!=nil||tag.RowsAffected()!=1{writeJSON(w,401,map[string]string{"error":"Credential counter changed or credential was revoked"});return}
 if _,err=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not consume assertion challenge"});return}
 if err=tx.Commit(r.Context());err!=nil{writeJSON(w,503,map[string]string{"error":"Could not commit assertion"});return}
 token,exp,err:=a.mintStaffElevation(user);if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not issue backend MFA elevation"});return}
 s.ElevatedToken=token
 if err=a.setSession(w,s);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist elevated session"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"ok":true,"elevated_token":token,"expires_at":exp})
}
func (a *app) handleWebAuthnReadiness(w http.ResponseWriter,r *http.Request){
 ctx,cancel:=context.WithTimeout(r.Context(),2*time.Second);defer cancel()
 _,_,staff:=a.currentUser(w,r);if !staff{writeJSON(w,401,map[string]string{"error":"Staff authentication required"});return}
 wa,rpid,err:=a.staffWebAuthn();dbOK:=a.database!=nil&&a.database.Ping(ctx)==nil
 schemaOK:=false;if dbOK{_ = a.database.QueryRow(ctx,"SELECT to_regclass('testagram_auth.staff_webauthn_users') IS NOT NULL AND to_regclass('testagram_auth.staff_webauthn_credentials') IS NOT NULL AND to_regclass('testagram_auth.staff_webauthn_challenges') IS NOT NULL").Scan(&schemaOK)}
 ready:=err==nil&&wa!=nil&&dbOK&&schemaOK&&len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))>=32&&a.aead!=nil
 writeJSON(w,200,map[string]any{"ready":ready,"rpid_configured":rpid!="","origins_configured":err==nil,"database":dbOK,"schema":schemaOK,"signing_key":len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))>=32,"credential_encryption":a.aead!=nil})
}
