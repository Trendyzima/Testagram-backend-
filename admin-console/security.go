package main

import (
	"context"
	"crypto/rand"
	"crypto/hmac"
	"encoding/base64"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)


func (a *app) consumeAdminRateLimit(ctx context.Context, actorID, operation string, limit int, window time.Duration) (bool, error) {
	ctx,cancel:=context.WithTimeout(ctx,2*time.Second); defer cancel()
	digest:=sha256.Sum256([]byte(actorID+"\x00"+operation))
	key:=hex.EncodeToString(digest[:])
	var count int
	err:=a.database.QueryRow(ctx, `INSERT INTO testagram_auth.admin_rate_limits AS current
		(rate_key,window_started_at,request_count) VALUES($1,now(),1)
		ON CONFLICT(rate_key) DO UPDATE SET
			request_count=CASE WHEN current.window_started_at <= now()-($2 * interval '1 second') THEN 1 ELSE current.request_count+1 END,
			window_started_at=CASE WHEN current.window_started_at <= now()-($2 * interval '1 second') THEN now() ELSE current.window_started_at END
		RETURNING request_count`,key,int64(window.Seconds())).Scan(&count)
	if err!=nil { return false,err }
	return count<=limit,nil
}
type sensitiveActionInput struct {
	ActionType string `json:"action_type"`
	TargetReference string `json:"target_reference"`
	Reason string `json:"reason"`
	Payload json.RawMessage `json:"payload"`
}
type decisionInput struct { Decision string `json:"decision"`; Reason string `json:"reason"` }

func normalizeSensitiveDecision(decision string) (string, error) {
	decision = strings.TrimSpace(decision)
	if decision == "" { return "approved", nil }
	if decision != "approved" && decision != "rejected" { return "", errors.New("unsupported decision") }
	return decision, nil
}

func permissionForAction(action string) (string, error) {
	switch action {
	case "identity.approve": return "identity.review", nil
	case "finance.execute": return "finance.initiate", nil
	case "security.change": return "security.manage", nil
	case "deployment.production": return "deployment.request", nil
	default: return "", errors.New("unsupported sensitive action")
	}
}
func approvalPermissionForAction(action string) (string, error) {
	switch action {
	case "identity.approve": return "identity.review", nil
	case "finance.execute": return "finance.approve", nil
	case "security.change": return "security.manage", nil
	case "deployment.production": return "deployment.approve", nil
	default: return "", errors.New("unsupported sensitive action")
	}
}
func (a *app) hasPermission(user authUser, permission string) (bool, error) {
	if staffRole(user) == "owner" { return true, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var granted bool
	err := a.database.QueryRow(ctx, "SELECT granted FROM testagram_auth.native_staff_permission_overrides WHERE user_id=$1::uuid AND permission_key=$2", user.ID, permission).Scan(&granted)
	if err == nil { return granted, nil }
	if !errors.Is(err, pgx.ErrNoRows) { return false, err }
	err = a.database.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM testagram_auth.staff_role_grants rg JOIN testagram_auth.staff_role_permissions rp ON rp.role=rg.role WHERE rg.user_id=$1::uuid AND rg.revoked_at IS NULL AND rp.permission_key=$2)", user.ID, permission).Scan(&granted)
	return granted, err
}
func (a *app) requirePermission(w http.ResponseWriter, r *http.Request, permission string) (session, authUser, bool) {
	s, user, ok := a.requireStaff(w, r)
	if !ok { return session{}, authUser{}, false }
	allowed, err := a.hasPermission(user, permission)
	if err != nil { writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error":"Permission service is unavailable"}); return session{}, authUser{}, false }
	if !allowed { writeJSON(w, http.StatusForbidden, map[string]string{"error":"Your staff account does not have permission for this action"}); return session{}, authUser{}, false }
	return s, user, true
}
func validateRecentStepUpToken(s session, secret []byte, now time.Time) (nativeElevationClaims, error) {
	var claims nativeElevationClaims
	parts := strings.Split(s.ElevatedToken, ".")
	if len(parts) != 3 || len(secret) < 32 {
		return claims, errors.New("a verified WebAuthn elevation is required")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err != nil || json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return claims, errors.New("invalid MFA token header")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, errors.New("invalid MFA elevation signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return claims, errors.New("invalid MFA elevation claims")
	}
	accessParts := strings.Split(s.AccessToken, ".")
	if len(accessParts) != 3 {
		return claims, errors.New("native session binding unavailable")
	}
	accessPayload, err := base64.RawURLEncoding.DecodeString(accessParts[1])
	var native nativeElevationClaims
	if err != nil || json.Unmarshal(accessPayload, &native) != nil {
		return claims, errors.New("native session binding unavailable")
	}
	nowUnix := now.UTC().Unix()
	if claims.Issuer != "testagram-auth" || claims.Audience != "testagram-api" || claims.Role != "authenticated" ||
		claims.Subject == "" || claims.Subject != native.Subject || claims.SessionID == "" || claims.SessionID != native.SessionID ||
		claims.TokenVersion < 1 || claims.TokenVersion != native.TokenVersion ||
		claims.IssuedAt <= 0 || claims.IssuedAt > nowUnix+60 || claims.ExpiresAt <= nowUnix || claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > 300 || claims.MFAAuthenticatedAt <= 0 || claims.MFAAuthenticatorID == "" ||
		claims.MFAAuthenticatedAt > nowUnix+60 || nowUnix-claims.MFAAuthenticatedAt > 300 {
		return claims, errors.New("expired or invalid native MFA elevation")
	}
	return claims, nil
}

func (a *app) requireRecentStepUp(w http.ResponseWriter, s session) bool {
	secret := []byte(strings.TrimSpace(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))
	claims, err := validateRecentStepUpToken(s, secret, time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return false
	}
	credentialID, err := hex.DecodeString(claims.MFAAuthenticatorID)
	rpid := strings.TrimSpace(os.Getenv("TESTAGRAM_WEBAUTHN_RPID"))
	if err != nil || len(credentialID) == 0 || rpid == "" || a.database == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The WebAuthn credential bound to this elevation is unavailable"})
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var active bool
	err = a.database.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM testagram_auth.staff_principals sp
			JOIN testagram_auth.staff_webauthn_credentials wc
			  ON wc.user_id=sp.user_id AND wc.rpid=$2 AND wc.credential_id=$3
			 AND wc.revoked_at IS NULL AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
			WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE
		)
	`, claims.Subject, rpid, credentialID).Scan(&active)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "WebAuthn credential status is unavailable"})
		return false
	}
	if !active {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The WebAuthn credential used for MFA has been revoked or is no longer trusted"})
		return false
	}
	return true
}

func (a *app) handleSensitiveActionRequest(w http.ResponseWriter,r *http.Request) {
	var input sensitiveActionInput; if !decodeBody(w,r,&input) { return }
	permission,err:=permissionForAction(input.ActionType); if err!=nil { writeJSON(w,400,map[string]string{"error":"Unsupported sensitive action type"}); return }
	s,actor,ok:=a.requirePermission(w,r,permission); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	if allowed,rateErr:=a.consumeAdminRateLimit(r.Context(),actor.ID,"sensitive-request",10,time.Hour); rateErr!=nil { writeJSON(w,503,map[string]string{"error":"Security rate-limit service is unavailable"}); return } else if !allowed { writeJSON(w,429,map[string]string{"error":"Too many sensitive-action requests. Try again later."}); return }
	input.TargetReference=strings.TrimSpace(input.TargetReference); input.Reason=strings.TrimSpace(input.Reason)
	if len(input.TargetReference)<1 || len(input.TargetReference)>200 || len(input.Reason)<12 || len(input.Reason)>1000 { writeJSON(w,400,map[string]string{"error":"A target reference and a reason of at least 12 characters are required"}); return }
	payload:=input.Payload; if len(payload)==0 { payload=json.RawMessage("{}") }
	var value any; if err:=json.Unmarshal(payload,&value); err!=nil { writeJSON(w,400,map[string]string{"error":"Payload must be valid JSON"}); return }
	object,ok:=value.(map[string]any)
	if !ok { writeJSON(w,400,map[string]string{"error":"Payload must be a JSON object"}); return }
	canonical,err:=json.Marshal(object); if err!=nil || len(canonical)>12<<10 { writeJSON(w,400,map[string]string{"error":"Payload is invalid or too large"}); return }
	digest:=sha256.Sum256(canonical)
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second); defer cancel()
	tx,err:=a.database.Begin(ctx); if err!=nil { writeJSON(w,503,map[string]string{"error":"Approval service is unavailable"}); return }
	defer func(){_ = tx.Rollback(context.Background())}()
	var id string
	err=tx.QueryRow(ctx,"INSERT INTO testagram_auth.native_sensitive_action_requests (action_type,requested_by_user_id,target_reference,reason,payload,payload_sha256,required_approvals) VALUES($1,$2,$3,$4,$5::jsonb,$6,2) RETURNING id::text",input.ActionType,actor.ID,input.TargetReference,input.Reason,string(canonical),hex.EncodeToString(digest[:])).Scan(&id)
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.native_admin_security_audit(actor_user_id,action,target_reference,request_id,metadata) VALUES($1,'sensitive_action.requested',$2,$3,jsonb_build_object('action_type',$4))",actor.ID,input.TargetReference,id,input.ActionType) }
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not safely record the approval request"}); return }
	writeJSON(w,201,map[string]any{"id":id,"status":"pending","required_approvals":2,"message":"Request recorded. Two different authorised staff members must approve it. The action has not been executed."})
}
type identityReviewSnapshot struct {
	ApplicationID              string `json:"application_id"`
	Email                       string `json:"email"`
	Username                    string `json:"username"`
	DisplayName                 string `json:"display_name"`
	EvidenceFingerprintSHA256   string `json:"evidence_fingerprint_sha256"`
	EvidenceAuditReference      string `json:"evidence_audit_reference"`
	TermsVersion                string `json:"terms_version"`
	PrivacyVersion              string `json:"privacy_version"`
}

func recordIdentityApplicationReview(ctx context.Context, tx pgx.Tx, requestID, applicationID, reviewerID, decision, reason string, rawPayload []byte, approvalCount int) (string, error) {
	var snapshot identityReviewSnapshot
	if err := json.Unmarshal(rawPayload, &snapshot); err != nil {
		return "", errors.New("identity approval payload is invalid")
	}
	if snapshot.ApplicationID != applicationID ||
		len(snapshot.EvidenceAuditReference) < 8 || len(snapshot.EvidenceAuditReference) > 200 {
		return "", errors.New("identity approval payload is not bound to an auditable application")
	}

	var email, username, displayName, termsVersion, privacyVersion, applicationStatus, linkedRequestID string
	var evidenceFingerprint []byte
	var adultVerified bool
	err := tx.QueryRow(ctx, `
		SELECT email, username, display_name, evidence_fingerprint_sha256,
		       terms_version, privacy_version, status, adult_verified_at IS NOT NULL,
		       native_sensitive_action_request_id::text
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid
		FOR UPDATE
	`, applicationID).Scan(&email, &username, &displayName, &evidenceFingerprint,
		&termsVersion, &privacyVersion, &applicationStatus, &adultVerified, &linkedRequestID)
	if err != nil {
		return "", errors.New("identity application was not found")
	}
	if linkedRequestID != requestID {
		return "", errors.New("identity application is not linked to this approval request")
	}
	if applicationStatus != "verified" && applicationStatus != "under_review" {
		return "", errors.New("identity application is no longer reviewable")
	}
	var actionType, requestStatus, targetReference, requesterID, requesterService string
	var requiredApprovals, recordedApprovals int
	err = tx.QueryRow(ctx, `
		SELECT action_type, status, target_reference, COALESCE(requested_by_user_id::text,''), requested_by_service, required_approvals
		FROM testagram_auth.native_sensitive_action_requests
		WHERE id=$1::uuid
		FOR UPDATE
	`, requestID).Scan(&actionType, &requestStatus, &targetReference, &requesterID, &requesterService, &requiredApprovals)
	if err != nil || actionType != "identity.approve" || targetReference != applicationID || requiredApprovals != 2 || requesterID != "" || requesterService != "identity_pipeline" {
		return "", errors.New("linked approval request does not match the identity application")
	}
	err = tx.QueryRow(ctx, `
		SELECT count(DISTINCT approver_user_id)
		FROM testagram_auth.native_sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
	`, requestID).Scan(&recordedApprovals)
	if err != nil || recordedApprovals != approvalCount {
		return "", errors.New("approval count does not match the durable approval ledger")
	}
	if decision == "rejected" && requestStatus != "rejected" {
		return "", errors.New("rejected identity decision is not reflected in the approval ledger")
	}
	if decision == "approved" && approvalCount > 2 {
		return "", errors.New("identity approval has more than two approvers")
	}
	if decision == "approved" && approvalCount == 2 && requestStatus != "approved" {
		return "", errors.New("completed two-person approval is not reflected in the approval ledger")
	}
	if decision == "approved" && approvalCount < 2 && requestStatus != "pending" {
		return "", errors.New("incomplete approval request is not pending")
	}
	if !adultVerified {
		return "", errors.New("verified adult evidence is required before review decisions")
	}
	if snapshot.Email != email || snapshot.Username != username ||
		snapshot.DisplayName != displayName ||
		snapshot.EvidenceFingerprintSHA256 != hex.EncodeToString(evidenceFingerprint) ||
		snapshot.TermsVersion != termsVersion || snapshot.PrivacyVersion != privacyVersion {
		return "", errors.New("identity application changed after the review request was created")
	}

	var wasEvidenceReviewer bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM testagram_identity.identity_application_reviews
		  WHERE application_id=$1::uuid AND reviewer_user_id=$2::uuid
		    AND review_stage='evidence_review'
		)
	`, applicationID, reviewerID).Scan(&wasEvidenceReviewer)
	if err != nil || wasEvidenceReviewer {
		return "", errors.New("evidence reviewers cannot approve account activation")
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_identity.identity_application_reviews
		  (application_id, reviewer_user_id, review_stage, decision, reason, evidence_audit_reference)
		VALUES ($1::uuid, $2, 'account_approval', $3, $4, $5)
	`, applicationID, reviewerID, decision, reason, snapshot.EvidenceAuditReference)
	if err != nil {
		return "", errors.New("identity review was already recorded or could not be persisted")
	}

	nextStatus := "under_review"
	if decision == "rejected" {
		nextStatus = "rejected"
	} else if approvalCount >= 2 {
		nextStatus = "approved"
	}
	_, err = tx.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET status=$2, updated_at=now()
		WHERE id=$1::uuid
	`, applicationID, nextStatus)
	if err != nil {
		return "", errors.New("identity application status could not be updated")
	}
	if decision == "approved" && approvalCount > 2 {
		return "", errors.New("identity approval has more than two approvers")
	}
	if decision == "approved" && approvalCount == 2 {
		rows, err := tx.Query(ctx, `
			SELECT approver_user_id::text
			FROM testagram_auth.native_sensitive_action_approvals
			WHERE request_id=$1::uuid AND decision='approved'
			ORDER BY created_at ASC, id ASC
			LIMIT 2
		`, requestID)
		if err != nil {
			return "", errors.New("independent account approvers could not be loaded")
		}
		approvers := make([]string, 0, 2)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return "", errors.New("independent account approvers could not be loaded")
			}
			approvers = append(approvers, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", errors.New("independent account approvers could not be loaded")
		}
		rows.Close()
		if len(approvers) != 2 || approvers[0] == approvers[1] {
			return "", errors.New("exactly two distinct account approvers are required")
		}
		var tokenSeed, jtiSeed [32]byte
		if _, err := rand.Read(tokenSeed[:]); err != nil {
			return "", errors.New("provisioning authorization seed could not be generated")
		}
		if _, err := rand.Read(jtiSeed[:]); err != nil {
			return "", errors.New("provisioning authorization identifier could not be generated")
		}
		tokenDigest := sha256.Sum256(tokenSeed[:])
		jtiDigest := sha256.Sum256(jtiSeed[:])
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_identity.identity_approval_tokens
			  (token_sha256,jti_sha256,application_id,first_approver_user_id,second_approver_user_id,expires_at)
			VALUES ($1,$2,$3::uuid,$4::uuid,$5::uuid,now()+interval '15 minutes')
		`, tokenDigest[:], jtiDigest[:], applicationID, approvers[0], approvers[1])
		if err != nil {
			return "", fmt.Errorf("provisioning authorization could not be initialized: %w", err)
		}
	}

	return nextStatus, nil
}

func recordSensitiveActionDecision(ctx context.Context, tx pgx.Tx, requestID, expectedAction, expectedRequester, approverID, decision, reason string) (string, string, int, error) {
	var actionType, requesterID, requesterService, requestStatus, targetReference string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT action_type, COALESCE(requested_by_user_id::text,''), requested_by_service, status, target_reference, payload
		FROM testagram_auth.native_sensitive_action_requests
		WHERE id=$1::uuid AND expires_at>now()
		FOR UPDATE
	`, requestID).Scan(&actionType, &requesterID, &requesterService, &requestStatus, &targetReference, &payload)
	if err != nil || requestStatus != "pending" {
		return "", "", 0, errors.New("approval request is missing, expired or no longer pending")
	}
	if actionType != expectedAction || requesterID != expectedRequester ||
		(requesterID == "" && requesterService != "identity_pipeline") ||
		(requesterID != "" && requesterService != "staff") {
		return "", "", 0, errors.New("approval request changed after authorization")
	}
	if requesterID != "" && approverID == requesterID {
		return "", "", 0, errors.New("requester cannot approve their own sensitive action")
	}
	if decision != "approved" && decision != "rejected" {
		return "", "", 0, errors.New("unsupported sensitive-action decision")
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_approvals
		  (request_id, approver_user_id, decision, reason)
		VALUES ($1::uuid, $2, $3, $4)
	`, requestID, approverID, decision, reason); err != nil {
		return "", "", 0, errors.New("approver has already decided this request or the decision could not be recorded")
	}

	var approvalCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT approver_user_id)
		FROM testagram_auth.native_sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
	`, requestID).Scan(&approvalCount); err != nil {
		return "", "", 0, err
	}

	state := "pending"
	if decision == "rejected" {
		state = "rejected"
	} else if approvalCount >= 2 {
		state = "approved"
	}
	if state != "pending" {
		tag, err := tx.Exec(ctx, `
			UPDATE testagram_auth.native_sensitive_action_requests
			SET status=$2, decided_at=now()
			WHERE id=$1::uuid AND status='pending'
		`, requestID, state)
		if err != nil {
			return "", "", 0, err
		}
		if tag.RowsAffected() != 1 {
			return "", "", 0, errors.New("approval request changed while recording the decision")
		}
	}

	applicationState := ""
	if actionType == "identity.approve" {
		applicationState, err = recordIdentityApplicationReview(
			ctx, tx, requestID, targetReference, approverID, decision, reason, payload, approvalCount,
		)
		if err != nil {
			return "", "", 0, err
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_auth.native_admin_security_audit
		  (actor_user_id, action, target_reference, request_id, metadata)
		VALUES ($1, $2, $3, $4::uuid,
		        jsonb_build_object('action_type', $5::text, 'decision', $6::text, 'approval_count', $7::integer))
	`, approverID, "sensitive_action."+decision, targetReference, requestID, actionType, decision, approvalCount)
	if err != nil {
		return "", "", 0, err
	}
	return state, applicationState, approvalCount, nil
}

func (a *app) handleSensitiveActionDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) != 36 || !regexp.MustCompile("^[0-9a-fA-F-]{36}$").MatchString(id) {
		writeJSON(w, 400, map[string]string{"error": "Invalid approval request ID"})
		return
	}
	var input decisionInput
	if !decodeBody(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	decision, decisionErr := normalizeSensitiveDecision(input.Decision)
	if decisionErr != nil {
		writeJSON(w, 400, map[string]string{"error": "Decision must be approved or rejected"})
		return
	}
	input.Decision = decision
	if len(input.Reason) < 8 || len(input.Reason) > 1000 {
		writeJSON(w, 400, map[string]string{"error": "A decision reason of at least 8 characters is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	var action, requester, status string
	err := a.database.QueryRow(ctx, `
		SELECT action_type, COALESCE(requested_by_user_id::text,''), status
		FROM testagram_auth.native_sensitive_action_requests
		WHERE id=$1::uuid AND expires_at>now()
	`, id).Scan(&action, &requester, &status)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "Approval request not found or expired"})
		return
	}
	if status != "pending" {
		writeJSON(w, 409, map[string]string{"error": "This request is no longer pending"})
		return
	}
	permission, err := approvalPermissionForAction(action)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "Unsupported approval type"})
		return
	}
	s, approver, ok := a.requirePermission(w, r, permission)
	if !ok {
		return
	}
	if !a.requireRecentStepUp(w, s) {
		return
	}
	if allowed, rateErr := a.consumeAdminRateLimit(ctx, approver.ID, "sensitive-decision", 30, time.Hour); rateErr != nil {
		writeJSON(w, 503, map[string]string{"error": "Security rate-limit service is unavailable"})
		return
	} else if !allowed {
		writeJSON(w, 429, map[string]string{"error": "Too many approval decisions. Try again later."})
		return
	}
	if requester != "" && approver.ID == requester {
		writeJSON(w, 403, map[string]string{"error": "The requester cannot approve their own sensitive action"})
		return
	}

	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Approval service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	state, applicationState, count, err := recordSensitiveActionDecision(ctx, tx, id, action, requester, approver.ID, input.Decision, input.Reason)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Could not safely record the approval"})
		return
	}
	response := map[string]any{
		"ok": true, "status": state, "approval_count": count,
		"required_approvals": 2,
		"message": "Decision recorded. The underlying action is not executed by this approval ledger.",
	}
	if action == "identity.approve" {
		response["application_status"] = applicationState
	}
	writeJSON(w, 200, response)
}

func (a *app) handleSensitiveActionQueue(w http.ResponseWriter,r *http.Request) {
	s,user,ok:=a.requireStaff(w,r); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	canAudit,err:=a.hasPermission(user,"audit.read")
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Permission service is unavailable"}); return }
	approvalPermissions:=[]string{"identity.review","finance.approve","security.manage","deployment.approve"}
	canReview:=canAudit
	if !canReview {
		for _,permission:=range approvalPermissions {
			allowed,permissionErr:=a.hasPermission(user,permission)
			if permissionErr!=nil { writeJSON(w,503,map[string]string{"error":"Permission service is unavailable"}); return }
			if allowed { canReview=true; break }
		}
	}
	if !canReview { writeJSON(w,403,map[string]string{"error":"You do not have permission to review sensitive actions"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	rows,err:=a.database.Query(ctx,"SELECT id::text,action_type,COALESCE(requested_by_user_id::text,requested_by_service),target_reference,reason,status,required_approvals,expires_at,created_at,payload,(SELECT count(*) FROM testagram_auth.native_sensitive_action_approvals sa WHERE sa.request_id=sar.id AND sa.decision='approved') FROM testagram_auth.native_sensitive_action_requests sar WHERE status='pending' AND expires_at>now() ORDER BY created_at ASC LIMIT 100")
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not load the approval queue"}); return }
	defer rows.Close()
	type item struct {
		ID string `json:"id"`
		ActionType string `json:"action_type"`
		RequestedBy string `json:"requested_by"`
		TargetReference string `json:"target_reference"`
		Reason string `json:"reason"`
		Status string `json:"status"`
		RequiredApprovals int `json:"required_approvals"`
		ExpiresAt time.Time `json:"expires_at"`
		CreatedAt time.Time `json:"created_at"`
		ApprovalCount int `json:"approval_count"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	items:=make([]item,0)
	for rows.Next() {
		var v item
		if err:=rows.Scan(&v.ID,&v.ActionType,&v.RequestedBy,&v.TargetReference,&v.Reason,&v.Status,&v.RequiredApprovals,&v.ExpiresAt,&v.CreatedAt,&v.Payload,&v.ApprovalCount); err!=nil { writeJSON(w,503,map[string]string{"error":"Could not read the approval queue"}); return }
		if !canAudit {
			permission,permissionErr:=approvalPermissionForAction(v.ActionType)
			if permissionErr!=nil { continue }
			allowed,permissionErr:=a.hasPermission(user,permission)
			if permissionErr!=nil { writeJSON(w,503,map[string]string{"error":"Permission service is unavailable"}); return }
			if !allowed { continue }
		}
		if v.ActionType == "identity.approve" {
			canReviewIdentity, permissionErr := a.hasPermission(user, "identity.review")
			if permissionErr != nil {
				writeJSON(w,503,map[string]string{"error":"Permission service is unavailable"})
				return
			}
			if !canReviewIdentity { v.Payload = nil }
		} else {
			v.Payload = nil
		}
		items=append(items,v)
	}
	if err:=rows.Err(); err!=nil { writeJSON(w,503,map[string]string{"error":"Could not read the approval queue"}); return }
	writeJSON(w,200,map[string]any{"items":items})
}


type permissionGrantInput struct {
	Permission string `json:"permission"`
	Granted bool `json:"granted"`
	Reason string `json:"reason"`
}

func (a *app) handleSetStaffPermission(w http.ResponseWriter, r *http.Request) {
	// Permission overrides are security-sensitive staff lifecycle mutations.
	// Until the durable two-person approval executor binds the exact target,
	// permission and grant value into one transaction, this route must not
	// mutate native authorization state, even for an owner with recent MFA.
	s, _, ok := a.requireStaff(w, r, "owner")
	if !ok {
		return
	}
	if !a.requireRecentStepUp(w, s) {
		return
	}
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "Staff permission changes require the two-person native staff approval workflow. No permission override was changed.",
	})
}


type staffRoleInput struct { Role string `json:"role"` }

func (a *app) handleStaffList(w http.ResponseWriter,r *http.Request) {
	s,_,ok:=a.requireStaff(w,r,"owner"); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	rows,err:=a.database.Query(ctx,`SELECT sp.user_id::text,i.email_normalized,
		sp.enabled,sp.mfa_enrolled_at,sp.created_at,
		COALESCE(string_agg(rg.role,',' ORDER BY rg.role) FILTER (WHERE rg.revoked_at IS NULL),'')
		FROM testagram_auth.staff_principals sp
		JOIN testagram_auth.identities i ON i.user_id=sp.user_id
		LEFT JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id
		GROUP BY sp.user_id,i.email_normalized,sp.enabled,sp.mfa_enrolled_at,sp.created_at
		ORDER BY sp.created_at DESC LIMIT 500`)
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not load native staff directory"}); return }
	defer rows.Close()
	type staffItem struct {
		UserID string `json:"user_id"`
		Email string `json:"email"`
		Enabled bool `json:"enabled"`
		MFAEnrolledAt *time.Time `json:"mfa_enrolled_at"`
		CreatedAt time.Time `json:"created_at"`
		Roles string `json:"roles"`
	}
	items:=make([]staffItem,0)
	for rows.Next(){var item staffItem;if err:=rows.Scan(&item.UserID,&item.Email,&item.Enabled,&item.MFAEnrolledAt,&item.CreatedAt,&item.Roles);err!=nil{writeJSON(w,503,map[string]string{"error":"Could not read native staff directory"});return};items=append(items,item)}
	if err:=rows.Err();err!=nil{writeJSON(w,503,map[string]string{"error":"Could not read native staff directory"});return}
	writeJSON(w,200,map[string]any{"items":items})
}

func (a *app) handleStaffRoleChange(w http.ResponseWriter,r *http.Request) {
	s,_,ok:=a.requireStaff(w,r,"owner");if !ok{return}
	if !a.requireRecentStepUp(w,s){return}
	writeJSON(w,http.StatusNotImplemented,map[string]string{"error":"Staff role changes require the two-person native staff approval workflow. No direct role mutation is permitted."})
}

func (a *app) handleStaffRevoke(w http.ResponseWriter,r *http.Request) {
	s,_,ok:=a.requireStaff(w,r,"owner");if !ok{return}
	if !a.requireRecentStepUp(w,s){return}
	writeJSON(w,http.StatusNotImplemented,map[string]string{"error":"Staff revocation requires the two-person native staff approval workflow. No direct role mutation is permitted."})
}


func (a *app) handleAdminAudit(w http.ResponseWriter,r *http.Request) {
	s,_,ok:=a.requirePermission(w,r,"audit.read");if !ok{return}
	if !a.requireRecentStepUp(w,s){return}
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	rows,err:=a.database.Query(ctx,`SELECT id,actor_user_id,action,target_reference,request_id::text,metadata,created_at
		FROM testagram_auth.native_admin_security_audit ORDER BY created_at DESC LIMIT 200`)
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not load the security audit ledger"}); return }
	defer rows.Close()
	type auditEntry struct {
		ID int64 `json:"id"`
		Actor string `json:"actor"`
		Action string `json:"action"`
		Target string `json:"target"`
		RequestID *string `json:"request_id"`
		Metadata json.RawMessage `json:"metadata"`
		CreatedAt time.Time `json:"created_at"`
	}
	entries:=make([]auditEntry,0)
	for rows.Next() {
		var entry auditEntry
		if err:=rows.Scan(&entry.ID,&entry.Actor,&entry.Action,&entry.Target,&entry.RequestID,&entry.Metadata,&entry.CreatedAt); err!=nil {
			writeJSON(w,503,map[string]string{"error":"Could not read the security audit ledger"}); return
		}
		entries=append(entries,entry)
	}
	if err:=rows.Err(); err!=nil { writeJSON(w,503,map[string]string{"error":"Could not read the security audit ledger"}); return }
	writeJSON(w,200,map[string]any{"items":entries})
}


type internalAuthorizationInput struct {
	UserID string `json:"user_id"`
	Permission string `json:"permission"`
}

func serviceCredentialMatches(provided string, expected []byte) bool {
	if len(expected)!=32 || len(provided)!=64 { return false }
	decoded,err:=hex.DecodeString(provided)
	return err==nil && subtle.ConstantTimeCompare(decoded,expected)==1
}

func (a *app) handleInternalAuthorize(w http.ResponseWriter,r *http.Request) {
	if r.Method!=http.MethodPost { writeJSON(w,http.StatusMethodNotAllowed,map[string]string{"error":"Method not allowed"}); return }
	if len(a.serviceAuthKey)!=32 { writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Internal authorization is not configured"}); return }
	provided:=r.Header.Get("X-Testagram-Admin-Service-Key")
	if !serviceCredentialMatches(provided,a.serviceAuthKey) {
		writeJSON(w,http.StatusUnauthorized,map[string]string{"error":"Invalid service credentials"}); return
	}
	var input internalAuthorizationInput
	if !decodeBody(w,r,&input) { return }
	input.UserID=strings.TrimSpace(input.UserID)
	input.Permission=strings.TrimSpace(input.Permission)
	if len(input.UserID)<1 || len(input.UserID)>36 || len(input.Permission)<1 || len(input.Permission)>100 {
		writeJSON(w,http.StatusBadRequest,map[string]string{"error":"A valid staff identity and permission are required"}); return
	}
	ctx,cancel:=context.WithTimeout(r.Context(),3*time.Second); defer cancel()
	var role string
	err:=a.database.QueryRow(ctx,`SELECT rg.role FROM testagram_auth.staff_principals sp
		JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
		WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE AND sp.mfa_enrolled_at IS NOT NULL
		ORDER BY CASE rg.role WHEN 'owner' THEN 0 WHEN 'administrator' THEN 1 ELSE 2 END LIMIT 1`,input.UserID).Scan(&role)
	if err!=nil {
		if errors.Is(err,pgx.ErrNoRows) { writeJSON(w,http.StatusForbidden,map[string]any{"authorized":false,"reason":"not_active_staff"}); return }
		writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Staff authorization database is unavailable"}); return
	}
	user:=authUser{ID:input.UserID,StaffRole:role}
	allowed,err:=a.hasPermission(user,input.Permission)
	if err!=nil { writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Permission service is unavailable"}); return }
	writeJSON(w,http.StatusOK,map[string]any{"authorized":allowed,"user_id":input.UserID,"permission":input.Permission,"role":role})
}
