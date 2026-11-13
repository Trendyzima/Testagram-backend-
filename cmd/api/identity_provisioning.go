package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type identityProvisioningToken struct {
	Token string `json:"approval_token"`
	JTI string `json:"jti"`
}

func (s *service) identityApplicationProvisioningToken(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Identity database is unavailable",requestID(r)))
		return
	}
	if !s.cors(w,r) {
		writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",requestID(r)))
		return
	}
	if r.Method!=http.MethodPost {
		writeJSON(w,http.StatusMethodNotAllowed,errorBody("METHOD_NOT_ALLOWED","POST required",requestID(r)))
		return
	}
	applicationID:=strings.TrimSpace(r.PathValue("application_id"))
	applicationToken:=strings.TrimSpace(r.Header.Get("X-Application-Token"))
	if !identityApplicationIDPattern.MatchString(applicationID) || len(applicationToken)<32 || len(applicationToken)>128 || s.db==nil {
		writeJSON(w,http.StatusNotFound,errorBody("APPLICATION_NOT_FOUND","Application was not found",requestID(r)))
		return
	}
	token,err:=authRandomToken()
	if err!=nil {
		writeJSON(w,http.StatusInternalServerError,errorBody("TOKEN_FAILED","Provisioning authorization could not be created",requestID(r)))
		return
	}
	jti,err:=authRandomToken()
	if err!=nil {
		writeJSON(w,http.StatusInternalServerError,errorBody("TOKEN_FAILED","Provisioning authorization could not be created",requestID(r)))
		return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),8*time.Second)
	defer cancel()
	tx,err:=s.db.Begin(ctx)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Provisioning authorization is unavailable",requestID(r)))
		return
	}
	defer func(){ _=tx.Rollback(context.Background()) }()
	var email,status,requestIDValue string
	var storedHash []byte
	err=tx.QueryRow(ctx,`
		SELECT email,status,COALESCE(sensitive_action_request_id::text,''),status_access_token_sha256
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()
		FOR UPDATE
	`,applicationID).Scan(&email,&status,&requestIDValue,&storedHash)
	if err!=nil || subtle.ConstantTimeCompare(storedHash,authTokenHash(applicationToken))!=1 {
		writeJSON(w,http.StatusNotFound,errorBody("APPLICATION_NOT_FOUND","Application was not found",requestID(r)))
		return
	}
	if status!="approved" || requestIDValue=="" {
		writeJSON(w,http.StatusConflict,errorBody("APPLICATION_NOT_APPROVED","Application has not completed two-person account approval",requestID(r)))
		return
	}
	var actionType,requestStatus string
	var requiredApprovals int
	err=tx.QueryRow(ctx,`
		SELECT action_type,status,required_approvals
		FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid FOR UPDATE
	`,requestIDValue).Scan(&actionType,&requestStatus,&requiredApprovals)
	if err!=nil || actionType!="identity.approve" || requestStatus!="approved" || requiredApprovals!=2 {
		writeJSON(w,http.StatusConflict,errorBody("TWO_PERSON_APPROVAL_REQUIRED","Two-person account approval is not complete",requestID(r)))
		return
	}
	var approvalCount,reviewCount int
	if err=tx.QueryRow(ctx,`
		SELECT count(DISTINCT approver_appwrite_user_id)
		FROM testagram_auth.sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
	`,requestIDValue).Scan(&approvalCount); err==nil {
		err=tx.QueryRow(ctx,`
			SELECT count(DISTINCT reviewer_appwrite_user_id)
			FROM testagram_identity.identity_application_reviews
			WHERE application_id=$1::uuid AND decision='approved'
		`,applicationID).Scan(&reviewCount)
	}
	if err!=nil || approvalCount!=2 || reviewCount!=2 {
		writeJSON(w,http.StatusConflict,errorBody("TWO_PERSON_APPROVAL_REQUIRED","Two distinct matching reviewers and approvals are required",requestID(r)))
		return
	}
	var firstApprover,secondApprover string
	err=tx.QueryRow(ctx,`
		SELECT approver_appwrite_user_id
		FROM testagram_auth.sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
		ORDER BY created_at ASC LIMIT 1
	`,requestIDValue).Scan(&firstApprover)
	if err==nil {
		err=tx.QueryRow(ctx,`
			SELECT approver_appwrite_user_id
			FROM testagram_auth.sensitive_action_approvals
			WHERE request_id=$1::uuid AND decision='approved' AND approver_appwrite_user_id<>$2
			ORDER BY created_at ASC LIMIT 1
		`,requestIDValue,firstApprover).Scan(&secondApprover)
	}
	if err!=nil || firstApprover=="" || secondApprover=="" {
		writeJSON(w,http.StatusConflict,errorBody("TWO_PERSON_APPROVAL_REQUIRED","Two distinct approvers are required",requestID(r)))
		return
	}
	var tokenWriteTag pgconn.CommandTag
	tokenWriteTag,err=tx.Exec(ctx,`
		INSERT INTO testagram_identity.identity_approval_tokens
		  (token_sha256,jti_sha256,application_id,first_approver_appwrite_user_id,
		   second_approver_appwrite_user_id,issued_at,expires_at)
		VALUES ($1,$2,$3::uuid,$4,$5,now(),now()+interval '15 minutes')
		ON CONFLICT (application_id) DO UPDATE
		  SET token_sha256=EXCLUDED.token_sha256,jti_sha256=EXCLUDED.jti_sha256,
		      issued_at=now(),expires_at=now()+interval '15 minutes'
		  WHERE testagram_identity.identity_approval_tokens.consumed_at IS NULL
	`,authTokenHash(token),authTokenHash(jti),applicationID,firstApprover,secondApprover)
	if err==nil && tokenWriteTag.RowsAffected()!=1 { err=errors.New("provisioning token already consumed") }
	if err!=nil {
		writeJSON(w,http.StatusConflict,errorBody("PROVISIONING_TOKEN_UNAVAILABLE","A provisioning token could not be issued",requestID(r)))
		return
	}
	if err:=tx.Commit(ctx); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Provisioning authorization could not be committed",requestID(r)))
		return
	}
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusCreated,identityProvisioningToken{Token:token,JTI:jti})
}

func (s *service) identityApplicationRedeem(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Identity database is unavailable",requestID(r)))
		return
	}
	if !s.cors(w,r) {
		writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",requestID(r)))
		return
	}
	if r.Method!=http.MethodPost {
		writeJSON(w,http.StatusMethodNotAllowed,errorBody("METHOD_NOT_ALLOWED","POST required",requestID(r)))
		return
	}
	applicationID:=strings.TrimSpace(r.PathValue("application_id"))
	applicationToken:=strings.TrimSpace(r.Header.Get("X-Application-Token"))
	approvalToken:=strings.TrimSpace(r.Header.Get("X-Identity-Approval-Token"))
	jti:=strings.TrimSpace(r.Header.Get("X-Identity-Approval-JTI"))
	authHeader:=strings.TrimSpace(r.Header.Get("Authorization"))
	if !identityApplicationIDPattern.MatchString(applicationID) || len(applicationToken)<32 || len(applicationToken)>128 ||
		len(approvalToken)<32 || len(approvalToken)>128 || len(jti)<32 || len(jti)>128 ||
		!strings.HasPrefix(strings.ToLower(authHeader),"bearer ") {
		writeJSON(w,http.StatusUnauthorized,errorBody("PROVISIONING_AUTH_REQUIRED","Application, approval and Appwrite credentials are required",requestID(r)))
		return
	}
	jwt:=strings.TrimSpace(authHeader[7:])
	endpoint:=strings.TrimSpace(env("APPWRITE_ENDPOINT",""))
	projectID:=strings.TrimSpace(env("APPWRITE_PROJECT_ID",""))
	if endpoint=="" || projectID=="" {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("APPWRITE_AUTH_UNAVAILABLE","Appwrite authentication is not configured",requestID(r)))
		return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),8*time.Second)
	defer cancel()
	account,status,err:=fetchAppwriteAccount(ctx,s.upstream,endpoint,projectID,jwt)
	if err!=nil {
		writeJSON(w,status,errorBody("APPWRITE_AUTH_FAILED",err.Error(),requestID(r)))
		return
	}
	email,emailErr:=authEmail(account.Email)
	if emailErr!=nil {
		writeJSON(w,http.StatusBadGateway,errorBody("APPWRITE_ACCOUNT_INVALID","Appwrite returned an invalid email address",requestID(r)))
		return
	}
	tx,err:=s.db.Begin(ctx)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Account provisioning is unavailable",requestID(r)))
		return
	}
	defer func(){ _=tx.Rollback(context.Background()) }()
	var applicationEmail,username,displayName,applicationStatus,requestIDValue string
	var applicationHash []byte
	err=tx.QueryRow(ctx,`
		SELECT email,username,display_name,status,COALESCE(sensitive_action_request_id::text,''),
		       status_access_token_sha256
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()
		FOR UPDATE
	`,applicationID).Scan(&applicationEmail,&username,&displayName,&applicationStatus,&requestIDValue,&applicationHash)
	if err!=nil || subtle.ConstantTimeCompare(applicationHash,authTokenHash(applicationToken))!=1 || !strings.EqualFold(applicationEmail,email) {
		writeJSON(w,http.StatusNotFound,errorBody("APPLICATION_NOT_FOUND","Approved application was not found for this Appwrite identity",requestID(r)))
		return
	}
	// A retry after a successful link is idempotent only for the same Appwrite user.
	var existingAppwriteID,existingCanonicalID,existingProvisioningStatus string
	existingErr:=tx.QueryRow(ctx,`
		SELECT appwrite_user_id,canonical_user_id::text,status
		FROM testagram_identity.registration_provisioning
		WHERE application_id=$1::uuid FOR UPDATE
	`,applicationID).Scan(&existingAppwriteID,&existingCanonicalID,&existingProvisioningStatus)
	if existingErr==nil {
		if existingAppwriteID!=account.ID {
			writeJSON(w,http.StatusConflict,errorBody("APPLICATION_ALREADY_LINKED","Application is already linked to another Appwrite account",requestID(r)))
			return
		}
		if err:=tx.Commit(ctx); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Provisioning retry could not be committed",requestID(r)))
			return
		}
		writeJSON(w,http.StatusOK,map[string]any{"status":existingProvisioningStatus,"canonical_user_id":existingCanonicalID,"email_verification_required":!account.EmailVerification})
		return
	}
	if !errors.Is(existingErr,pgx.ErrNoRows) {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Provisioning state could not be read",requestID(r)))
		return
	}
	if applicationStatus!="approved" || requestIDValue=="" {
		writeJSON(w,http.StatusConflict,errorBody("APPLICATION_NOT_APPROVED","Application is not approved for provisioning",requestID(r)))
		return
	}
	var tokenApplicationID string
	var consumedAt *time.Time
	var consumedBy *string
	err=tx.QueryRow(ctx,`
		SELECT application_id::text,consumed_at,consumed_by_appwrite_user_id
		FROM testagram_identity.identity_approval_tokens
		WHERE application_id=$1::uuid AND token_sha256=$2 AND jti_sha256=$3
		  AND expires_at>now()
		FOR UPDATE
	`,applicationID,authTokenHash(approvalToken),authTokenHash(jti)).Scan(&tokenApplicationID,&consumedAt,&consumedBy)
	if err!=nil || consumedAt!=nil || consumedBy!=nil {
		writeJSON(w,http.StatusUnauthorized,errorBody("APPROVAL_TOKEN_INVALID","Provisioning approval token is invalid, expired or already consumed",requestID(r)))
		return
	}
	var requestStatus string
	var approvals int
	err=tx.QueryRow(ctx,`
		SELECT status FROM testagram_auth.sensitive_action_requests
		WHERE id=$1::uuid AND action_type='identity.approve' AND required_approvals=2
	`,requestIDValue).Scan(&requestStatus)
	if err==nil {
		err=tx.QueryRow(ctx,`
			SELECT count(DISTINCT approver_appwrite_user_id)
			FROM testagram_auth.sensitive_action_approvals
			WHERE request_id=$1::uuid AND decision='approved'
		`,requestIDValue).Scan(&approvals)
	}
	if err!=nil || requestStatus!="approved" || approvals!=2 {
		writeJSON(w,http.StatusConflict,errorBody("TWO_PERSON_APPROVAL_REQUIRED","The two-person approval ledger is not complete",requestID(r)))
		return
	}
	var linkedEmailCount int
	err=tx.QueryRow(ctx,`
		SELECT count(*) FROM testagram_identity.appwrite_accounts
		WHERE email=$1 AND appwrite_user_id<>$2
	`,email,account.ID).Scan(&linkedEmailCount)
	if err!=nil || linkedEmailCount>0 {
		writeJSON(w,http.StatusConflict,errorBody("EMAIL_ALREADY_LINKED","This email is already linked to another Testagram identity",requestID(r)))
		return
	}
	var usernameExists bool
	err=tx.QueryRow(ctx,`
		SELECT EXISTS(SELECT 1 FROM testagram_local.accounts WHERE lower(username)=lower($1))
	`,username).Scan(&usernameExists)
	if err!=nil || usernameExists {
		writeJSON(w,http.StatusConflict,errorBody("USERNAME_UNAVAILABLE","The approved username is no longer available",requestID(r)))
		return
	}
	var canonicalID string
	if err=tx.QueryRow(ctx,"SELECT gen_random_uuid()::text").Scan(&canonicalID); err==nil {
		accountStatus:="deactivated"
		provisioningStatus:="linked"
		applicationNextStatus:="provisioning"
		if account.EmailVerification {
			accountStatus="active"
			provisioningStatus="completed"
			applicationNextStatus="provisioned"
		}
		_,err=tx.Exec(ctx,`
			INSERT INTO testagram_local.accounts(user_id,username,display_name,account_status)
			VALUES ($1::uuid,$2,$3,$4)
		`,canonicalID,username,displayName,accountStatus)
		if err==nil {
			_,err=tx.Exec(ctx,`
				INSERT INTO testagram_identity.appwrite_accounts
				  (appwrite_user_id,canonical_user_id,email,email_verified,linked_at,last_seen_at)
				VALUES ($1,$2::uuid,$3,$4,now(),now())
			`,account.ID,canonicalID,email,account.EmailVerification)
		}
		if err==nil {
			idempotencyHash:=authTokenHash(applicationID+":"+account.ID)
			_,err=tx.Exec(ctx,`
				INSERT INTO testagram_identity.registration_provisioning
				  (application_id,idempotency_key_sha256,appwrite_user_id,canonical_user_id,status,attempts)
				VALUES ($1::uuid,$2,$3,$4::uuid,$5,1)
			`,applicationID,idempotencyHash,account.ID,canonicalID,provisioningStatus)
		}
		if err==nil {
			_,err=tx.Exec(ctx,`
				UPDATE testagram_identity.identity_applications
				SET status=$2,updated_at=now()
				WHERE id=$1::uuid AND status='approved'
			`,applicationID,applicationNextStatus)
		}
		if err==nil {
			_,err=tx.Exec(ctx,`
				UPDATE testagram_identity.identity_approval_tokens
				SET consumed_at=now(),consumed_by_appwrite_user_id=$2
				WHERE application_id=$1::uuid AND token_sha256=$3 AND jti_sha256=$4
				  AND consumed_at IS NULL
			`,applicationID,account.ID,authTokenHash(approvalToken),authTokenHash(jti))
		}
	}
	if err!=nil {
		writeJSON(w,http.StatusConflict,errorBody("PROVISIONING_FAILED","Account could not be linked safely; retry or contact support",requestID(r)))
		return
	}
	if err:=tx.Commit(ctx); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("PROVISIONING_COMMIT_FAILED","Account linking could not be committed",requestID(r)))
		return
	}
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusCreated,map[string]any{
		"status":map[bool]string{true:"completed",false:"linked"}[account.EmailVerification],
		"canonical_user_id":canonicalID,
		"email_verification_required":!account.EmailVerification,
		"message":"Appwrite identity is linked. Verify the email address before using Testagram.",
	})
}
