package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

)

type identityReviewDecisionInput struct {
	Decision string `json:"decision"`
	ReasonCode string `json:"reason_code"`
}

type identityApprovalInput struct {
	Reason string `json:"reason"`
}

type identityReviewSnapshot struct {
	ApplicationID string `json:"application_id"`
	Email string `json:"email"`
	Username string `json:"username"`
	DisplayName string `json:"display_name"`
	EvidenceFingerprintSHA256 string `json:"evidence_fingerprint_sha256"`
	EvidenceAuditReference string `json:"evidence_audit_reference"`
	TermsVersion string `json:"terms_version"`
	PrivacyVersion string `json:"privacy_version"`
}

func (s *service) identityReviewer(r *http.Request) (appwriteAccount, int, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
		return appwriteAccount{}, http.StatusUnauthorized, errors.New("staff authentication required")
	}
	jwt := strings.TrimSpace(header[7:])
	endpoint := strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT"))
	projectID := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	if endpoint == "" || projectID == "" {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("Appwrite authentication is not configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	account, status, err := fetchAppwriteAccount(ctx, s.upstream, endpoint, projectID, jwt)
	if err != nil {
		return appwriteAccount{}, status, err
	}
	if !account.EmailVerification {
		return appwriteAccount{}, http.StatusForbidden, errors.New("verified Appwrite email is required")
	}
	var allowed bool
	err = s.db.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM testagram_auth.staff_roles sr
		  LEFT JOIN testagram_auth.staff_role_permissions rp
		    ON rp.role=sr.role AND rp.permission_key='identity.review'
		  LEFT JOIN testagram_auth.staff_permission_overrides po
		    ON po.appwrite_user_id=sr.appwrite_user_id AND po.permission_key='identity.review'
		  WHERE sr.appwrite_user_id=$1 AND sr.access_revoked_at IS NULL
		    AND (po.granted IS TRUE OR (po.granted IS NULL AND rp.permission_key IS NOT NULL))
		)
	`, account.ID).Scan(&allowed)
	if err != nil {
		return appwriteAccount{}, http.StatusServiceUnavailable, errors.New("staff authorization is unavailable")
	}
	if !allowed {
		return appwriteAccount{}, http.StatusForbidden, errors.New("identity review permission is required")
	}
	return account, http.StatusOK, nil
}

func (s *service) identityReviewQueue(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET required", requestID(r)))
		return
	}
	reviewer, status, err := s.identityReviewer(r)
	if err != nil {
		writeJSON(w, status, errorBody("STAFF_ACCESS_DENIED", err.Error(), requestID(r)))
		return
	}
	if s.virtualStorage == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Private evidence storage is unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT v.id::text,a.id::text,a.email,a.username,a.display_name,
		       e.id::text,e.object_key,e.content_type,e.size_bytes,v.created_at,v.expires_at
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		JOIN testagram_identity.verification_evidence e ON e.session_id=v.id AND e.status='verified'
		WHERE v.status='manual_review' AND v.expires_at>now()
		ORDER BY v.created_at ASC
		LIMIT 100
	`)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review queue is unavailable", requestID(r)))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var sessionID, applicationID, email, username, displayName, evidenceID, objectKey, contentType string
		var size int64
		var createdAt, expiresAt time.Time
		if err := rows.Scan(&sessionID,&applicationID,&email,&username,&displayName,&evidenceID,&objectKey,&contentType,&size,&createdAt,&expiresAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review queue could not be read", requestID(r)))
			return
		}
		downloadURL, err := s.virtualStorage.DownloadURL(ctx, applicationID, applicationID, identityEvidenceNamespace, objectKey)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Evidence access could not be authorized", requestID(r)))
			return
		}
		items = append(items, map[string]any{
			"session_id":sessionID,"application_id":applicationID,"email":email,
			"username":username,"display_name":displayName,"evidence_id":evidenceID,
			"evidence_url":downloadURL,"content_type":contentType,"size_bytes":size,
			"created_at":createdAt.UTC().Format(time.RFC3339),"expires_at":expiresAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review queue could not be read", requestID(r)))
		return
	}
	_ = reviewer
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items":items,"count":len(items)})
}

func (s *service) identityReviewDecision(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	reviewer, status, err := s.identityReviewer(r)
	if err != nil {
		writeJSON(w, status, errorBody("STAFF_ACCESS_DENIED", err.Error(), requestID(r)))
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if !identityApplicationIDPattern.MatchString(sessionID) {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input identityReviewDecisionInput
	if err := decodeSingleJSON(r.Body,&input); err != nil ||
		(input.Decision!="verified" && input.Decision!="failed" && input.Decision!="escalate") ||
		len(strings.TrimSpace(input.ReasonCode))<2 || len(input.ReasonCode)>64 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REVIEW_DECISION", "Decision or reason code is invalid", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review decision could not be recorded", requestID(r)))
		return
	}
	defer func(){ _ = tx.Rollback(context.Background()) }()
	var applicationID, applicantEmail, evidenceRef, termsVersion, privacyVersion, username, displayName string
	var fingerprint []byte
	var currentStatus string
	err = tx.QueryRow(ctx, `
		SELECT a.id::text,a.email,a.username,a.display_name,a.terms_version,a.privacy_version,
		       a.evidence_reference,a.evidence_fingerprint_sha256,v.status
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		JOIN testagram_identity.verification_evidence e ON e.session_id=v.id AND e.status='verified'
		WHERE v.id=$1::uuid AND v.expires_at>now()
		FOR UPDATE OF v,a
	`,sessionID).Scan(&applicationID,&applicantEmail,&username,&displayName,&termsVersion,&privacyVersion,&evidenceRef,&fingerprint,&currentStatus)
	if err!=nil || currentStatus!="manual_review" {
		writeJSON(w,http.StatusConflict,errorBody("SESSION_NOT_REVIEWABLE","Verification session is not available for review",requestID(r)))
		return
	}
	if strings.EqualFold(strings.TrimSpace(applicantEmail),strings.TrimSpace(reviewer.Email)) {
		writeJSON(w,http.StatusForbidden,errorBody("SELF_REVIEW_FORBIDDEN","Applicants cannot review their own applications",requestID(r)))
		return
	}
	if _,err=tx.Exec(ctx,`
		INSERT INTO testagram_identity.verification_reviews(session_id,reviewer_appwrite_user_id,decision,reason_code,evidence_reference)
		VALUES ($1::uuid,$2,$3,$4,$5)
	`,sessionID,reviewer.ID,input.Decision,input.ReasonCode,evidenceRef); err!=nil {
		writeJSON(w,http.StatusConflict,errorBody("REVIEW_ALREADY_RECORDED","A reviewer may record only one decision per session",requestID(r)))
		return
	}
	if _,err=tx.Exec(ctx,`
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES ($1::uuid,$2,'reviewer',$3,$4,jsonb_build_object('decision',$5,'reason_code',$6))
	`,sessionID,map[string]string{"verified":"decision_verified","failed":"decision_failed","escalate":"decision_escalated"}[input.Decision],reviewer.ID,requestID(r),input.Decision,input.ReasonCode); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_AUDIT_FAILED","Review audit could not be recorded",requestID(r)))
		return
	}
	if input.Decision=="escalate" {
		if err:=tx.Commit(ctx); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Review decision could not be committed",requestID(r)))
			return
		}
		writeJSON(w,http.StatusAccepted,map[string]any{"status":"manual_review","message":"Application escalated for additional review."})
		return
	}
	var matchingCount int
	err=tx.QueryRow(ctx,`
		SELECT count(*) FROM testagram_identity.verification_reviews
		WHERE session_id=$1::uuid AND decision=$2
	`,sessionID,input.Decision).Scan(&matchingCount)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Review decision could not be evaluated",requestID(r)))
		return
	}
	if matchingCount<2 {
		if err:=tx.Commit(ctx); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Review decision could not be committed",requestID(r)))
			return
		}
		writeJSON(w,http.StatusAccepted,map[string]any{"status":"manual_review","review_count":1,"required_matching_reviews":2})
		return
	}
	if input.Decision=="failed" {
		_,err=tx.Exec(ctx,`
			UPDATE testagram_identity.verification_sessions
			SET status='failed',verified_age_over_threshold=FALSE,assurance_level='none',
			    decided_at=now(),decided_by_appwrite_user_id=$2,decision_reason_code=$3,updated_at=now()
			WHERE id=$1::uuid
		`,sessionID,reviewer.ID,input.ReasonCode)
		if err==nil {
			_,err=tx.Exec(ctx,`
				UPDATE testagram_identity.identity_applications
				SET adult_verification_result='failed',status='rejected',updated_at=now()
				WHERE id=$1::uuid
			`,applicationID)
		}
		if err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Failed decision could not be committed",requestID(r)))
			return
		}
		if err:=tx.Commit(ctx); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Failed decision could not be committed",requestID(r)))
			return
		}
		writeJSON(w,http.StatusOK,map[string]any{"status":"failed","application_status":"rejected"})
		return
	}

	// Two matching, distinct reviewers are required before an application can
	// enter the existing two-person sensitive-action approval ledger.
	rows,err:=tx.Query(ctx,`
		SELECT reviewer_appwrite_user_id,reason_code
		FROM testagram_identity.verification_reviews
		WHERE session_id=$1::uuid AND decision='verified'
		ORDER BY created_at ASC LIMIT 2
	`,sessionID)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Reviewer records could not be loaded",requestID(r)))
		return
	}
	type reviewerRecord struct{ id,reason string }
	approvedReviewers:=make([]reviewerRecord,0,2)
	for rows.Next(){ var v reviewerRecord; if err:=rows.Scan(&v.id,&v.reason); err!=nil { rows.Close(); writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Reviewer records could not be loaded",requestID(r))); return }; approvedReviewers=append(approvedReviewers,v) }
	rows.Close()
	if len(approvedReviewers)!=2 || approvedReviewers[0].id==approvedReviewers[1].id {
		writeJSON(w,http.StatusConflict,errorBody("TWO_REVIEWERS_REQUIRED","Two independent matching reviews are required",requestID(r)))
		return
	}
	snapshot:=identityReviewSnapshot{
		ApplicationID:applicationID,Email:applicantEmail,Username:username,DisplayName:displayName,
		EvidenceFingerprintSHA256:hex.EncodeToString(fingerprint),EvidenceAuditReference:evidenceRef,
		TermsVersion:termsVersion,PrivacyVersion:privacyVersion,
	}
	payloadBytes,err:=json.Marshal(snapshot)
	if err!=nil {
		writeJSON(w,http.StatusInternalServerError,errorBody("IDENTITY_SNAPSHOT_FAILED","Application snapshot could not be created",requestID(r)))
		return
	}
	payloadHash:=sha256.Sum256(payloadBytes)
	var approvalRequestID string
	err=tx.QueryRow(ctx,`
		INSERT INTO testagram_auth.sensitive_action_requests
		  (action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES ('identity.approve','testagram-identity-service',$1,
		        'Verified identity application requires independent two-person account approval',
		        $2::jsonb,$3,2)
		RETURNING id::text
	`,applicationID,string(payloadBytes),hex.EncodeToString(payloadHash[:])).Scan(&approvalRequestID)
	if err==nil {
		for _,v:=range approvedReviewers {
			_,err=tx.Exec(ctx,`
				INSERT INTO testagram_identity.identity_application_reviews
				  (application_id,reviewer_appwrite_user_id,decision,reason,evidence_audit_reference)
				VALUES ($1::uuid,$2,'approved',$3,$4)
			`,applicationID,v.id,"Independent evidence review: "+v.reason,evidenceRef)
			if err!=nil { break }
		}
	}
	if err==nil {
		_,err=tx.Exec(ctx,`
			UPDATE testagram_identity.identity_applications
			SET adult_verified_at=now(),adult_verification_provider='testagram_manual',
			    adult_verification_reference=$2,adult_verification_checked_at=now(),
			    adult_verification_webhook_received_at=now(),adult_verification_result='verified',
			    status='verified',sensitive_action_request_id=$3::uuid,updated_at=now()
			WHERE id=$1::uuid
		`,applicationID,evidenceRef,approvalRequestID)
	}
	if err==nil {
		_,err=tx.Exec(ctx,`
			UPDATE testagram_identity.verification_sessions
			SET status='verified',verified_age_over_threshold=TRUE,assurance_level='reviewed',
			    decided_at=now(),decided_by_appwrite_user_id=$2,decision_reason_code=$3,updated_at=now()
			WHERE id=$1::uuid
		`,sessionID,reviewer.ID,input.ReasonCode)
	}
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_LINK_FAILED","Verified review could not be linked to the approval workflow",requestID(r)))
		return
	}
	if err:=tx.Commit(ctx); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_LINK_FAILED","Verified review could not be committed",requestID(r)))
		return
	}
	writeJSON(w,http.StatusAccepted,map[string]any{"status":"verified","application_status":"verified","account_approval":"awaiting_two_person_approval"})
}

func (s *service) identityApplicationApprove(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w,r) {
		writeJSON(w,http.StatusForbidden,errorBody("ORIGIN_NOT_ALLOWED","Origin is not allowed",requestID(r)))
		return
	}
	if r.Method!=http.MethodPost {
		writeJSON(w,http.StatusMethodNotAllowed,errorBody("METHOD_NOT_ALLOWED","POST required",requestID(r)))
		return
	}
	reviewer,status,err:=s.identityReviewer(r)
	if err!=nil {
		writeJSON(w,status,errorBody("STAFF_ACCESS_DENIED",err.Error(),requestID(r)))
		return
	}
	applicationID:=strings.TrimSpace(r.PathValue("application_id"))
	if !identityApplicationIDPattern.MatchString(applicationID) {
		writeJSON(w,http.StatusNotFound,errorBody("APPLICATION_NOT_FOUND","Application was not found",requestID(r)))
		return
	}
	r.Body=http.MaxBytesReader(w,r.Body,4<<10)
	defer r.Body.Close()
	var input identityApprovalInput
	if err:=decodeSingleJSON(r.Body,&input); err!=nil || len(strings.TrimSpace(input.Reason))<8 || len(input.Reason)>1000 {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_APPROVAL_REASON","A reason of 8 to 1000 characters is required",requestID(r)))
		return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),8*time.Second)
	defer cancel()
	tx,err:=s.db.Begin(ctx)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Approval could not be recorded",requestID(r)))
		return
	}
	defer func(){ _=tx.Rollback(context.Background()) }()
	var email,requestIDValue,appStatus string
	err=tx.QueryRow(ctx,`
		SELECT email,COALESCE(sensitive_action_request_id::text,''),status
		FROM testagram_identity.identity_applications WHERE id=$1::uuid FOR UPDATE
	`,applicationID).Scan(&email,&requestIDValue,&appStatus)
	if err!=nil || requestIDValue=="" || appStatus!="verified" {
		writeJSON(w,http.StatusConflict,errorBody("APPLICATION_NOT_APPROVABLE","Application is not awaiting account approval",requestID(r)))
		return
	}
	if strings.EqualFold(strings.TrimSpace(email),strings.TrimSpace(reviewer.Email)) {
		writeJSON(w,http.StatusForbidden,errorBody("SELF_APPROVAL_FORBIDDEN","Applicants cannot approve their own applications",requestID(r)))
		return
	}
	var isReviewer bool
	err=tx.QueryRow(ctx,`
		SELECT EXISTS(SELECT 1 FROM testagram_identity.identity_application_reviews
		WHERE application_id=$1::uuid AND reviewer_appwrite_user_id=$2 AND decision='approved')
	`,applicationID,reviewer.ID).Scan(&isReviewer)
	if err!=nil || !isReviewer {
		writeJSON(w,http.StatusForbidden,errorBody("APPROVAL_REVIEWER_MISMATCH","Only the two independent identity reviewers may approve account activation",requestID(r)))
		return
	}
	_,err=tx.Exec(ctx,`
		INSERT INTO testagram_auth.sensitive_action_approvals(request_id,approver_appwrite_user_id,decision,reason)
		VALUES ($1::uuid,$2,'approved',$3)
	`,requestIDValue,reviewer.ID,input.Reason)
	if err!=nil {
		writeJSON(w,http.StatusConflict,errorBody("APPROVAL_ALREADY_RECORDED","This reviewer has already decided this approval request",requestID(r)))
		return
	}
	var approvalCount int
	err=tx.QueryRow(ctx,`
		SELECT count(*) FROM testagram_auth.sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
	`,requestIDValue).Scan(&approvalCount)
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Approval status could not be evaluated",requestID(r)))
		return
	}
	if approvalCount<2 {
		if err:=tx.Commit(ctx); err!=nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_STORE_UNAVAILABLE","Approval could not be committed",requestID(r)))
			return
		}
		writeJSON(w,http.StatusAccepted,map[string]any{"status":"pending","approvals_received":approvalCount,"approvals_required":2})
		return
	}
	if approvalCount!=2 {
		writeJSON(w,http.StatusConflict,errorBody("INVALID_APPROVAL_COUNT","Approval ledger has an unexpected number of approvals",requestID(r)))
		return
	}
	if _,err=tx.Exec(ctx,`
		UPDATE testagram_auth.sensitive_action_requests SET status='approved',decided_at=now()
		WHERE id=$1::uuid AND status='pending' AND required_approvals=2
	`,requestIDValue); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_FAILED","Approval request could not be finalized",requestID(r)))
		return
	}
	if _,err=tx.Exec(ctx,`
		UPDATE testagram_identity.identity_applications SET status='approved',updated_at=now()
		WHERE id=$1::uuid AND status='verified'
	`,applicationID); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_FAILED","Application could not be approved",requestID(r)))
		return
	}
	var firstApprover,secondApprover string
	err=tx.QueryRow(ctx,`
		SELECT approver_appwrite_user_id FROM testagram_auth.sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
		ORDER BY created_at ASC LIMIT 1
	`,requestIDValue).Scan(&firstApprover)
	if err==nil {
		err=tx.QueryRow(ctx,`
			SELECT approver_appwrite_user_id FROM testagram_auth.sensitive_action_approvals
			WHERE request_id=$1::uuid AND decision='approved' AND approver_appwrite_user_id<>$2
			ORDER BY created_at ASC LIMIT 1
		`,requestIDValue,firstApprover).Scan(&secondApprover)
	}
	token,jti:="",""
	if err==nil { token,err=authRandomToken() }
	if err==nil { jti,err=authRandomToken() }
	if err==nil {
		_,err=tx.Exec(ctx,`
			INSERT INTO testagram_identity.identity_approval_tokens
			  (token_sha256,jti_sha256,application_id,first_approver_appwrite_user_id,
			   second_approver_appwrite_user_id,expires_at)
			VALUES ($1,$2,$3::uuid,$4,$5,now()+interval '15 minutes')
		`,authTokenHash(token),authTokenHash(jti),applicationID,firstApprover,secondApprover)
	}
	if err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_TOKEN_FAILED","Approved account could not receive a provisioning authorization",requestID(r)))
		return
	}
	if _,err=tx.Exec(ctx,`
		INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,request_id,metadata)
		VALUES ($1,'identity.account_approval.completed',$2,$3::uuid,jsonb_build_object('approval_count',2))
	`,reviewer.ID,applicationID,requestIDValue); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_AUDIT_FAILED","Approval audit could not be recorded",requestID(r)))
		return
	}
	if err:=tx.Commit(ctx); err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("IDENTITY_APPROVAL_FAILED","Approval could not be committed",requestID(r)))
		return
	}
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusOK,map[string]any{"status":"approved","approval_token":token,"expires_in_seconds":900,"provisioning":"authorized"})
}
