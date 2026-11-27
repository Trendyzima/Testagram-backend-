package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "strings"

 "github.com/jackc/pgx/v5"
)

// executeApprovedStaffLifecycle runs only inside the approval transaction.
// The invitation action intentionally remains unsupported until its native
// InstaBox outbox delivery and redemption flow are connected.
func executeApprovedStaffLifecycle(ctx context.Context, tx pgx.Tx, requestID, action, requesterID, targetReference string, payload []byte) error {
 if action == "staff.invite" {
  return errors.New("staff invitations require the native mail outbox executor")
 }
 var approvals []string
 rows, err := tx.Query(ctx, `
  SELECT approver_user_id::text
  FROM testagram_auth.native_sensitive_action_approvals
  WHERE request_id=$1::uuid AND decision='approved'
  ORDER BY created_at,id
  FOR UPDATE
 `, requestID)
 if err != nil { return err }
 for rows.Next() {
  var id string
  if err := rows.Scan(&id); err != nil { rows.Close(); return err }
  approvals = append(approvals,id)
 }
 if err := rows.Err(); err != nil { rows.Close(); return err }
 rows.Close()
 if len(approvals)!=2 || approvals[0]==approvals[1] || approvals[0]==requesterID || approvals[1]==requesterID {
  return errors.New("exactly two distinct approvers, independent from the requester, are required")
 }
 // staff.manage is intentionally owner-only until a separately reviewed
 // delegation model exists. Recheck both principals and current credentials
 // inside this transaction rather than trusting the earlier HTTP preflight.
 for _, approver := range approvals {
  var eligible bool
  err=tx.QueryRow(ctx,`
   SELECT EXISTS (
    SELECT 1 FROM testagram_auth.staff_principals sp
    JOIN testagram_auth.identities i ON i.user_id=sp.user_id
    JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.role='owner' AND rg.revoked_at IS NULL
    JOIN testagram_auth.native_sensitive_action_approvals sa ON sa.approver_user_id=sp.user_id
    JOIN testagram_auth.staff_webauthn_credentials wc
      ON wc.user_id=sp.user_id AND wc.rpid=sa.webauthn_rpid
     AND wc.credential_id=sa.webauthn_credential_id
     AND wc.revoked_at IS NULL AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
    WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE
      AND sp.mfa_enrolled_at IS NOT NULL
      AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
      AND sa.request_id=$2::uuid AND sa.decision='approved'
   )
  `,approver,requestID).Scan(&eligible)
  if err!=nil || !eligible { return errors.New("both approvers must remain active verified owners with the exact trusted WebAuthn credential") }
 }
 // The requester must also still be an active owner at execution time.
 var requesterEligible bool
 if err=tx.QueryRow(ctx,`
  SELECT EXISTS (
   SELECT 1 FROM testagram_auth.staff_principals sp
   JOIN testagram_auth.identities i ON i.user_id=sp.user_id
   JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id AND rg.role='owner' AND rg.revoked_at IS NULL
   WHERE sp.user_id=$1::uuid AND sp.enabled=TRUE AND i.disabled_at IS NULL
  )
 `,requesterID).Scan(&requesterEligible); err!=nil || !requesterEligible {
  return errors.New("staff lifecycle requester is no longer an active owner")
 }
 var p struct {
  UserID string `json:"user_id"`
  Role string `json:"role"`
  Operation string `json:"operation"`
  Permission string `json:"permission"`
  Granted *bool `json:"granted"`
  Reason string `json:"reason"`
 }
 dec:=json.NewDecoder(strings.NewReader(string(payload)))
 dec.DisallowUnknownFields()
 if err=dec.Decode(&p);err!=nil{return errors.New("staff lifecycle payload is invalid")}
 p.UserID=strings.TrimSpace(p.UserID)
 if p.UserID=="" || p.UserID==requesterID || p.UserID==approvals[0] || p.UserID==approvals[1] {
  return errors.New("staff lifecycle target must be a different principal from requester and approvers")
 }
 if targetReference!=p.UserID { return errors.New("staff lifecycle target does not match the immutable request reference") }
 var targetExists bool
 if err=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_auth.staff_principals sp JOIN testagram_auth.identities i ON i.user_id=sp.user_id WHERE sp.user_id=$1::uuid AND i.disabled_at IS NULL)`,p.UserID).Scan(&targetExists);err!=nil || !targetExists {
  return errors.New("target must be an existing enabled native identity with a staff principal")
 }
 switch action {
 case "staff.role_change":
  if p.Operation!="grant" && p.Operation!="revoke" { return errors.New("role operation must be grant or revoke") }
  allowed:=map[string]bool{"administrator":true,"identity_reviewer":true,"engineer":true,"operations":true,"support":true,"moderator":true}
  if !allowed[p.Role] { return errors.New("role is not eligible for dual-control assignment") }
  if p.Operation=="grant" {
   tag,e:=tx.Exec(ctx,`
    INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
    SELECT $1::uuid,$2,$3::uuid
    WHERE NOT EXISTS(SELECT 1 FROM testagram_auth.staff_role_grants WHERE user_id=$1::uuid AND role=$2 AND revoked_at IS NULL)
   `,p.UserID,p.Role,requesterID)
   if e!=nil{return e};if tag.RowsAffected()!=1{return errors.New("target already has this active role")}
   for _,actor:=range approvals { if _,e=tx.Exec(ctx,`INSERT INTO testagram_auth.native_staff_lifecycle_events(request_id,actor_user_id,target_user_id,action,role) VALUES($1::uuid,$2::uuid,$3::uuid,'role_granted',$4)`,requestID,actor,p.UserID,p.Role);e!=nil{return e} }
  } else {
   tag,e:=tx.Exec(ctx,`UPDATE testagram_auth.staff_role_grants SET revoked_at=now(),revoked_by=$3::uuid WHERE user_id=$1::uuid AND role=$2 AND revoked_at IS NULL`,p.UserID,p.Role,requesterID)
   if e!=nil{return e};if tag.RowsAffected()!=1{return errors.New("active target role grant was not found")}
   for _,actor:=range approvals { if _,e=tx.Exec(ctx,`INSERT INTO testagram_auth.native_staff_lifecycle_events(request_id,actor_user_id,target_user_id,action,role) VALUES($1::uuid,$2::uuid,$3::uuid,'role_revoked',$4)`,requestID,actor,p.UserID,p.Role);e!=nil{return e} }
  }
 case "staff.revoke":
  tag,e:=tx.Exec(ctx,`UPDATE testagram_auth.staff_principals SET enabled=FALSE WHERE user_id=$1::uuid AND enabled=TRUE`,p.UserID)
  if e!=nil{return e};if tag.RowsAffected()!=1{return errors.New("target staff principal is already disabled")}
  if _,e=tx.Exec(ctx,`UPDATE testagram_auth.staff_role_grants SET revoked_at=now(),revoked_by=$2::uuid WHERE user_id=$1::uuid AND revoked_at IS NULL`,p.UserID,requesterID);e!=nil{return e}
  for _,actor:=range approvals { if _,e=tx.Exec(ctx,`INSERT INTO testagram_auth.native_staff_lifecycle_events(request_id,actor_user_id,target_user_id,action) VALUES($1::uuid,$2::uuid,$3::uuid,'staff_disabled')`,requestID,actor,p.UserID);e!=nil{return e} }
 case "staff.permission_change":
  if p.Granted==nil || strings.TrimSpace(p.Permission)=="" || len(p.Permission)>100 || len(strings.TrimSpace(p.Reason))<8 || len(p.Reason)>500 { return errors.New("permission change requires permission, granted flag and reason") }
  var permissionExists bool
  if e:=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM testagram_auth.admin_permissions WHERE permission_key=$1)`,p.Permission).Scan(&permissionExists);e!=nil || !permissionExists { return errors.New("permission key is not registered") }
  if _,e:=tx.Exec(ctx,`
   INSERT INTO testagram_auth.native_staff_permission_overrides(user_id,permission_key,granted,reason,changed_by_user_id)
   VALUES($1::uuid,$2,$3,$4,$5::uuid)
   ON CONFLICT(user_id,permission_key) DO UPDATE SET granted=EXCLUDED.granted,reason=EXCLUDED.reason,changed_by_user_id=EXCLUDED.changed_by_user_id,created_at=now()
  `,p.UserID,p.Permission,*p.Granted,p.Reason,requesterID);e!=nil{return e}
  for _,actor:=range approvals { if _,e:=tx.Exec(ctx,`INSERT INTO testagram_auth.native_staff_lifecycle_events(request_id,actor_user_id,target_user_id,action,metadata) VALUES($1::uuid,$2::uuid,$3::uuid,'permission_changed',jsonb_build_object('permission',$4,'granted',$5))`,requestID,actor,p.UserID,p.Permission,*p.Granted);e!=nil{return e} }
 case "staff.webauthn.enroll":
  if _,e:=tx.Exec(ctx,`
   INSERT INTO testagram_auth.staff_webauthn_enrollment_authorizations(user_id,authorized_by,authorization_method,reason,expires_at)
   VALUES($1::uuid,$2::uuid,'dual_control',$3,now()+interval '30 minutes')
  `,p.UserID,requesterID,"Two-person approved staff enrollment "+requestID);e!=nil{return fmt.Errorf("enrollment authorization could not be created: %w",e)}
  for _,actor:=range approvals { if _,e:=tx.Exec(ctx,`INSERT INTO testagram_auth.native_staff_lifecycle_events(request_id,actor_user_id,target_user_id,action) VALUES($1::uuid,$2::uuid,$3::uuid,'webauthn_enrollment_authorized')`,requestID,actor,p.UserID);e!=nil{return e} }
 default:
  return fmt.Errorf("no approved runtime executor is registered for action %q",action)
 }
 return nil
}
