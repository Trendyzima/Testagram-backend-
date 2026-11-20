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
 if err:=a.database.QueryRow(ctx,"SELECT i.email_normalized FROM testagram_auth.staff_principals sp JOIN testagram_auth.identities i ON i.user_id=sp.user_id WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE",id).Scan(&u.name);err!=nil{return nil,err}
 handle:=make([]byte,32);if _,err:=rand.Read(handle);err!=nil{return nil,err}
 if err:=a.database.QueryRow(ctx,"INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle) VALUES($1,$2::uuid,$3) ON CONFLICT(rpid,user_id) DO UPDATE SET user_id=EXCLUDED.user_id RETURNING user_handle",rpid,id,handle).Scan(&u.handle);err!=nil{return nil,err}
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
 plain,err:=json.Marshal(sd);if err!=nil{return err}
 sum:=sha256.Sum256([]byte(sd.Challenge))
 aad:=[]byte("testagram-staff-webauthn-session-v1:"+userID+":"+kind+":"+base64.RawURLEncoding.EncodeToString(sum[:]))
 nonce:=make([]byte,a.aead.NonceSize());if _,err=rand.Read(nonce);err!=nil{return err}
 encrypted:=append(nonce,a.aead.Seal(nil,nonce,plain,aad)...)
 _,err=a.database.Exec(ctx,"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE expires_at<=now()")
 if err!=nil{return err}
 _,err=a.database.Exec(ctx,"INSERT INTO testagram_auth.staff_webauthn_challenges(challenge_hash,user_id,ceremony,session_data,expires_at) VALUES($1,$2::uuid,$3,$4,LEAST($5::timestamptz,now()+interval '2 minutes'))",sum[:],userID,kind,encrypted,sd.Expires)
 return err
}

func (a *app) handleStaffWebAuthnAuthorize(w http.ResponseWriter,r *http.Request) {
 s,actor,ok:=a.requireStaff(w,r,"owner");if !ok{return}
 if !a.requireRecentStepUp(w,s){return}
 target:=strings.TrimSpace(r.PathValue("id"))
 if target==""||target==actor.ID{writeJSON(w,400,map[string]string{"error":"A different canonical staff identity is required"});return}
 var input struct{Reason string `json:"reason"`}
 if !decodeBody(w,r,&input){return}
 input.Reason=strings.TrimSpace(input.Reason)
 if len(input.Reason)<8||len(input.Reason)>500{writeJSON(w,400,map[string]string{"error":"Provide an enrollment authorization reason of 8 to 500 characters"});return}
 if allowed,err:=a.consumeAdminRateLimit(r.Context(),actor.ID,"webauthn-enrollment",5,time.Hour);err!=nil{writeJSON(w,503,map[string]string{"error":"Enrollment authorization rate-limit unavailable"});return}else if !allowed{writeJSON(w,429,map[string]string{"error":"Enrollment authorization rate limit exceeded"});return}
 if _,err:=a.database.Exec(r.Context(),"UPDATE testagram_auth.staff_webauthn_enrollment_authorizations SET consumed_at=now() WHERE user_id=$1::uuid AND consumed_at IS NULL AND expires_at<=now()",target);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not expire stale enrollment authorizations"});return}
 tag,err:=a.database.Exec(r.Context(),`INSERT INTO testagram_auth.staff_webauthn_enrollment_authorizations(user_id,authorized_by,authorization_method,reason,expires_at)
 SELECT sp.user_id,$2::uuid,'dual_control',$3,now()+interval '30 minutes'
 FROM testagram_auth.staff_principals sp
 JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
 WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE
 AND rg.role IN ('owner','administrator','identity_reviewer','engineer','operations','support','moderator')`,target,actor.ID,input.Reason)
 if err!=nil||tag.RowsAffected()!=1{writeJSON(w,404,map[string]string{"error":"Target is not an enabled, independently provisioned Testagram staff identity"});return}
 writeJSON(w,200,map[string]any{"ok":true,"user_id":target,"expires_in_seconds":1800})
}

func (a *app) handleStaffWebAuthnRegisterBegin(w http.ResponseWriter,r *http.Request){
 s,user,ok:=a.requireStaff(w,r);if !ok{return}
 if allowed,err:=a.consumeAdminRateLimit(r.Context(),user.ID,"webauthn-registration-begin",8,time.Hour);err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn rate-limit service unavailable"});return}else if !allowed{writeJSON(w,429,map[string]string{"error":"WebAuthn registration rate limit exceeded"});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil{writeJSON(w,503,map[string]string{"error":"Canonical staff identity or credential store unavailable"});return}
 if len(u.credentials)>0&&!a.requireRecentStepUp(w,s){return}
 var enrollmentAuthorized bool
 if err:=a.database.QueryRow(r.Context(),"SELECT EXISTS(SELECT 1 FROM testagram_auth.staff_webauthn_enrollment_authorizations WHERE user_id=$1::uuid AND consumed_at IS NULL AND expires_at>now())",user.ID).Scan(&enrollmentAuthorized);err!=nil||!enrollmentAuthorized{writeJSON(w,403,map[string]string{"error":"Staff enrollment requires an unexpired independent authorization."});return}
 if strings.EqualFold(os.Getenv("TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED"),"true")!=true{writeJSON(w,503,map[string]string{"error":"Staff WebAuthn enrollment is closed by production policy."});return}
 options,sd,err:=wa.BeginRegistration(u,webauthn.WithRegistrationOrigin(a.publicOrigin),webauthn.WithExclusions(webauthn.Credentials(u.credentials).CredentialDescriptors()))
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not create registration ceremony"});return}
 if err=a.saveStaffChallenge(r.Context(),user.ID,"registration",sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist registration challenge"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"publicKey":options.Response,"challenge":sd.Challenge})
}
func (a *app) handleStaffWebAuthnRegisterFinish(w http.ResponseWriter,r *http.Request){
 s,user,ok:=a.requireStaff(w,r);if !ok{return}
 var input struct{Challenge string `json:"challenge"`;Response json.RawMessage `json:"response"`}
 if !decodeBody(w,r,&input){return}
 if input.Challenge==""||len(input.Response)==0||len(input.Response)>1<<20{writeJSON(w,400,map[string]string{"error":"Invalid registration response"});return}
 if !strings.EqualFold(os.Getenv("TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED"),"true"){writeJSON(w,503,map[string]string{"error":"Staff WebAuthn enrollment is closed by production policy."});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 tx,err:=a.database.Begin(r.Context());if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential store unavailable"});return};defer tx.Rollback(r.Context())
 sum:=sha256.Sum256([]byte(input.Challenge))
 var raw []byte
 err=tx.QueryRow(r.Context(),"SELECT session_data FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1 AND user_id=$2::uuid AND ceremony='registration' AND expires_at>now() FOR UPDATE",sum[:],user.ID).Scan(&raw)
 if err!=nil{writeJSON(w,401,map[string]string{"error":"Registration challenge expired, invalid or already used"});return}
 var sd webauthn.SessionData
 aad:=[]byte("testagram-staff-webauthn-session-v1:"+user.ID+":registration:"+base64.RawURLEncoding.EncodeToString(sum[:]))
 if len(raw)<a.aead.NonceSize(){writeJSON(w,503,map[string]string{"error":"Stored registration state is invalid"});return}
 plain,e:=a.aead.Open(nil,raw[:a.aead.NonceSize()],raw[a.aead.NonceSize():],aad)
 if e!=nil||json.Unmarshal(plain,&sd)!=nil{writeJSON(w,503,map[string]string{"error":"Stored registration state failed integrity verification"});return}
 var authorizationID int64
 if err=tx.QueryRow(r.Context(),"SELECT id FROM testagram_auth.staff_webauthn_enrollment_authorizations WHERE user_id=$1::uuid AND consumed_at IS NULL AND expires_at>now() ORDER BY created_at LIMIT 1 FOR UPDATE",user.ID).Scan(&authorizationID);err!=nil{writeJSON(w,403,map[string]string{"error":"Enrollment authorization is missing, expired or already consumed"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil{writeJSON(w,403,map[string]string{"error":"Staff enrollment is not authorized"});return}
 if len(u.credentials)>0&&!a.requireRecentStepUp(w,s){return}
 req:=r.Clone(r.Context());req.Body=io.NopCloser(bytes.NewReader(input.Response));req.ContentLength=int64(len(input.Response))
 credential,err:=wa.FinishRegistration(u,sd,req)
 if err!=nil{_,_=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);_ = tx.Commit(r.Context());writeJSON(w,401,map[string]string{"error":"Cryptographic registration verification failed"});return}
 encrypted,err:=a.encryptStaffCredential(credential);if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential encryption failed"});return}
 _,err=tx.Exec(r.Context(),"INSERT INTO testagram_auth.staff_webauthn_credentials(rpid,user_id,credential_id,credential,sign_count,user_verified,backup_eligible,backup_state) VALUES($1,$2::uuid,$3,$4,$5,$6,$7,$8)",rpid,user.ID,credential.ID,encrypted,credential.Authenticator.SignCount,credential.Flags.UserVerified,credential.Flags.BackupEligible,credential.Flags.BackupState)
 if err!=nil{writeJSON(w,409,map[string]string{"error":"Credential already exists or could not be stored"});return}
 tag,err:=tx.Exec(r.Context(),"UPDATE testagram_auth.staff_principals SET mfa_enrolled_at=COALESCE(mfa_enrolled_at,now()) WHERE user_id=$1::uuid AND enabled=TRUE",user.ID)
 if err!=nil||tag.RowsAffected()!=1{writeJSON(w,503,map[string]string{"error":"Could not activate staff MFA enrollment"});return}
 if _,err=tx.Exec(r.Context(),"UPDATE testagram_auth.staff_webauthn_enrollment_authorizations SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL",authorizationID);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not consume enrollment authorization"});return}
 if _,err=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not consume registration challenge"});return}
 if err=tx.Commit(r.Context());err!=nil{writeJSON(w,503,map[string]string{"error":"Could not commit WebAuthn enrollment"});return}
 writeJSON(w,200,map[string]any{"ok":true,"credential_id":base64.RawURLEncoding.EncodeToString(credential.ID)})
}
func (a *app) handleStaffWebAuthnLoginBegin(w http.ResponseWriter,r *http.Request){
 _,user,ok:=a.requireStaff(w,r);if !ok{return}
 if allowed,err:=a.consumeAdminRateLimit(r.Context(),user.ID,"webauthn-assertion-begin",12,time.Hour);err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn rate-limit service unavailable"});return}else if !allowed{writeJSON(w,429,map[string]string{"error":"WebAuthn assertion rate limit exceeded"});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil||len(u.credentials)==0{writeJSON(w,403,map[string]string{"error":"No registered authenticator exists"});return}
 options,sd,err:=wa.BeginLogin(u,webauthn.WithUserVerification(protocol.VerificationRequired),webauthn.WithLoginAuthorizeUVInitialization(true),webauthn.WithLoginOrigin(a.publicOrigin))
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not create assertion ceremony"});return}
 if err=a.saveStaffChallenge(r.Context(),user.ID,"assertion",sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist assertion challenge"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"publicKey":options.Response,"challenge":sd.Challenge})
}
func (a *app) mintStaffElevation(user authUser)(string,int64,error){
 secret:=[]byte(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")));if len(secret)<32{return "",0,errors.New("elevation signing key unavailable")}
 nonce:=make([]byte,16);if _,err:=rand.Read(nonce);err!=nil{return "",0,err}
 now:=time.Now().Unix();exp:=now+5*60
 header,_:=json.Marshal(map[string]string{"alg":"HS256","typ":"JWT"})
 claims,_:=json.Marshal(map[string]any{"iss":"testagram-admin-console","aud":"testagram-admin-sensitive","sub":user.ID,"staff_role":user.StaffRole,"iat":now,"exp":exp,"mfa_at":now,"jti":base64.RawURLEncoding.EncodeToString(nonce)})
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
 var sd webauthn.SessionData
 aad:=[]byte("testagram-staff-webauthn-session-v1:"+user.ID+":assertion:"+base64.RawURLEncoding.EncodeToString(sum[:]))
 if len(raw)<a.aead.NonceSize(){writeJSON(w,503,map[string]string{"error":"Stored assertion state is invalid"});return}
 plain,e:=a.aead.Open(nil,raw[:a.aead.NonceSize()],raw[a.aead.NonceSize():],aad)
 if e!=nil||json.Unmarshal(plain,&sd)!=nil{writeJSON(w,503,map[string]string{"error":"Stored assertion state failed integrity verification"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil||len(u.credentials)==0{writeJSON(w,403,map[string]string{"error":"No registered authenticator exists"});return}
 req:=r.Clone(r.Context());req.Body=io.NopCloser(bytes.NewReader(input.Response));req.ContentLength=int64(len(input.Response))
 credential,err:=wa.FinishLogin(u,sd,req)
 if err!=nil||credential.Authenticator.CloneWarning||!credential.Flags.UserVerified{_,_=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);_ = tx.Commit(r.Context());writeJSON(w,401,map[string]string{"error":"WebAuthn assertion signature, origin, RP ID, user verification or counter validation failed"});return}
 encrypted,err:=a.encryptStaffCredential(credential);if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential update encryption failed"});return}
 tag,err:=tx.Exec(r.Context(),"UPDATE testagram_auth.staff_webauthn_credentials SET credential=$1,sign_count=$2,clone_warning=$3,user_verified=$4,backup_eligible=$5,backup_state=$6,last_used_at=now() WHERE rpid=$7 AND user_id=$8::uuid AND credential_id=$9 AND revoked_at IS NULL AND sign_count<=$2",encrypted,credential.Authenticator.SignCount,credential.Authenticator.CloneWarning,credential.Flags.UserVerified,credential.Flags.BackupEligible,credential.Flags.BackupState,rpid,user.ID,credential.ID)
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential counter update failed"});return}
 if tag.RowsAffected()!=1{_,_=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);_ = tx.Commit(r.Context());writeJSON(w,401,map[string]string{"error":"Credential counter changed or credential was revoked"});return}
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
 _,current,staff:=a.currentUser(w,r);if !staff||current.StaffRole==""{writeJSON(w,401,map[string]string{"error":"Staff authentication required"});return}
 wa,rpid,err:=a.staffWebAuthn();dbOK:=a.database!=nil&&a.database.Ping(ctx)==nil
 schemaOK:=false
 if dbOK{_ = a.database.QueryRow(ctx,"SELECT to_regclass('testagram_auth.staff_webauthn_users') IS NOT NULL AND to_regclass('testagram_auth.staff_webauthn_credentials') IS NOT NULL AND to_regclass('testagram_auth.staff_webauthn_challenges') IS NOT NULL AND to_regclass('testagram_auth.staff_webauthn_enrollment_authorizations') IS NOT NULL").Scan(&schemaOK)}
 privilegesOK:=false
 if dbOK&&schemaOK{_ = a.database.QueryRow(ctx,`SELECT
  has_schema_privilege(current_user,'testagram_auth','USAGE')
  AND has_table_privilege(current_user,'testagram_auth.staff_principals','SELECT')
  AND has_column_privilege(current_user,'testagram_auth.staff_principals','mfa_enrolled_at','UPDATE')
  AND has_table_privilege(current_user,'testagram_auth.identities','SELECT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_users','SELECT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_users','INSERT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_users','UPDATE')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_credentials','SELECT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_credentials','INSERT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_credentials','UPDATE')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_challenges','SELECT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_challenges','INSERT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_challenges','DELETE')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_enrollment_authorizations','SELECT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_enrollment_authorizations','INSERT')
  AND has_table_privilege(current_user,'testagram_auth.staff_webauthn_enrollment_authorizations','UPDATE')`).Scan(&privilegesOK)}
 ready:=err==nil&&wa!=nil&&dbOK&&schemaOK&&privilegesOK&&len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))>=32&&a.aead!=nil&&strings.EqualFold(os.Getenv("TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED"),"true")
 writeJSON(w,200,map[string]any{"ready":ready,"rpid_configured":rpid!="","origins_configured":err==nil,"database":dbOK,"schema":schemaOK,"privileges":privilegesOK,"signing_key":len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))>=32,"credential_encryption":a.aead!=nil,"enrollment_enabled":strings.EqualFold(os.Getenv("TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED"),"true")})
}
