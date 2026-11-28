package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"net/http"
	"os"
	"strings"
	"time"

	nativeauth "github.com/Trendyzima/Testagram-backend-/internal/nativeauth"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type nativeElevationClaims struct {
 Subject string `json:"sub"`
 Email string `json:"email"`
 Role string `json:"role"`
 Issuer string `json:"iss"`
 Audience string `json:"aud"`
 IssuedAt int64 `json:"iat"`
 ExpiresAt int64 `json:"exp"`
 TokenVersion int64 `json:"ver"`
 SessionID string `json:"sid"`
 MFAAuthenticatedAt int64 `json:"mfa_at,omitempty"`
 MFAAuthenticatorID string `json:"mfa_credential,omitempty"`
 JWTID string `json:"jti,omitempty"`
}

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

func (a *app) handleStaffWebAuthnAuthorize(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.requireStaff(w, r, "owner")
	if !ok { return }
	claims, ok := a.requireRecentStepUpClaims(w, s)
	if !ok { return }
	if claims.Subject != actor.ID {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "WebAuthn elevation does not belong to the authenticated staff principal"})
		return
	}
	if !strings.EqualFold(os.Getenv("TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED"), "true") {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Staff WebAuthn enrollment is closed by production policy"})
		return
	}
	targetID := strings.TrimSpace(r.PathValue("id"))
	if len(targetID) != 36 || !regexp.MustCompile("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$").MatchString(targetID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A canonical staff UUID is required"})
		return
	}
	if targetID == actor.ID {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "An administrator cannot request enrollment authorization for their own account"})
		return
	}
	var input struct { Reason string `json:"reason"` }
	if !decodeBody(w, r, &input) { return }
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason) < 12 || len(input.Reason) > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A reason of 12 to 1000 characters is required"})
		return
	}
	if allowed, err := a.consumeAdminRateLimit(r.Context(), actor.ID, "webauthn-enrollment-authorization-request", 5, time.Hour); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Security rate-limit service is unavailable"})
		return
	} else if !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many enrollment authorization requests. Try again later."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Staff enrollment approval service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var lockedTarget string
	err = tx.QueryRow(ctx, `
		SELECT sp.user_id::text
		FROM testagram_auth.staff_principals sp
		JOIN testagram_auth.identities i ON i.user_id=sp.user_id
		JOIN testagram_auth.staff_role_grants active_role
		  ON active_role.user_id=sp.user_id AND active_role.revoked_at IS NULL
		WHERE sp.user_id=$1::uuid
		  AND sp.enabled=TRUE
		  AND sp.mfa_enrolled_at IS NULL
		  AND i.disabled_at IS NULL
		  AND i.email_verified_at IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM testagram_auth.staff_webauthn_credentials wc
			WHERE wc.user_id=sp.user_id AND wc.revoked_at IS NULL
		  )
		FOR UPDATE OF sp,i,active_role
	`, targetID).Scan(&lockedTarget)
	if err != nil || lockedTarget != targetID {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Target must be an enabled, email-verified staff principal without an enrolled authenticator"})
		return
	}
	var existing bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM testagram_auth.native_sensitive_action_requests
			WHERE action_type='staff.webauthn.enroll'
			  AND target_reference=$1 AND status='pending' AND expires_at>now()
		) OR EXISTS (
			SELECT 1 FROM testagram_auth.staff_webauthn_enrollment_authorizations
			WHERE user_id=$1::uuid AND consumed_at IS NULL
			  AND revoked_at IS NULL AND expires_at>now()
		)
	`, targetID).Scan(&existing)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not check existing enrollment approvals"})
		return
	}
	if existing {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "A pending request or live enrollment authorization already exists for this staff member"})
		return
	}
	payload, err := json.Marshal(struct { TargetUserID string `json:"target_user_id"` }{TargetUserID: targetID})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not prepare the immutable enrollment request"})
		return
	}
	digest := sha256.Sum256(payload)
	var requestID string
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES ('staff.webauthn.enroll',$1::uuid,$2,$3,$4::jsonb,$5,2)
		RETURNING id::text
	`, actor.ID, targetID, input.Reason, string(payload), fmt.Sprintf("%x", digest[:])).Scan(&requestID)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_auth.native_admin_security_audit
			  (actor_user_id,action,target_reference,request_id,metadata)
			VALUES ($1::uuid,'staff.webauthn.enrollment_requested',$2,$3::uuid,
			        jsonb_build_object('required_approvals',2,'requester',$1::text))
		`, actor.ID, targetID, requestID)
	}
	if err == nil { err = tx.Commit(ctx) }
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not safely record the two-person enrollment request"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "request_id": requestID, "status": "pending", "required_approvals": 2,
		"message": "Request recorded. Two different eligible staff members must approve with WebAuthn. No enrollment authorization exists until both approvals are committed.",
	})
}

func (a *app) handleStaffWebAuthnAuthorizationRevoke(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.requireStaff(w, r, "owner")
	if !ok { return }
	claims, ok := a.requireRecentStepUpClaims(w, s)
	if !ok { return }
	if claims.Subject != actor.ID {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "WebAuthn elevation does not belong to the authenticated staff principal"})
		return
	}
	targetID := strings.TrimSpace(r.PathValue("id"))
	if len(targetID) != 36 || !regexp.MustCompile("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$").MatchString(targetID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A canonical staff UUID is required"})
		return
	}
	var input struct { Reason string `json:"reason"` }
	if !decodeBody(w, r, &input) { return }
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason) < 12 || len(input.Reason) > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A revocation reason of 12 to 1000 characters is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Enrollment authorization service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
		UPDATE testagram_auth.staff_webauthn_enrollment_authorizations
		SET revoked_at=now()
		WHERE user_id=$1::uuid AND request_id IS NOT NULL
		  AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>now()
		RETURNING id,request_id::text
	`, targetID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not revoke the enrollment authorization"})
		return
	}
	if !rows.Next() {
		rows.Close()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "No live, unused dual-control enrollment authorization exists for this staff member"})
		return
	}
	var authorizationID int64
	var requestID string
	if err := rows.Scan(&authorizationID, &requestID); err != nil {
		rows.Close()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not read the revoked authorization"})
		return
	}
	if rows.Next() {
		rows.Close()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Multiple live enrollment authorizations violate the uniqueness invariant"})
		return
	}
	if rows.Err() != nil {
		rows.Close()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not verify enrollment authorization uniqueness"})
		return
	}
	rows.Close()
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_auth.native_staff_lifecycle_events
		  (request_id,actor_user_id,target_user_id,action,metadata)
		VALUES ($1::uuid,$2::uuid,$3::uuid,'webauthn_enrollment_revoked',
		        jsonb_build_object('authorization_id',$4::bigint,'reason',$5::text))
	`, requestID, actor.ID, targetID, authorizationID, input.Reason)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_auth.native_admin_security_audit
			  (actor_user_id,action,target_reference,request_id,metadata)
			VALUES ($1::uuid,'staff.webauthn.enrollment_revoked',$2,$3::uuid,
			        jsonb_build_object('authorization_id',$4::bigint,'reason',$5::text))
		`, actor.ID, targetID, requestID, authorizationID, input.Reason)
	}
	if err == nil { err = tx.Commit(ctx) }
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Revocation audit could not be committed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true, "authorization_id": authorizationID})
}

func (a *app) handleStaffWebAuthnRegisterBegin(w http.ResponseWriter,r *http.Request){
 s,user,ok:=a.requireStaff(w,r);if !ok{return}
 if !a.requireStaffWebAuthnRuntime(w,r,true){return}
 if allowed,err:=a.consumeAdminRateLimit(r.Context(),user.ID,"webauthn-registration-begin",8,time.Hour);err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn rate-limit service unavailable"});return}else if !allowed{writeJSON(w,429,map[string]string{"error":"WebAuthn registration rate limit exceeded"});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil{writeJSON(w,503,map[string]string{"error":"Canonical staff identity or credential store unavailable"});return}
 if len(u.credentials)>0&&!a.requireRecentStepUp(w,s){return}
 var enrollmentAuthorized bool
 if err:=a.database.QueryRow(r.Context(),`SELECT EXISTS(
		SELECT 1 FROM testagram_auth.staff_webauthn_enrollment_authorizations ea
		WHERE ea.user_id=$1::uuid AND ea.consumed_at IS NULL
		  AND ea.revoked_at IS NULL AND ea.expires_at>now()
		  AND ea.request_id IS NOT NULL
		  AND testagram_auth.staff_webauthn_enrollment_authorization_is_valid(ea.id,ea.user_id,$2)
	)`,user.ID,rpid).Scan(&enrollmentAuthorized);err!=nil||!enrollmentAuthorized{writeJSON(w,403,map[string]string{"error":"Staff enrollment requires an unexpired, unconsumed two-person authorization with both approvers still eligible."});return}
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
 if !a.requireStaffWebAuthnRuntime(w,r,true){return}
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
 if err=tx.QueryRow(r.Context(),`SELECT ea.id
		FROM testagram_auth.staff_webauthn_enrollment_authorizations ea
		WHERE ea.user_id=$1::uuid AND ea.consumed_at IS NULL
		  AND ea.revoked_at IS NULL AND ea.expires_at>now()
		  AND ea.request_id IS NOT NULL
		  AND testagram_auth.staff_webauthn_enrollment_authorization_is_valid(ea.id,ea.user_id,$2)
		ORDER BY ea.created_at LIMIT 1 FOR UPDATE OF ea`,user.ID,rpid).Scan(&authorizationID);err!=nil{writeJSON(w,403,map[string]string{"error":"Two-person enrollment authorization is missing, expired, revoked, already consumed or has an ineligible approver"});return}
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
 if tag,consumeErr:=tx.Exec(r.Context(),`UPDATE testagram_auth.staff_webauthn_enrollment_authorizations
		SET consumed_at=now()
		WHERE id=$1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>now()`,authorizationID);consumeErr!=nil||tag.RowsAffected()!=1{writeJSON(w,503,map[string]string{"error":"Enrollment authorization could not be consumed exactly once"});return}
 if _,err=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not consume registration challenge"});return}
 if err=tx.Commit(r.Context());err!=nil{writeJSON(w,503,map[string]string{"error":"Could not commit WebAuthn enrollment"});return}
 writeJSON(w,200,map[string]any{"ok":true,"credential_id":base64.RawURLEncoding.EncodeToString(credential.ID)})
}
func (a *app) handleStaffWebAuthnLoginBegin(w http.ResponseWriter,r *http.Request){
 _,user,ok:=a.requireStaff(w,r);if !ok{return}
 if !a.requireStaffWebAuthnRuntime(w,r,false){return}
 if allowed,err:=a.consumeAdminRateLimit(r.Context(),user.ID,"webauthn-assertion-begin",12,time.Hour);err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn rate-limit service unavailable"});return}else if !allowed{writeJSON(w,429,map[string]string{"error":"WebAuthn assertion rate limit exceeded"});return}
 wa,rpid,err:=a.staffWebAuthn();if err!=nil{writeJSON(w,503,map[string]string{"error":"WebAuthn RP configuration is incomplete"});return}
 u,err:=a.loadStaffWebAuthnUser(r.Context(),user.ID,rpid);if err!=nil||len(u.credentials)==0{writeJSON(w,403,map[string]string{"error":"No registered authenticator exists"});return}
 options,sd,err:=wa.BeginLogin(u,webauthn.WithUserVerification(protocol.VerificationRequired),webauthn.WithLoginAuthorizeUVInitialization(true),webauthn.WithLoginOrigin(a.publicOrigin))
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not create assertion ceremony"});return}
 if err=a.saveStaffChallenge(r.Context(),user.ID,"assertion",sd);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist assertion challenge"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"publicKey":options.Response,"challenge":sd.Challenge})
}
func (a *app) mintStaffElevation(user authUser, accessToken string, authenticatorID []byte) (string, int64, error) {
	if user.ID == "" {
		return "", 0, errors.New("native staff identity is unavailable")
	}
	secret := []byte(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))
	return nativeauth.MintStaffElevationToken(accessToken, user.ID, authenticatorID, secret, time.Now().UTC())
}

func (a *app) handleStaffWebAuthnLoginFinish(w http.ResponseWriter,r *http.Request){
 s,user,ok:=a.requireStaff(w,r);if !ok{return}
 if !a.requireStaffWebAuthnRuntime(w,r,false){return}
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
 tag,err:=tx.Exec(r.Context(),"UPDATE testagram_auth.staff_webauthn_credentials SET credential=$1,sign_count=$2,clone_warning=$3,user_verified=$4,backup_eligible=$5,backup_state=$6,last_used_at=now() WHERE rpid=$7 AND user_id=$8::uuid AND credential_id=$9 AND revoked_at IS NULL AND (sign_count<$2 OR (sign_count=0 AND $2=0))",encrypted,credential.Authenticator.SignCount,credential.Authenticator.CloneWarning,credential.Flags.UserVerified,credential.Flags.BackupEligible,credential.Flags.BackupState,rpid,user.ID,credential.ID)
 if err!=nil{writeJSON(w,503,map[string]string{"error":"Credential counter update failed"});return}
 if tag.RowsAffected()!=1{_,_=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);_ = tx.Commit(r.Context());writeJSON(w,401,map[string]string{"error":"Credential counter changed or credential was revoked"});return}
 if _,err=tx.Exec(r.Context(),"DELETE FROM testagram_auth.staff_webauthn_challenges WHERE challenge_hash=$1",sum[:]);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not consume assertion challenge"});return}
 if err=tx.Commit(r.Context());err!=nil{writeJSON(w,503,map[string]string{"error":"Could not commit assertion"});return}
 token,exp,err:=a.mintStaffElevation(user,s.AccessToken,credential.ID);if err!=nil{writeJSON(w,503,map[string]string{"error":"Could not issue backend MFA elevation"});return}
 s.ElevatedToken=token
 if err=a.setSession(w,s);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not persist elevated session"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,map[string]any{"ok":true,"elevated":true,"expires_at":exp})
}
func (a *app) staffWebAuthnCredentialsDecryptable(ctx context.Context, rpid string) bool {
	if a.database == nil || a.aead == nil || rpid == "" {
		return false
	}
	rows, err := a.database.Query(ctx, `
		SELECT credential FROM testagram_auth.staff_webauthn_credentials
		WHERE rpid=$1 AND revoked_at IS NULL
		ORDER BY user_id,id
	`, rpid)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var encrypted []byte
		if err := rows.Scan(&encrypted); err != nil || len(encrypted) < a.aead.NonceSize() {
			return false
		}
		plain, err := a.aead.Open(nil, encrypted[:a.aead.NonceSize()], encrypted[a.aead.NonceSize():], []byte("testagram-staff-webauthn-credential-v1"))
		if err != nil {
			return false
		}
		var credential webauthn.Credential
		if err := json.Unmarshal(plain, &credential); err != nil || len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
			return false
		}
	}
	return rows.Err() == nil
}

func (a *app) staffWebAuthnReadiness(ctx context.Context) map[string]any {
 wa,rpid,configErr:=a.staffWebAuthn()
 dbOK:=a.database!=nil&&a.database.Ping(ctx)==nil
 schemaOK:=false
 if dbOK {
  _=a.database.QueryRow(ctx,`SELECT
   to_regclass('testagram_auth.staff_principals') IS NOT NULL
   AND to_regclass('testagram_auth.staff_role_grants') IS NOT NULL
   AND to_regclass('testagram_auth.identities') IS NOT NULL
   AND to_regclass('testagram_auth.admin_rate_limits') IS NOT NULL
   AND to_regclass('testagram_auth.admin_permissions') IS NOT NULL
   AND to_regclass('testagram_auth.staff_role_permissions') IS NOT NULL
   AND to_regclass('testagram_auth.native_staff_permission_overrides') IS NOT NULL
   AND to_regclass('testagram_auth.staff_webauthn_users') IS NOT NULL
   AND to_regclass('testagram_auth.staff_webauthn_credentials') IS NOT NULL
   AND to_regclass('testagram_auth.staff_webauthn_challenges') IS NOT NULL
   AND to_regclass('testagram_auth.staff_webauthn_enrollment_authorizations') IS NOT NULL
   AND to_regclass('testagram_auth.native_sensitive_action_requests') IS NOT NULL
   AND to_regclass('testagram_auth.native_sensitive_action_approvals') IS NOT NULL
   AND to_regclass('testagram_auth.native_staff_lifecycle_events') IS NOT NULL
   AND to_regprocedure('testagram_auth.staff_webauthn_enrollment_authorization_is_valid(bigint,uuid,text)') IS NOT NULL
   AND (SELECT count(*)=4 FROM information_schema.columns
        WHERE table_schema='testagram_auth'
          AND table_name='staff_webauthn_enrollment_authorizations'
          AND column_name IN ('request_id','first_approver_user_id','second_approver_user_id','revoked_at'))
   AND EXISTS (SELECT 1 FROM pg_trigger
        WHERE tgrelid=to_regclass('testagram_auth.native_sensitive_action_approvals')
          AND tgname='native_staff_webauthn_enrollment_approver_permission'
          AND NOT tgisinternal)`).Scan(&schemaOK)
 }
 privilegesOK:=false
 if dbOK&&schemaOK {
  _=a.database.QueryRow(ctx,`SELECT
   has_schema_privilege(current_user,'testagram_auth','USAGE')
   AND has_table_privilege(current_user,'testagram_auth.staff_principals','SELECT')
   AND has_column_privilege(current_user,'testagram_auth.staff_principals','mfa_enrolled_at','UPDATE')
   AND has_table_privilege(current_user,'testagram_auth.staff_role_grants','SELECT')
   AND has_table_privilege(current_user,'testagram_auth.identities','SELECT')
   AND has_table_privilege(current_user,'testagram_auth.admin_rate_limits','SELECT')
   AND has_table_privilege(current_user,'testagram_auth.admin_rate_limits','INSERT')
   AND has_table_privilege(current_user,'testagram_auth.admin_rate_limits','UPDATE')
   AND has_table_privilege(current_user,'testagram_auth.admin_permissions','SELECT')
   AND has_table_privilege(current_user,'testagram_auth.staff_role_permissions','SELECT')
   AND has_table_privilege(current_user,'testagram_auth.native_staff_permission_overrides','SELECT')
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
   AND has_column_privilege(current_user,'testagram_auth.staff_webauthn_enrollment_authorizations','consumed_at','UPDATE')
   AND has_column_privilege(current_user,'testagram_auth.staff_webauthn_enrollment_authorizations','revoked_at','UPDATE')`).Scan(&privilegesOK)
 }
 credentialedStaff:=0
 credentialedReviewers:=0
 credentialedApprovers:=0
 if dbOK&&schemaOK&&privilegesOK&&rpid!="" {
  _=a.database.QueryRow(ctx,`WITH eligible_staff AS (
   SELECT sp.user_id,
          bool_or(rg.role='identity_reviewer') AS is_reviewer,
          bool_or(rg.role IN ('owner','administrator')) AS is_approver
   FROM testagram_auth.staff_principals sp
   JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
   JOIN testagram_auth.identities i ON i.user_id=sp.user_id
   JOIN testagram_auth.staff_webauthn_credentials wc
     ON wc.user_id=sp.user_id AND wc.rpid=$1 AND wc.revoked_at IS NULL
      AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
   WHERE sp.enabled=TRUE AND sp.mfa_enrolled_at IS NOT NULL
     AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
     AND rg.role IN ('owner','administrator','identity_reviewer')
   GROUP BY sp.user_id
  )
  SELECT count(*) FILTER (WHERE is_reviewer OR is_approver),
         count(*) FILTER (WHERE is_reviewer AND NOT is_approver),
         count(*) FILTER (WHERE is_approver AND NOT is_reviewer)
  FROM eligible_staff
  `,rpid).Scan(&credentialedStaff,&credentialedReviewers,&credentialedApprovers)
 }
 signingKeyOK:=len(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))>=32
 encryptionOK:=a.aead!=nil&&a.staffWebAuthnCredentialsDecryptable(ctx,rpid)
 enrollmentEnabled:=strings.EqualFold(os.Getenv("TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED"),"true")
 origins:=strings.FieldsFunc(os.Getenv("TESTAGRAM_WEBAUTHN_ORIGINS"),func(r rune)bool{return r==','||r==' '||r=='\n'})
 rpFingerprint:=nativeauth.WebAuthnConfigurationFingerprint(rpid,origins)
 backendAPIReady:=false
 if a.apiHealthURL!=""&&a.client!=nil {
  req,err:=http.NewRequestWithContext(ctx,http.MethodGet,a.apiHealthURL,nil)
  if err==nil {
   client:=*a.client;client.Timeout=2*time.Second
   client.CheckRedirect=func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}
   if resp,e:=client.Do(req);e==nil {
    backendAPIReady=resp.StatusCode==http.StatusOK
    _,_=io.Copy(io.Discard,resp.Body);_ = resp.Body.Close()
   }
  }
 }
 originsOK:=configErr==nil&&wa!=nil&&rpid!=""
 ceremoniesReady:=originsOK&&dbOK&&schemaOK&&privilegesOK&&signingKeyOK&&encryptionOK&&backendAPIReady
 return map[string]any{
  "ready":ceremoniesReady&&enrollmentEnabled,
  "ceremonies_ready":ceremoniesReady,
  "rpid_configured":rpid!="",
  "rp_configuration_fingerprint":rpFingerprint,
  "origins_configured":originsOK,
  "database":dbOK,"schema":schemaOK,"privileges":privilegesOK,
  "signing_key":signingKeyOK,"credential_encryption":encryptionOK,
  "backend_api":backendAPIReady,"enrollment_enabled":enrollmentEnabled,
  "credentialed_staff":credentialedStaff,
   "credentialed_reviewers":credentialedReviewers,
  "credentialed_approvers":credentialedApprovers,
  "identity_review_ready":ceremoniesReady&&credentialedStaff>=4&&credentialedReviewers>=2&&credentialedApprovers>=2,
 }
}

func (a *app) requireStaffWebAuthnRuntime(w http.ResponseWriter,r *http.Request,registration bool) bool {
 ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second);defer cancel()
 state:=a.staffWebAuthnReadiness(ctx)
 key:="ceremonies_ready";if registration{key="ready"}
 if ready,_:=state[key].(bool);!ready{
  writeJSON(w,503,map[string]string{"error":"WebAuthn is fail-closed because one or more runtime prerequisites are unavailable."})
  return false
 }
 return true
}

func (a *app) handleInternalWebAuthnReadiness(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimSpace(os.Getenv("TESTAGRAM_WEBAUTHN_READINESS_TOKEN"))
	if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Internal WebAuthn readiness authentication required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a.staffWebAuthnReadiness(ctx))
}

func (a *app) handleWebAuthnReadiness(w http.ResponseWriter,r *http.Request){
 ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second);defer cancel()
 _,current,staff:=a.currentUser(w,r);if !staff||current.StaffRole==""{writeJSON(w,401,map[string]string{"error":"Staff authentication required"});return}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,200,a.staffWebAuthnReadiness(ctx))
}
