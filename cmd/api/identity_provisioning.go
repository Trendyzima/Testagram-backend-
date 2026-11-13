package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) identityApplicationToken(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.PathValue("application_id"))
	token := strings.TrimSpace(r.Header.Get("X-Application-Token"))
	if !identityApplicationIDPattern.MatchString(id) || len(token) < 32 || len(token) > 128 || s.db == nil {
		return "", pgx.ErrNoRows
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var digest []byte
	err := s.db.QueryRow(ctx, "SELECT status_access_token_sha256 FROM testagram_identity.identity_applications WHERE id=$1::uuid AND status_access_token_expires_at>now()", id).Scan(&digest)
	if err != nil || subtleTokenCompare(digest, authTokenHash(token)) != 1 {
		return "", pgx.ErrNoRows
	}
	return id, nil
}

func (s *service) identityProvisioningToken(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) { writeJSON(w, 403, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r))); return }
	if r.Method != http.MethodPost { writeJSON(w, 405, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r))); return }
	applicationID, err := s.identityApplicationToken(r)
	if err != nil { writeJSON(w, 404, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r))); return }
	token, err := authRandomToken()
	if err != nil { writeJSON(w, 500, errorBody("TOKEN_FAILED", "Provisioning authorization could not be issued", requestID(r))); return }
	jti, err := authRandomToken()
	if err != nil { writeJSON(w, 500, errorBody("TOKEN_FAILED", "Provisioning authorization could not be issued", requestID(r))); return }
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil { writeJSON(w, 503, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning authorization is unavailable", requestID(r))); return }
	defer func() { _ = tx.Rollback(context.Background()) }()
	var status string
	err = tx.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid FOR UPDATE", applicationID).Scan(&status)
	if err != nil || status != "approved" { writeJSON(w, 409, errorBody("APPLICATION_NOT_APPROVED", "Application has not completed both approval stages", requestID(r))); return }
	tag, err := tx.Exec(ctx, "UPDATE testagram_identity.identity_approval_tokens SET token_sha256=$2,jti_sha256=$3,issued_at=now(),expires_at=now()+interval '15 minutes' WHERE application_id=$1::uuid AND consumed_at IS NULL", applicationID, authTokenHash(token), authTokenHash(jti))
	if err != nil || tag.RowsAffected() != 1 { writeJSON(w, 409, errorBody("PROVISIONING_AUTHORIZATION_UNAVAILABLE", "Provisioning authorization is already consumed or unavailable", requestID(r))); return }
	if err := tx.Commit(ctx); err != nil { writeJSON(w, 503, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning authorization could not be committed", requestID(r))); return }
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"status":"approved","approval_token":token,"jti":jti,"expires_in_seconds":900})
}

func (s *service) identityProvisioningRedeem(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) { writeJSON(w, 403, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r))); return }
	if r.Method != http.MethodPost { writeJSON(w, 405, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r))); return }
	applicationID, err := s.identityApplicationToken(r)
	approvalToken := strings.TrimSpace(r.Header.Get("X-Identity-Approval-Token"))
	jti := strings.TrimSpace(r.Header.Get("X-Identity-Approval-JTI"))
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if err != nil || len(approvalToken)<32 || len(approvalToken)>128 || len(jti)<32 || len(jti)>128 || len(auth)<8 || !strings.EqualFold(auth[:7],"Bearer ") {
		writeJSON(w, 404, errorBody("PROVISIONING_AUTHORIZATION_INVALID", "Provisioning authorization was not found", requestID(r))); return
	}
	endpoint, projectID := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	if endpoint=="" || projectID=="" { writeJSON(w,503,errorBody("APPWRITE_NOT_CONFIGURED","Appwrite authentication is unavailable",requestID(r))); return }
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	account, statusCode, err := fetchAppwriteAccount(ctx,s.upstream,endpoint,projectID,strings.TrimSpace(auth[7:]))
	if err!=nil { writeJSON(w,statusCode,errorBody("APPWRITE_SESSION_INVALID","A valid Appwrite session is required to link the approved account",requestID(r))); return }
	if !validAppwriteUserID(account.ID) { writeJSON(w,401,errorBody("APPWRITE_SESSION_INVALID","Appwrite account is invalid",requestID(r))); return }
	tx, err := s.db.Begin(ctx)
	if err!=nil { writeJSON(w,503,errorBody("IDENTITY_STORE_UNAVAILABLE","Account linking is unavailable",requestID(r))); return }
	defer func(){ _=tx.Rollback(context.Background()) }()
	var email, username, displayName, appStatus string
	var storedAppToken []byte
	err=tx.QueryRow(ctx,"SELECT email,username,display_name,status,status_access_token_sha256 FROM testagram_identity.identity_applications WHERE id=$1::uuid AND status_access_token_expires_at>now() FOR UPDATE",applicationID).Scan(&email,&username,&displayName,&appStatus,&storedAppToken)
	if err!=nil || subtleTokenCompare(storedAppToken,authTokenHash(strings.TrimSpace(r.Header.Get("X-Application-Token"))))!=1 { writeJSON(w,404,errorBody("APPLICATION_NOT_FOUND","Application was not found",requestID(r))); return }
	if !strings.EqualFold(strings.TrimSpace(account.Email),strings.TrimSpace(email)) { writeJSON(w,403,errorBody("APPWRITE_EMAIL_MISMATCH","Appwrite account email does not match the approved application",requestID(r))); return }
	var oldAppwrite, oldCanonical, oldStatus string
	oldErr:=tx.QueryRow(ctx,"SELECT appwrite_user_id,canonical_user_id::text,status FROM testagram_identity.registration_provisioning WHERE application_id=$1::uuid",applicationID).Scan(&oldAppwrite,&oldCanonical,&oldStatus)
	if oldErr==nil && oldAppwrite==account.ID && (oldStatus=="linked" || oldStatus=="verification_sent" || oldStatus=="completed") {
		if err:=tx.Commit(ctx); err!=nil { writeJSON(w,503,errorBody("IDENTITY_STORE_UNAVAILABLE","Account link could not be confirmed",requestID(r))); return }
		w.Header().Set("Cache-Control","no-store")
		writeJSON(w,200,map[string]any{"status":"provisioned","canonical_user_id":oldCanonical,"email_verification_required":!account.EmailVerification,"idempotent_replay":true})
		return
	}
	if oldErr!=nil && !errors.Is(oldErr,pgx.ErrNoRows) { writeJSON(w,503,errorBody("IDENTITY_STORE_UNAVAILABLE","Provisioning state could not be read",requestID(r))); return }
	if appStatus!="approved" { writeJSON(w,409,errorBody("APPLICATION_NOT_APPROVED","Application is not awaiting provisioning",requestID(r))); return }
	var consumedAt *time.Time
	err=tx.QueryRow(ctx,"SELECT consumed_at FROM testagram_identity.identity_approval_tokens WHERE token_sha256=$1 AND jti_sha256=$2 AND application_id=$3::uuid AND expires_at>now() FOR UPDATE",authTokenHash(approvalToken),authTokenHash(jti),applicationID).Scan(&consumedAt)
	if err!=nil || consumedAt!=nil { writeJSON(w,409,errorBody("PROVISIONING_AUTHORIZATION_REPLAYED","Provisioning authorization is invalid, expired or already consumed",requestID(r))); return }
	var canonicalID string
	if err:=tx.QueryRow(ctx,"SELECT gen_random_uuid()::text").Scan(&canonicalID); err!=nil { writeJSON(w,503,errorBody("IDENTITY_STORE_UNAVAILABLE","Canonical account could not be allocated",requestID(r))); return }
	_,err=tx.Exec(ctx,"INSERT INTO testagram_local.accounts(user_id,username,display_name) VALUES($1::uuid,$2,$3)",canonicalID,username,displayName)
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_identity.appwrite_accounts(appwrite_user_id,canonical_user_id,email,email_verified,linked_at,last_seen_at,updated_at) VALUES($1,$2::uuid,$3,$4,now(),now(),now())",account.ID,canonicalID,strings.ToLower(strings.TrimSpace(account.Email)),account.EmailVerification) }
	key:=sha256.Sum256([]byte(applicationID+":"+account.ID))
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_identity.registration_provisioning(application_id,idempotency_key_sha256,appwrite_user_id,canonical_user_id,status,attempts,completed_at,updated_at) VALUES($1::uuid,$2,$3,$4::uuid,'completed',1,now(),now())",applicationID,key[:],account.ID,canonicalID) }
	if err==nil { _,err=tx.Exec(ctx,"UPDATE testagram_identity.identity_approval_tokens SET consumed_at=now(),consumed_by_appwrite_user_id=$2 WHERE token_sha256=$1 AND consumed_at IS NULL",authTokenHash(approvalToken),account.ID) }
	nextStatus:="verification_pending"; if account.EmailVerification { nextStatus="verified" }
	if err==nil { _,err=tx.Exec(ctx,"UPDATE testagram_identity.identity_applications SET status=$2,updated_at=now() WHERE id=$1::uuid AND status='approved'",applicationID,nextStatus) }
	if err!=nil { writeJSON(w,409,errorBody("ACCOUNT_PROVISIONING_FAILED","Account could not be linked. The database transaction was rolled back; retry with the same Appwrite account.",requestID(r))); return }
	if err:=tx.Commit(ctx); err!=nil { writeJSON(w,503,errorBody("ACCOUNT_PROVISIONING_UNCERTAIN","Account linking outcome is being reconciled. Retry with the same Appwrite account.",requestID(r))); return }
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,200,map[string]any{"status":"provisioned","canonical_user_id":canonicalID,"email_verification_required":!account.EmailVerification})
}

func subtleTokenCompare(a,b []byte) int {
	if len(a)!=len(b) || len(a)==0 { return 0 }
	return subtle.ConstantTimeCompare(a,b)
}
