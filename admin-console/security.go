package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type stepUpInput struct { Password string `json:"password"` }
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
	err := a.database.QueryRow(ctx, "SELECT granted FROM testagram_auth.staff_permission_overrides WHERE appwrite_user_id=$1 AND permission_key=$2", user.ID, permission).Scan(&granted)
	if err == nil { return granted, nil }
	if !errors.Is(err, pgx.ErrNoRows) { return false, err }
	err = a.database.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM testagram_auth.staff_roles sr JOIN testagram_auth.staff_role_permissions rp ON rp.role=sr.role WHERE sr.appwrite_user_id=$1 AND rp.permission_key=$2)", user.ID, permission).Scan(&granted)
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
func (a *app) requireRecentStepUp(w http.ResponseWriter, s session) bool {
	if s.StepUpUntil <= time.Now().Unix() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error":"Re-authentication required. Confirm your password again before continuing."})
		return false
	}
	return true
}
func (a *app) handleStepUp(w http.ResponseWriter, r *http.Request) {
	s, user, ok := a.requireStaff(w, r); if !ok { return }
	var input stepUpInput; if !decodeBody(w,r,&input) { return }
	if len(input.Password)<1 || len(input.Password)>1024 { writeJSON(w,400,map[string]string{"error":"Password is required"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),8*time.Second); defer cancel()
	var temporary appwriteSession
	_,err:=a.appwriteRequest(ctx,http.MethodPost,"/account/sessions/email","",map[string]string{"email":user.Email,"password":input.Password},&temporary)
	if err!=nil || temporary.Secret=="" || temporary.ID=="" { writeJSON(w,401,map[string]string{"error":"Re-authentication failed"}); return }
	_,err=a.appwriteRequest(ctx,http.MethodDelete,"/account/sessions/"+url.PathEscape(temporary.ID),temporary.Secret,nil,nil)
	if err!=nil { log.Printf("staff step-up temporary session revoke failed user=%s",user.ID); writeJSON(w,503,map[string]string{"error":"Could not safely complete re-authentication"}); return }
	s.StepUpUntil=time.Now().Add(5*time.Minute).Unix()
	if err:=a.setSession(w,s); err!=nil { writeJSON(w,500,map[string]string{"error":"Could not refresh secure session"}); return }
	writeJSON(w,200,map[string]any{"ok":true,"step_up_expires_at":s.StepUpUntil})
}
func (a *app) handleSensitiveActionRequest(w http.ResponseWriter,r *http.Request) {
	var input sensitiveActionInput; if !decodeBody(w,r,&input) { return }
	permission,err:=permissionForAction(input.ActionType); if err!=nil { writeJSON(w,400,map[string]string{"error":"Unsupported sensitive action type"}); return }
	s,actor,ok:=a.requirePermission(w,r,permission); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
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
	err=tx.QueryRow(ctx,"INSERT INTO testagram_auth.sensitive_action_requests (action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals) VALUES($1,$2,$3,$4,$5::jsonb,$6,2) RETURNING id::text",input.ActionType,actor.ID,input.TargetReference,input.Reason,string(canonical),hex.EncodeToString(digest[:])).Scan(&id)
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,request_id,metadata) VALUES($1,'sensitive_action.requested',$2,$3,jsonb_build_object('action_type',$4))",actor.ID,input.TargetReference,id,input.ActionType) }
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not safely record the approval request"}); return }
	writeJSON(w,201,map[string]any{"id":id,"status":"pending","required_approvals":2,"message":"Request recorded. Two different authorised staff members must approve it. The action has not been executed."})
}
func (a *app) handleSensitiveActionDecision(w http.ResponseWriter,r *http.Request) {
	id:=r.PathValue("id")
	if len(id)!=36 || !regexp.MustCompile("^[0-9a-fA-F-]{36}$").MatchString(id) { writeJSON(w,400,map[string]string{"error":"Invalid approval request ID"}); return }
	var input decisionInput; if !decodeBody(w,r,&input) { return }
	input.Reason=strings.TrimSpace(input.Reason); decision,decisionErr:=normalizeSensitiveDecision(input.Decision); if decisionErr!=nil { writeJSON(w,400,map[string]string{"error":"Decision must be approved or rejected"}); return }; input.Decision=decision; if len(input.Reason)<8 || len(input.Reason)>1000 { writeJSON(w,400,map[string]string{"error":"A decision reason of at least 8 characters is required"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),6*time.Second); defer cancel()
	var action,requester,status string
	err:=a.database.QueryRow(ctx,"SELECT action_type,requested_by_appwrite_user_id,status FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid AND expires_at>now()",id).Scan(&action,&requester,&status)
	if err!=nil { writeJSON(w,404,map[string]string{"error":"Approval request not found or expired"}); return }
	if status!="pending" { writeJSON(w,409,map[string]string{"error":"This request is no longer pending"}); return }
	permission,err:=approvalPermissionForAction(action); if err!=nil { writeJSON(w,400,map[string]string{"error":"Unsupported approval type"}); return }
	s,approver,ok:=a.requirePermission(w,r,permission); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	if approver.ID==requester { writeJSON(w,403,map[string]string{"error":"The requester cannot approve their own sensitive action"}); return }
	tx,err:=a.database.Begin(ctx); if err!=nil { writeJSON(w,503,map[string]string{"error":"Approval service is unavailable"}); return }
	defer func(){_ = tx.Rollback(context.Background())}()
	var locked string
	err=tx.QueryRow(ctx,"SELECT status FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid AND expires_at>now() FOR UPDATE",id).Scan(&locked)
	if err!=nil || locked!="pending" { writeJSON(w,409,map[string]string{"error":"This request is no longer pending"}); return }
	_,err=tx.Exec(ctx,"INSERT INTO testagram_auth.sensitive_action_approvals(request_id,approver_appwrite_user_id,decision,reason) VALUES($1::uuid,$2,$3,$4)",id,approver.ID,input.Decision,input.Reason)
	if err!=nil { writeJSON(w,409,map[string]string{"error":"You have already decided this request or the approval could not be recorded"}); return }
	var count int
	err=tx.QueryRow(ctx,"SELECT count(*) FROM testagram_auth.sensitive_action_approvals WHERE request_id=$1::uuid AND decision='approved'",id).Scan(&count)
	state:="pending"
	if input.Decision=="rejected" { state="rejected"; _,err=tx.Exec(ctx,"UPDATE testagram_auth.sensitive_action_requests SET status='rejected',decided_at=now() WHERE id=$1::uuid AND status='pending'",id) } else if err==nil && count>=2 { state="approved"; _,err=tx.Exec(ctx,"UPDATE testagram_auth.sensitive_action_requests SET status='approved',decided_at=now() WHERE id=$1::uuid AND status='pending'",id) }
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,request_id,metadata) SELECT $1,$2,target_reference,id,jsonb_build_object('action_type',action_type,'decision', $3, 'approval_count',$4) FROM testagram_auth.sensitive_action_requests WHERE id=$5::uuid",approver.ID,"sensitive_action."+input.Decision,input.Decision,count,id) }
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not safely record the approval"}); return }
	writeJSON(w,200,map[string]any{"ok":true,"status":state,"approval_count":count,"required_approvals":2,"message":"Decision recorded. The underlying action is not executed by this approval ledger."})
}
func (a *app) handleSensitiveActionQueue(w http.ResponseWriter,r *http.Request) {
	_,user,ok:=a.requireStaff(w,r); if !ok { return }
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
	rows,err:=a.database.Query(ctx,"SELECT id::text,action_type,requested_by_appwrite_user_id,target_reference,reason,status,required_approvals,expires_at,created_at,(SELECT count(*) FROM testagram_auth.sensitive_action_approvals sa WHERE sa.request_id=sar.id AND sa.decision='approved') FROM testagram_auth.sensitive_action_requests sar WHERE status='pending' AND expires_at>now() ORDER BY created_at ASC LIMIT 100")
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
	}
	items:=make([]item,0)
	for rows.Next() {
		var v item
		if err:=rows.Scan(&v.ID,&v.ActionType,&v.RequestedBy,&v.TargetReference,&v.Reason,&v.Status,&v.RequiredApprovals,&v.ExpiresAt,&v.CreatedAt,&v.ApprovalCount); err!=nil { writeJSON(w,503,map[string]string{"error":"Could not read the approval queue"}); return }
		if !canAudit {
			permission,permissionErr:=approvalPermissionForAction(v.ActionType)
			if permissionErr!=nil { continue }
			allowed,permissionErr:=a.hasPermission(user,permission)
			if permissionErr!=nil { writeJSON(w,503,map[string]string{"error":"Permission service is unavailable"}); return }
			if !allowed { continue }
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
	s, actor, ok := a.requireStaff(w, r, "owner")
	if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	target:=r.PathValue("id")
	if len(target)<1 || len(target)>36 { writeJSON(w,400,map[string]string{"error":"Invalid staff user ID"}); return }
	var input permissionGrantInput
	if !decodeBody(w,r,&input) { return }
	input.Permission=strings.TrimSpace(input.Permission)
	input.Reason=strings.TrimSpace(input.Reason)
	if input.Permission=="" || len(input.Permission)>100 || len(input.Reason)<12 || len(input.Reason)>500 {
		writeJSON(w,400,map[string]string{"error":"Permission and a reason of at least 12 characters are required"}); return
	}
	if input.Permission=="staff.manage" { writeJSON(w,403,map[string]string{"error":"Staff management remains reserved to the Master owner"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	tx,err:=a.database.Begin(ctx)
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Permission service is unavailable"}); return }
	defer func(){_ = tx.Rollback(context.Background())}()
	var exists bool
	err=tx.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_auth.staff_roles WHERE appwrite_user_id=$1)",target).Scan(&exists)
	if err!=nil || !exists { writeJSON(w,404,map[string]string{"error":"Staff account not found"}); return }
	err=tx.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM testagram_auth.admin_permissions WHERE permission_key=$1)",input.Permission).Scan(&exists)
	if err!=nil || !exists { writeJSON(w,400,map[string]string{"error":"Unknown permission"}); return }
	_,err=tx.Exec(ctx,"INSERT INTO testagram_auth.staff_permission_overrides(appwrite_user_id,permission_key,granted,reason,changed_by_appwrite_user_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(appwrite_user_id,permission_key) DO UPDATE SET granted=EXCLUDED.granted,reason=EXCLUDED.reason,changed_by_appwrite_user_id=EXCLUDED.changed_by_appwrite_user_id,created_at=now()",target,input.Permission,input.Granted,input.Reason,actor.ID)
	if err==nil {
		_,err=tx.Exec(ctx,"INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,metadata) VALUES($1,'staff.permission_changed',$2,jsonb_build_object('permission',$3,'granted',$4,'reason',$5))",actor.ID,target,input.Permission,input.Granted,input.Reason)
	}
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not safely update staff permissions"}); return }
	writeJSON(w,200,map[string]any{"ok":true,"user_id":target,"permission":input.Permission,"granted":input.Granted})
}


type staffRoleInput struct { Role string `json:"role"` }

func (a *app) handleStaffList(w http.ResponseWriter,r *http.Request) {
	s,_,ok:=a.requireStaff(w,r,"owner"); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	rows,err:=a.database.Query(ctx,"SELECT appwrite_user_id,role,created_at,updated_at,access_revoked_at FROM testagram_auth.staff_roles ORDER BY created_at DESC LIMIT 500")
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not load staff directory"}); return }
	defer rows.Close()
	type staffItem struct {
		UserID string `json:"user_id"`
		Role string `json:"role"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		RevokedAt *time.Time `json:"revoked_at"`
	}
	items:=make([]staffItem,0)
	for rows.Next() { var item staffItem; if err:=rows.Scan(&item.UserID,&item.Role,&item.CreatedAt,&item.UpdatedAt,&item.RevokedAt); err!=nil { writeJSON(w,503,map[string]string{"error":"Could not read staff directory"}); return }; items=append(items,item) }
	if err:=rows.Err(); err!=nil { writeJSON(w,503,map[string]string{"error":"Could not read staff directory"}); return }
	writeJSON(w,200,map[string]any{"items":items})
}

func (a *app) handleStaffRoleChange(w http.ResponseWriter,r *http.Request) {
	s,actor,ok:=a.requireStaff(w,r,"owner"); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	target:=r.PathValue("id")
	if len(target)<1 || len(target)>36 { writeJSON(w,400,map[string]string{"error":"Invalid staff user ID"}); return }
	var input staffRoleInput
	if !decodeBody(w,r,&input) { return }
	switch input.Role { case "administrator","engineer","operations","support","moderator": default: writeJSON(w,400,map[string]string{"error":"Unsupported staff role"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	tx,err:=a.database.Begin(ctx); if err!=nil { writeJSON(w,503,map[string]string{"error":"Staff access service is unavailable"}); return }
	defer func(){_ = tx.Rollback(context.Background())}()
	var currentRole string
	err=tx.QueryRow(ctx,"SELECT role FROM testagram_auth.staff_roles WHERE appwrite_user_id=$1 AND access_revoked_at IS NULL FOR UPDATE",target).Scan(&currentRole)
	if err!=nil { writeJSON(w,404,map[string]string{"error":"Active staff account not found"}); return }
	if currentRole=="owner" { writeJSON(w,403,map[string]string{"error":"The Master owner account cannot be changed through staff management"}); return }
	_,err=tx.Exec(ctx,"UPDATE testagram_auth.staff_roles SET role=$2,updated_at=now() WHERE appwrite_user_id=$1",target,input.Role)
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.staff_role_audit(actor_appwrite_user_id,target_appwrite_user_id,action,role) VALUES($1,$2,'role_changed',$3)",actor.ID,target,input.Role) }
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,metadata) VALUES($1,'staff.role_changed',$2,jsonb_build_object('old_role',$3,'new_role',$4))",actor.ID,target,currentRole,input.Role) }
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not safely change staff role"}); return }
	writeJSON(w,200,map[string]any{"ok":true,"user_id":target,"role":input.Role})
}

func (a *app) handleStaffRevoke(w http.ResponseWriter,r *http.Request) {
	s,actor,ok:=a.requireStaff(w,r,"owner"); if !ok { return }
	if !a.requireRecentStepUp(w,s) { return }
	target:=r.PathValue("id")
	if len(target)<1 || len(target)>36 { writeJSON(w,400,map[string]string{"error":"Invalid staff user ID"}); return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	tx,err:=a.database.Begin(ctx); if err!=nil { writeJSON(w,503,map[string]string{"error":"Staff access service is unavailable"}); return }
	defer func(){_ = tx.Rollback(context.Background())}()
	var role string
	err=tx.QueryRow(ctx,"SELECT role FROM testagram_auth.staff_roles WHERE appwrite_user_id=$1 AND access_revoked_at IS NULL FOR UPDATE",target).Scan(&role)
	if err!=nil { writeJSON(w,404,map[string]string{"error":"Active staff account not found"}); return }
	if role=="owner" { writeJSON(w,403,map[string]string{"error":"The Master owner account cannot be revoked through staff management"}); return }
	_,err=tx.Exec(ctx,"UPDATE testagram_auth.staff_roles SET access_revoked_at=now(),updated_at=now() WHERE appwrite_user_id=$1",target)
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.staff_role_audit(actor_appwrite_user_id,target_appwrite_user_id,action,role) VALUES($1,$2,'revoked',$3)",actor.ID,target,role) }
	if err==nil { _,err=tx.Exec(ctx,"INSERT INTO testagram_auth.admin_security_audit(actor_appwrite_user_id,action,target_reference,metadata) VALUES($1,'staff.access_revoked',$2,jsonb_build_object('former_role',$3))",actor.ID,target,role) }
	if err==nil { err=tx.Commit(ctx) }
	if err!=nil { writeJSON(w,503,map[string]string{"error":"Could not safely revoke staff access"}); return }
	writeJSON(w,200,map[string]any{"ok":true,"user_id":target,"access_revoked":true})
}


func (a *app) handleAdminAudit(w http.ResponseWriter,r *http.Request) {
	if _,_,ok:=a.requirePermission(w,r,"audit.read"); !ok { return }
	ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
	rows,err:=a.database.Query(ctx,`SELECT id,actor_appwrite_user_id,action,target_reference,request_id::text,metadata,created_at
		FROM testagram_auth.admin_security_audit ORDER BY created_at DESC LIMIT 200`)
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
