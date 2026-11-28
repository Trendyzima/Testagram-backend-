package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	htmlstd "html"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type staffInvitationPayload struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type staffInvitationMail struct {
	Recipient string            `json:"recipient"`
	Subject   string            `json:"subject"`
	HTML      string            `json:"html"`
	Text      string            `json:"text"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func normalizeStaffInvitationEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || len(value) > 254 {
		return "", errors.New("invalid invitation email")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address == "" || !strings.EqualFold(parsed.Address, value) {
		return "", errors.New("enter a valid email address without a display name")
	}
	return strings.ToLower(parsed.Address), nil
}

func validStaffInvitationRole(role string) bool {
	switch role {
	case "owner", "administrator", "identity_reviewer", "engineer", "operations", "support", "moderator":
		return true
	default:
		return false
	}
}

func (a *app) handleInvite(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.requireStaff(w, r, "owner")
	if !ok {
		return
	}
	claims, ok := a.requireRecentStepUpClaims(w, s)
	if !ok {
		return
	}
	if claims.Subject != actor.ID {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "WebAuthn elevation does not belong to the authenticated staff principal"})
		return
	}

	var input struct {
		Email  string `json:"email"`
		Role   string `json:"role"`
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	email, err := normalizeStaffInvitationEmail(input.Email)
	input.Role = strings.TrimSpace(input.Role)
	input.Reason = strings.TrimSpace(input.Reason)
	if err != nil || !validStaffInvitationRole(input.Role) || len(input.Reason) < 12 || len(input.Reason) > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A valid email, supported staff role and reason of 12 to 1000 characters are required"})
		return
	}
	if allowed, rateErr := a.consumeAdminRateLimit(r.Context(), actor.ID, "staff-invitation-request", 5, time.Hour); rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Security rate-limit service is unavailable"})
		return
	} else if !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many staff invitation requests. Try again later."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Staff invitation service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var targetID string
	err = tx.QueryRow(ctx, `
		SELECT i.user_id::text
		FROM testagram_auth.identities i
		WHERE i.email_normalized=$1
		  AND i.email_verified_at IS NOT NULL
		  AND i.disabled_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM testagram_auth.staff_principals sp
		    WHERE sp.user_id=i.user_id
		      AND (sp.enabled=TRUE OR EXISTS (
		        SELECT 1 FROM testagram_auth.staff_role_grants rg
		        WHERE rg.user_id=sp.user_id AND rg.revoked_at IS NULL
		      ))
		  )
		FOR UPDATE OF i
	`, email).Scan(&targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "The email must belong to an existing, verified Testagram account that is not already active staff"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not validate the invitation target"})
		return
	}
	if targetID == actor.ID {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "You cannot invite your own account"})
		return
	}

	var duplicate bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM testagram_auth.native_staff_invitations
		  WHERE email_normalized=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()
		) OR EXISTS (
		  SELECT 1 FROM testagram_auth.native_sensitive_action_requests
		  WHERE action_type='staff.invite' AND payload->>'email'=$1
		    AND status='pending' AND expires_at>now()
		)
	`, email).Scan(&duplicate)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not check existing invitations"})
		return
	}
	if duplicate {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "A live invitation or pending invitation approval already exists for this email"})
		return
	}

	payload, err := json.Marshal(staffInvitationPayload{Email: email, Role: input.Role})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not prepare the invitation request"})
		return
	}
	digest := sha256.Sum256(payload)
	var requestID string
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES ('staff.invite',$1::uuid,$2,$3,$4::jsonb,$5,2)
		RETURNING id::text
	`, actor.ID, targetID, input.Reason, string(payload), hex.EncodeToString(digest[:])).Scan(&requestID)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_auth.native_admin_security_audit
			  (actor_user_id,action,target_reference,request_id,metadata)
			VALUES ($1::uuid,'staff.invitation.requested',$2,$3::uuid,
			        jsonb_build_object('role',$4::text,'required_approvals',2))
		`, actor.ID, targetID, requestID, input.Role)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not safely record the invitation request"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": requestID, "status": "pending", "required_approvals": 2,
		"message": "Invitation request recorded. Two different eligible owners must approve it before an email is sent.",
	})
}


func (a *app) handleRevokeStaffInvitation(w http.ResponseWriter, r *http.Request) {
	s, actor, ok := a.requireStaff(w, r, "owner")
	if !ok { return }
	claims, ok := a.requireRecentStepUpClaims(w, s)
	if !ok { return }
	if claims.Subject != actor.ID { writeJSON(w, http.StatusUnauthorized, map[string]string{"error":"WebAuthn elevation does not belong to the authenticated staff principal"}); return }
	var input struct { Reason string `json:"reason"` }
	if !decodeBody(w, r, &input) { return }
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason)<12 || len(input.Reason)>1000 { writeJSON(w,http.StatusBadRequest,map[string]string{"error":"A revocation reason of 12 to 1000 characters is required"}); return }
	if allowed, rateErr := a.consumeAdminRateLimit(r.Context(),actor.ID,"staff-invitation-revoke-request",5,time.Hour); rateErr!=nil {
		writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Security rate-limit service is unavailable"}); return
	} else if !allowed { writeJSON(w,http.StatusTooManyRequests,map[string]string{"error":"Too many invitation revocation requests. Try again later."}); return }
	invitationID:=strings.TrimSpace(r.PathValue("id"))
	if len(invitationID)!=36 || !regexp.MustCompile("^[0-9a-fA-F-]{36}$").MatchString(invitationID) { writeJSON(w,http.StatusBadRequest,map[string]string{"error":"Invalid invitation ID"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second); defer cancel()
	tx,err:=a.database.Begin(ctx)
	if err!=nil { writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Invitation revocation service is unavailable"}); return }
	defer func(){ _=tx.Rollback(context.Background()) }()
	var targetID, originalRequestID string
	err=tx.QueryRow(ctx,`
		SELECT i.user_id::text,ni.request_id::text
		FROM testagram_auth.native_staff_invitations ni
		JOIN testagram_auth.native_sensitive_action_requests original
		  ON original.id=ni.request_id AND original.action_type='staff.invite' AND original.status='approved'
		JOIN testagram_auth.identities i
		  ON i.email_normalized=ni.email_normalized AND i.email_verified_at IS NOT NULL AND i.disabled_at IS NULL
		WHERE ni.id=$1::uuid AND ni.accepted_at IS NULL AND ni.revoked_at IS NULL AND ni.expires_at>now()
		FOR UPDATE OF ni,i
	`,invitationID).Scan(&targetID,&originalRequestID)
	if err!=nil {
		if errors.Is(err,pgx.ErrNoRows) { writeJSON(w,http.StatusConflict,map[string]string{"error":"Invitation is not pending, has expired, or its verified target no longer exists"}); return }
		writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Could not validate the pending invitation"}); return
	}
	if targetID==actor.ID { writeJSON(w,http.StatusConflict,map[string]string{"error":"The invitation target cannot request its own revocation"}); return }
	var duplicate bool
	err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_auth.native_sensitive_action_requests WHERE action_type='staff.invitation.revoke' AND payload->>'invitation_id'=$1 AND status='pending' AND expires_at>now())`,invitationID).Scan(&duplicate)
	if err!=nil { writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Could not check existing revocation requests"}); return }
	if duplicate { writeJSON(w,http.StatusConflict,map[string]string{"error":"A two-person revocation request is already pending for this invitation"}); return }
	payload,err:=json.Marshal(struct { InvitationID string `json:"invitation_id"` }{InvitationID:invitationID})
	if err!=nil { writeJSON(w,http.StatusInternalServerError,map[string]string{"error":"Could not prepare the revocation request"}); return }
	digest:=sha256.Sum256(payload)
	var requestID string
	err=tx.QueryRow(ctx,`
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES ('staff.invitation.revoke',$1::uuid,$2,$3,$4::jsonb,$5,2) RETURNING id::text
	`,actor.ID,targetID,input.Reason,string(payload),hex.EncodeToString(digest[:])).Scan(&requestID)
	if err==nil { _,err=tx.Exec(ctx,`
		INSERT INTO testagram_auth.native_admin_security_audit(actor_user_id,action,target_reference,request_id,metadata)
		VALUES($1::uuid,'staff.invitation.revocation_requested',$2,$3::uuid,
		       jsonb_build_object('invitation_id',$4::uuid,'original_invitation_request_id',$5::uuid,'required_approvals',2))
	`,actor.ID,targetID,requestID,invitationID,originalRequestID) }
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,http.StatusServiceUnavailable,map[string]string{"error":"Could not safely record the revocation request"}); return }
	writeJSON(w,http.StatusCreated,map[string]any{"id":requestID,"status":"pending","required_approvals":2,"message":"Revocation request recorded. Two different eligible owners must approve it before the invitation token is invalidated."})
}

func (a *app) hasPendingStaffInvitation(ctx context.Context, userID, email string) (bool, error) {
	normalized, err := normalizeStaffInvitationEmail(email)
	if err != nil {
		return false, nil
	}
	var pending bool
	err = a.database.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM testagram_auth.native_staff_invitations ni
		  JOIN testagram_auth.native_sensitive_action_requests sar ON sar.id=ni.request_id
		  JOIN testagram_auth.identities i ON i.user_id=$1::uuid
		  WHERE ni.email_normalized=$2
		    AND i.email_normalized=ni.email_normalized
		    AND i.email_verified_at IS NOT NULL AND i.disabled_at IS NULL
		    AND ni.accepted_at IS NULL AND ni.revoked_at IS NULL AND ni.expires_at>now()
		    AND sar.action_type='staff.invite' AND sar.status='approved'
		)
	`, userID, normalized).Scan(&pending)
	return pending, err
}

func (a *app) handleAcceptStaffInvitation(w http.ResponseWriter, r *http.Request) {
	_, user, ok := a.currentUser(w, r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Sign in to the invited Testagram account before accepting this invitation"})
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(input.Token))
	if err != nil || len(rawToken) != 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invitation token is invalid or expired"})
		return
	}
	tokenHash := sha256.Sum256(rawToken)
	if allowed, rateErr := a.consumeAdminRateLimit(r.Context(), user.ID, "staff-invitation-accept", 5, time.Hour); rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Security rate-limit service is unavailable"})
		return
	} else if !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many invitation attempts. Try again later."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Invitation acceptance service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var invitationID, requestID, targetEmail, role, requestStatus, secondApprover string
	var targetID string
	err = tx.QueryRow(ctx, `
		SELECT ni.id::text,ni.request_id::text,ni.email_normalized,ni.role,
		       i.user_id::text,sar.status,
		       (SELECT a.approver_user_id::text
		        FROM testagram_auth.native_sensitive_action_approvals a
		        WHERE a.request_id=ni.request_id AND a.decision='approved'
		        ORDER BY a.created_at DESC,a.id DESC LIMIT 1)
		FROM testagram_auth.native_staff_invitations ni
		JOIN testagram_auth.native_sensitive_action_requests sar ON sar.id=ni.request_id
		JOIN testagram_auth.identities i ON i.email_normalized=ni.email_normalized
		WHERE ni.token_sha256=$1 AND ni.accepted_at IS NULL AND ni.revoked_at IS NULL
		  AND ni.expires_at>now() AND i.user_id=$2::uuid
		  AND i.email_verified_at IS NOT NULL AND i.disabled_at IS NULL
		  AND sar.action_type='staff.invite' AND sar.status='approved'
		  AND sar.target_reference=i.user_id::text
		FOR UPDATE OF ni,i
	`, tokenHash[:], user.ID).Scan(&invitationID, &requestID, &targetEmail, &role, &targetID, &requestStatus, &secondApprover)
	if err != nil || requestStatus != "approved" || targetID != user.ID ||
		!strings.EqualFold(targetEmail, user.Email) || !validStaffInvitationRole(role) || secondApprover == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Invitation is invalid, expired, already used, or does not belong to this verified account"})
		return
	}

	var requester string
	var quorum int
	var approvalCount int
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(sar.requested_by_user_id::text,''),sar.required_approvals,
		       (SELECT count(DISTINCT a.approver_user_id)
		        FROM testagram_auth.native_sensitive_action_approvals a
		        WHERE a.request_id=sar.id AND a.decision='approved')
		FROM testagram_auth.native_sensitive_action_requests sar
		WHERE sar.id=$1::uuid
		FOR UPDATE
	`, requestID).Scan(&requester, &quorum, &approvalCount)
	if err != nil || requester == user.ID || quorum != 2 || approvalCount != 2 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Invitation approval quorum is incomplete or invalid"})
		return
	}
	var alreadyActive bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM testagram_auth.staff_principals sp
		  WHERE sp.user_id=$1::uuid
		    AND (sp.enabled=TRUE OR EXISTS (
		      SELECT 1 FROM testagram_auth.staff_role_grants rg
		      WHERE rg.user_id=sp.user_id AND rg.revoked_at IS NULL
		    ))
		)
	`, user.ID).Scan(&alreadyActive)
	if err != nil || alreadyActive {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "This account already has active staff access"})
		return
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO testagram_auth.staff_principals AS existing_staff(user_id,enabled,enabled_at,enabled_by)
		VALUES($1::uuid,TRUE,now(),$2::uuid)
		ON CONFLICT(user_id) DO UPDATE
		  SET enabled=TRUE,enabled_at=now(),enabled_by=EXCLUDED.enabled_by
		  WHERE existing_staff.enabled=FALSE
		    AND NOT EXISTS (
		      SELECT 1 FROM testagram_auth.staff_role_grants rg
		      WHERE rg.user_id=EXCLUDED.user_id AND rg.revoked_at IS NULL
		    )
	`, user.ID, secondApprover)
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Staff principal could not be safely activated"})
		return
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
		VALUES($1::uuid,$2,$3::uuid)
	`, user.ID, role, secondApprover)
	if err == nil {
		var accepted pgconn.CommandTag
		accepted, err = tx.Exec(ctx, `
			UPDATE testagram_auth.native_staff_invitations
			SET accepted_by_user_id=$2::uuid,accepted_at=now()
			WHERE id=$1::uuid AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()
		`, invitationID, user.ID)
		if err == nil && accepted.RowsAffected() != 1 {
			err = errors.New("invitation was concurrently consumed")
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_auth.native_staff_lifecycle_events
			  (request_id,actor_user_id,target_user_id,target_email,action,role,metadata)
			VALUES ($1::uuid,$2::uuid,$2::uuid,$3,'invitation_accepted',$4,
			        jsonb_build_object('invitation_id',$5::uuid,'two_person_approval_count',2))
		`, requestID, user.ID, targetEmail, role, invitationID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Invitation acceptance could not be committed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "role": role,
		"message": "Invitation accepted. Your Testagram staff identity is provisioned. An owner must now authorize your two-person WebAuthn enrollment before privileged access is available.",
	})
}

func executeStaffInvitation(ctx context.Context, tx pgx.Tx, requestID, requesterID, approverID, targetReference string, payload []byte) error {
	var actionType, requestStatus, immutableTarget string
	var quorum int
	if err := tx.QueryRow(ctx, `
		SELECT action_type,status,target_reference,required_approvals
		FROM testagram_auth.native_sensitive_action_requests
		WHERE id=$1::uuid FOR UPDATE
	`, requestID).Scan(&actionType, &requestStatus, &immutableTarget, &quorum); err != nil ||
		actionType != "staff.invite" || requestStatus != "approved" || immutableTarget != targetReference || quorum != 2 {
		return errors.New("staff invitation request state or two-person quorum is invalid")
	}
	var totalApprovals int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT approver_user_id)
		FROM testagram_auth.native_sensitive_action_approvals
		WHERE request_id=$1::uuid AND decision='approved'
	`, requestID).Scan(&totalApprovals); err != nil || totalApprovals != 2 {
		return errors.New("exactly two independent approvals are required")
	}
	var invitation staffInvitationPayload
	if err := json.Unmarshal(payload, &invitation); err != nil {
		return errors.New("staff invitation payload is invalid")
	}
	email, err := normalizeStaffInvitationEmail(invitation.Email)
	if err != nil || !validStaffInvitationRole(invitation.Role) {
		return errors.New("staff invitation payload does not match its immutable target")
	}

	var targetID string
	err = tx.QueryRow(ctx, `
		SELECT user_id::text FROM testagram_auth.identities
		WHERE email_normalized=$1 AND email_verified_at IS NOT NULL AND disabled_at IS NULL
		FOR UPDATE
	`, email).Scan(&targetID)
	if err != nil || targetID != targetReference || targetID == requesterID || targetID == approverID {
		return errors.New("invitation target is missing, unverified, disabled or conflicts with an approver")
	}

	var requesterEligible bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM testagram_auth.staff_principals sp
		  JOIN testagram_auth.identities i ON i.user_id=sp.user_id
		  JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id
		  WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE AND sp.mfa_enrolled_at IS NOT NULL
		    AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
		    AND rg.role='owner' AND rg.revoked_at IS NULL
		    AND EXISTS (
		      SELECT 1 FROM testagram_auth.staff_webauthn_credentials wc
		      WHERE wc.user_id=sp.user_id AND wc.revoked_at IS NULL
		        AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
		    )
		)
	`, requesterID).Scan(&requesterEligible)
	if err != nil || !requesterEligible {
		return errors.New("invitation requester is no longer an eligible owner")
	}

	var eligibleApprovers int
	err = tx.QueryRow(ctx, `
		SELECT count(DISTINCT a.approver_user_id)
		FROM testagram_auth.native_sensitive_action_approvals a
		JOIN testagram_auth.staff_principals sp ON sp.user_id=a.approver_user_id AND sp.enabled=TRUE
		JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.role='owner' AND rg.revoked_at IS NULL
		JOIN testagram_auth.staff_webauthn_credentials wc
		  ON wc.user_id=a.approver_user_id AND wc.rpid=a.webauthn_rpid
		 AND wc.credential_id=a.webauthn_credential_id
		 AND wc.revoked_at IS NULL AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
		WHERE a.request_id=$1::uuid AND a.decision='approved' AND a.approver_user_id<>$2::uuid
	`, requestID, targetID).Scan(&eligibleApprovers)
	if err != nil || eligibleApprovers != 2 {
		return errors.New("exactly two distinct, currently eligible owner WebAuthn approvers are required")
	}

	var existing bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM testagram_auth.staff_principals sp
		  WHERE sp.user_id=$1::uuid
		    AND (sp.enabled=TRUE OR EXISTS (
		      SELECT 1 FROM testagram_auth.staff_role_grants rg
		      WHERE rg.user_id=sp.user_id AND rg.revoked_at IS NULL
		    ))
		) OR EXISTS (
		  SELECT 1 FROM testagram_auth.native_staff_invitations
		  WHERE email_normalized=$2 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()
		)
	`, targetID, email).Scan(&existing)
	if err != nil || existing {
		return errors.New("invitation target already has staff access or a live invitation")
	}

	randomToken := make([]byte, 32)
	if _, err := rand.Read(randomToken); err != nil {
		return errors.New("invitation token could not be generated")
	}
	token := base64.RawURLEncoding.EncodeToString(randomToken)
	tokenHash := sha256.Sum256(randomToken)
	var invitationID string
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_staff_invitations
		  (request_id,email_normalized,role,token_sha256,created_by,expires_at)
		VALUES ($1::uuid,$2,$3,$4,$5::uuid,now()+interval '7 days')
		RETURNING id::text
	`, requestID, email, invitation.Role, tokenHash[:], requesterID).Scan(&invitationID)
	if err != nil {
		return errors.New("staff invitation record could not be created")
	}
	if err := enqueueStaffInvitationMail(ctx, tx, email, invitation.Role, token); err != nil {
		return errors.New("staff invitation verification email could not be queued")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_auth.native_staff_lifecycle_events
		  (request_id,actor_user_id,target_user_id,target_email,action,role,metadata)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4,'invitation_created',$5,
		        jsonb_build_object('invitation_id',$6::uuid,'expires_in_days',7,'approval_count',2))
	`, requestID, approverID, targetID, email, invitation.Role, invitationID)
	return err
}


func executeStaffInvitationRevocation(ctx context.Context, tx pgx.Tx, requestID, requesterID, approverID, targetReference string, payload []byte) error {
	var actionType,requestStatus,immutableTarget string
	var quorum int
	if err:=tx.QueryRow(ctx,`SELECT action_type,status,target_reference,required_approvals FROM testagram_auth.native_sensitive_action_requests WHERE id=$1::uuid FOR UPDATE`,requestID).Scan(&actionType,&requestStatus,&immutableTarget,&quorum); err!=nil || actionType!="staff.invitation.revoke" || requestStatus!="approved" || immutableTarget!=targetReference || quorum!=2 {
		return errors.New("staff invitation revocation request state or two-person quorum is invalid")
	}
	var request struct { InvitationID string `json:"invitation_id"` }
	if err:=json.Unmarshal(payload,&request); err!=nil || len(request.InvitationID)!=36 { return errors.New("staff invitation revocation payload is invalid") }
	var count int
	if err:=tx.QueryRow(ctx,`SELECT count(DISTINCT approver_user_id) FROM testagram_auth.native_sensitive_action_approvals WHERE request_id=$1::uuid AND decision='approved'`,requestID).Scan(&count); err!=nil || count!=2 {
		return errors.New("exactly two independent approvals are required to revoke an invitation")
	}
	var originalRequestID,email,role string
	err:=tx.QueryRow(ctx,`
		SELECT ni.request_id::text,ni.email_normalized,ni.role
		FROM testagram_auth.native_staff_invitations ni
		WHERE ni.id=$1::uuid AND ni.accepted_at IS NULL AND ni.revoked_at IS NULL AND ni.expires_at>now()
		FOR UPDATE
	`,request.InvitationID).Scan(&originalRequestID,&email,&role)
	if err!=nil { return errors.New("invitation was accepted, revoked, expired or no longer exists") }
	var targetID string
	err=tx.QueryRow(ctx,`SELECT user_id::text FROM testagram_auth.identities WHERE email_normalized=$1 AND email_verified_at IS NOT NULL AND disabled_at IS NULL FOR UPDATE`,email).Scan(&targetID)
	if err!=nil || targetID!=targetReference || targetID==requesterID || targetID==approverID { return errors.New("invitation target no longer matches the immutable revocation request") }
	var originalAction,originalStatus string
	err=tx.QueryRow(ctx,`SELECT action_type,status FROM testagram_auth.native_sensitive_action_requests WHERE id=$1::uuid FOR UPDATE`,originalRequestID).Scan(&originalAction,&originalStatus)
	if err!=nil || originalAction!="staff.invite" || originalStatus!="approved" { return errors.New("original invitation approval is not valid") }
	var requesterEligible bool
	err=tx.QueryRow(ctx,`
		SELECT EXISTS(SELECT 1 FROM testagram_auth.staff_principals sp
		  JOIN testagram_auth.identities i ON i.user_id=sp.user_id
		  JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id
		  WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE AND sp.mfa_enrolled_at IS NOT NULL
		    AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
		    AND rg.role='owner' AND rg.revoked_at IS NULL
		    AND EXISTS(SELECT 1 FROM testagram_auth.staff_webauthn_credentials wc
		      WHERE wc.user_id=sp.user_id AND wc.revoked_at IS NULL AND wc.user_verified=TRUE AND wc.clone_warning=FALSE))
	`,requesterID).Scan(&requesterEligible)
	if err!=nil || !requesterEligible { return errors.New("revocation requester is no longer an eligible owner") }
	var eligibleApprovers int
	err=tx.QueryRow(ctx,`
		SELECT count(DISTINCT a.approver_user_id)
		FROM testagram_auth.native_sensitive_action_approvals a
		JOIN testagram_auth.staff_principals sp ON sp.user_id=a.approver_user_id AND sp.enabled=TRUE
		JOIN testagram_auth.identities i ON i.user_id=sp.user_id AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
		JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.role='owner' AND rg.revoked_at IS NULL
		JOIN testagram_auth.staff_webauthn_credentials wc ON wc.user_id=a.approver_user_id
		  AND wc.rpid=a.webauthn_rpid AND wc.credential_id=a.webauthn_credential_id
		  AND wc.revoked_at IS NULL AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
		WHERE a.request_id=$1::uuid AND a.decision='approved'
		  AND a.approver_user_id<>$2::uuid AND a.approver_user_id<>$3::uuid
	`,requestID,requesterID,targetID).Scan(&eligibleApprovers)
	if err!=nil || eligibleApprovers!=2 { return errors.New("two distinct, currently eligible owner WebAuthn approvers are required") }
	tag,err:=tx.Exec(ctx,`UPDATE testagram_auth.native_staff_invitations SET revoked_at=now() WHERE id=$1::uuid AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()`,request.InvitationID)
	if err!=nil || tag.RowsAffected()!=1 { return errors.New("invitation could not be atomically revoked") }
	_,err=tx.Exec(ctx,`
		INSERT INTO testagram_auth.native_staff_lifecycle_events(request_id,actor_user_id,target_user_id,target_email,action,role,metadata)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4,'invitation_revoked',$5,
		       jsonb_build_object('invitation_id',$6::uuid,'original_invitation_request_id',$7::uuid,'two_person_approval_count',2))
	`,requestID,approverID,targetID,email,role,request.InvitationID,originalRequestID)
	return err
}


func enqueueStaffInvitationMail(ctx context.Context, tx pgx.Tx, recipient, role, token string) error {
	key, err := hex.DecodeString(getEnvMailKey())
	if err != nil || len(key) != 32 {
		return errors.New("mail encryption key unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(env("ADMIN_PUBLIC_URL", "")), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return errors.New("staff console public URL must be a canonical HTTPS origin")
	}
	link := baseURL + "/staff/invitations/accept?token=" + url.QueryEscape(token)
	escapedLink := htmlstd.EscapeString(link)
	escapedRole := htmlstd.EscapeString(role)
	subject := "Your Testagram staff invitation"
	body := "<p>You have been invited to join the Testagram staff team as <strong>" + escapedRole + "</strong>.</p>" +
		"<p>Sign in to the Testagram staff console with the invited, email-verified account, then accept your invitation:</p>" +
		"<p><a href=\"" + escapedLink + "\">Accept staff invitation</a></p>" +
		"<p>This single-use invitation expires in seven days. Your staff access will still require a separately authorized WebAuthn enrollment.</p>" +
		"<p>If you were not expecting this invitation, you can ignore this email.</p>"
	html := "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"></head><body>" + body + "</body></html>"
	textBody := "You have been invited to join the Testagram staff team as " + role + ". Sign in to the invited, email-verified account and accept your invitation: " + link + ". This single-use invitation expires in seven days. Staff access still requires separately authorized WebAuthn enrollment."
	payload, err := json.Marshal(staffInvitationMail{Recipient: recipient, Subject: subject, HTML: html, Text: textBody})
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := aead.Seal(nil, nonce, payload, []byte("testagram-mail-v1"))
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(strings.ToLower(recipient)))
	fingerprint := mac.Sum(nil)
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_mail.outbox(payload_ciphertext,payload_nonce,status,available_at,recipient_fingerprint)
		VALUES($1,$2,'pending',now(),$3)
	`, ciphertext, nonce, fingerprint)
	return err
}

func getEnvMailKey() string {
	return strings.TrimSpace(env("TESTAGRAM_MAIL_ENCRYPTION_KEY", ""))
}
