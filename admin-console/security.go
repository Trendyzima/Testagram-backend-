package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type stepUpInput struct {
	Password string `json:"password"`
}

type sensitiveActionInput struct {
	ActionType      string          `json:"action_type"`
	TargetReference string          `json:"target_reference"`
	Reason          string          `json:"reason"`
	Payload         json.RawMessage `json:"payload"`
}

type decisionInput struct {
	Reason string `json:"reason"`
}

func permissionForAction(action string) (string, error) {
	switch action {
	case "identity.approve":
		return "identity.review", nil
	case "finance.execute":
		return "finance.initiate", nil
	case "security.change":
		return "security.manage", nil
	case "deployment.production":
		return "deployment.request", nil
	default:
		return "", errors.New("unsupported sensitive action")
	}
}

func approvalPermissionForAction(action string) (string, error) {
	switch action {
	case "identity.approve":
		return "identity.review", nil
	case "finance.execute":
		return "finance.approve", nil
	case "security.change":
		return "security.manage", nil
	case "deployment.production":
		return "deployment.approve", nil
	default:
		return "", errors.New("unsupported sensitive action")
	}
}

func (a *app) hasPermission(user authUser, permission string) (bool, error) {
	if staffRole(user) == "owner" {
		return true, nil
	}
	var granted bool
	err := a.database.QueryRow(contextBackground(), `
		SELECT granted FROM testagram_auth.staff_permission_overrides
		WHERE appwrite_user_id=$1 AND permission_key=$2
	`, user.ID, permission).Scan(&granted)
	if err == nil {
		return granted, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return false, err
	}
	err = a.database.QueryRow(contextBackground(), `
		SELECT EXISTS (
			SELECT 1 FROM testagram_auth.staff_roles sr
			JOIN testagram_auth.staff_role_permissions rp ON rp.role=sr.role
			WHERE sr.appwrite_user_id=$1 AND rp.permission_key=$2
		)
	`, user.ID, permission).Scan(&granted)
	return granted, err
}

// requirePermission always resolves authorization from PostgreSQL, never from
// browser state, role labels or client-supplied permission claims.
func (a *app) requirePermission(w http.ResponseWriter, r *http.Request, permission string) (session, authUser, bool) {
	s, user, ok := a.requireStaff(w, r)
	if !ok {
		return session{}, authUser{}, false
	}
	allowed, err := a.hasPermission(user, permission)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Permission service is unavailable"})
		return session{}, authUser{}, false
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Your staff account does not have permission for this action"})
		return session{}, authUser{}, false
	}
	return s, user, true
}

func (a *app) requireRecentStepUp(w http.ResponseWriter, s session) bool {
	if s.StepUpUntil <= time.Now().Unix() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Re-authentication required. Confirm your password again before continuing."})
		return false
	}
	return true
}

func (a *app) handleStepUp(w http.ResponseWriter, r *http.Request) {
	s, user, ok := a.requireStaff(w, r)
	if !ok {
		return
	}
	var input stepUpInput
	if !decodeBody(w, r, &input) {
		return
	}
	if len(input.Password) < 1 || len(input.Password) > 1024 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var temporary appwriteSession
	_, err := a.appwriteRequest(ctx, http.MethodPost, "/account/sessions/email", "", map[string]string{
		"email": user.Email, "password": input.Password,
	}, &temporary)
	if err != nil || temporary.Secret == "" || temporary.ID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Re-authentication failed"})
		return
	}
	// The temporary session is used only to prove knowledge of the current
	// password; revoke it immediately and keep the existing console session.
	_, revokeErr := a.appwriteRequest(ctx, http.MethodDelete, "/account/sessions/"+url.PathEscape(temporary.ID), temporary.Secret, nil, nil)
	if revokeErr != nil {
		log.Printf("staff step-up temporary session revoke failed user=%s", user.ID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not safely complete re-authentication"})
		return
	}
	s.StepUpUntil = time.Now().Add(5 * time.Minute).Unix()
	if err := a.setSession(w, s); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not refresh secure session"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "step_up_expires_at": s.StepUpUntil})
}

func (a *app) handleSensitiveActionRequest(w http.ResponseWriter, r *http.Request) {
	var input sensitiveActionInput
	if !decodeBody(w, r, &input) {
		return
	}
	permission, err := permissionForAction(input.ActionType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unsupported sensitive action type"})
		return
	}
	s, actor, ok := a.requirePermission(w, r, permission)
	if !ok {
		return
	}
	if !a.requireRecentStepUp(w, s) {
		return
	}
	input.TargetReference = strings.TrimSpace(input.TargetReference)
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.TargetReference) < 1 || len(input.TargetReference) > 200 || len(input.Reason) < 12 || len(input.Reason) > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A target reference and a reason of at least 12 characters are required"})
		return
	}
	payload := input.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	var payloadValue any
	if err := json.Unmarshal(payload, &payloadValue); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload must be valid JSON"})
		return
	}
	canonicalPayload, err := json.Marshal(payloadValue)
	if err != nil || len(canonicalPayload) > 12<<10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Payload is invalid or too large"})
		return
	}
	digest := sha256.Sum256(canonicalPayload)
	requestID := uuid.New()
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Approval service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_auth.sensitive_action_requests
		(id,action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,2)
	`, requestID, input.ActionType, actor.ID, input.TargetReference, input.Reason, string(canonicalPayload), hex.EncodeToString(digest[:]))
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,request_id,metadata)
			VALUES($1,'sensitive_action.requested',$2,$3,jsonb_build_object('action_type',$4))
		`, actor.ID, input.TargetReference, requestID, input.ActionType)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not safely record the approval request"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": requestID.String(), "status": "pending", "required_approvals": 2,
		"message": "Request recorded. Two different authorised staff members must approve it. The action has not been executed.",
	})
}

func (a *app) handleSensitiveActionDecision(w http.ResponseWriter, r *http.Request) {
	requestID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid approval request ID"})
		return
	}
	var input decisionInput
	if !decodeBody(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason) < 8 || len(input.Reason) > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A decision reason of at least 8 characters is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	var actionType, requesterID, status string
	err = a.database.QueryRow(ctx, `
		SELECT action_type,requested_by_appwrite_user_id,status
		FROM testagram_auth.sensitive_action_requests WHERE id=$1 AND expires_at>now()
	`, requestID).Scan(&actionType, &requesterID, &status)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Approval request not found or expired"})
		return
	}
	if status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "This request is no longer pending"})
		return
	}
	permission, err := approvalPermissionForAction(actionType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unsupported approval type"})
		return
	}
	s, approver, ok := a.requirePermission(w, r, permission)
	if !ok {
		return
	}
	if !a.requireRecentStepUp(w, s) {
		return
	}
	if approver.ID == requesterID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "The requester cannot approve their own sensitive action"})
		return
	}
	tx, err := a.database.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Approval service is unavailable"})
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var lockedStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM testagram_auth.sensitive_action_requests WHERE id=$1 AND expires_at>now() FOR UPDATE`, requestID).Scan(&lockedStatus)
	if err != nil || lockedStatus != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "This request is no longer pending"})
		return
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO testagram_auth.sensitive_action_approvals(request_id,approver_appwrite_user_id,decision,reason)
		VALUES($1,$2,'approved',$3)
	`, requestID, approver.ID, input.Reason)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "You have already decided this request or the approval could not be recorded"})
		return
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM testagram_auth.sensitive_action_approvals WHERE request_id=$1 AND decision='approved'`, requestID).Scan(&count)
	if err == nil && count >= 2 {
		_, err = tx.Exec(ctx, `UPDATE testagram_auth.sensitive_action_requests SET status='approved',decided_at=now() WHERE id=$1 AND status='pending'`, requestID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,request_id,metadata)
			SELECT $1,'sensitive_action.approved',target_reference,id,jsonb_build_object('action_type',action_type,'approval_count',$2)
			FROM testagram_auth.sensitive_action_requests WHERE id=$3
		`, approver.ID, count, requestID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not safely record the approval"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": map[bool]string{true: "approved", false: "pending"}[count >= 2], "approval_count": count, "required_approvals": 2, "message": "Approval recorded. Approval does not itself execute the underlying action."})
}

func (a *app) handleSensitiveActionQueue(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.requirePermission(w, r, "audit.read"); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.database.Query(ctx, `
		SELECT id,action_type,requested_by_appwrite_user_id,target_reference,reason,status,
		       required_approvals,expires_at,created_at,
		       (SELECT count(*) FROM testagram_auth.sensitive_action_approvals sa WHERE sa.request_id=sar.id AND sa.decision='approved')
		FROM testagram_auth.sensitive_action_requests sar
		WHERE status='pending' AND expires_at>now()
		ORDER BY created_at ASC LIMIT 100
	`)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not load the approval queue"})
		return
	}
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
	}
	items := make([]item, 0)
	for rows.Next() {
		var v item
		var id uuid.UUID
		if err := rows.Scan(&id,&v.ActionType,&v.RequestedBy,&v.TargetReference,&v.Reason,&v.Status,&v.RequiredApprovals,&v.ExpiresAt,&v.CreatedAt,&v.ApprovalCount); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not read the approval queue"})
			return
		}
		v.ID = id.String()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not read the approval queue"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
