package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const socialListMaxMembers = 500

func socialListMemberCapacityExceeded(count int64, alreadyMember bool) bool {
	return !alreadyMember && count >= socialListMaxMembers
}

type socialListInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
}

type socialListMemberInput struct {
	UserID string `json:"user_id"`
}

func normalizeSocialListInput(in *socialListInput) bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Visibility = strings.ToLower(strings.TrimSpace(in.Visibility))
	if len([]rune(in.Name)) < 1 || len([]rune(in.Name)) > 64 || len([]rune(in.Description)) > 280 {
		return false
	}
	if in.Visibility == "" {
		in.Visibility = "private"
	}
	return in.Visibility == "private" || in.Visibility == "unlisted" || in.Visibility == "public"
}

func (s *service) socialListsList(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
SELECT l.id::text,l.owner_id::text,l.name,l.description,l.visibility,l.is_pinned,
       l.created_at,l.updated_at,count(m.member_user_id)::bigint
FROM testagram_local.social_lists l
LEFT JOIN testagram_local.social_list_members m ON m.list_id=l.id
WHERE l.owner_id=$1::uuid AND l.deleted_at IS NULL
GROUP BY l.id
ORDER BY l.is_pinned DESC,l.updated_at DESC,l.id DESC
LIMIT 100`, uid)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LISTS_UNAVAILABLE", "Lists could not be loaded", rid))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, owner, name, description, visibility string
		var pinned bool
		var created, updated time.Time
		var count int64
		if err := rows.Scan(&id, &owner, &name, &description, &visibility, &pinned, &created, &updated, &count); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LISTS_UNAVAILABLE", "List data could not be decoded", rid))
			return
		}
		items = append(items, map[string]any{"id": id, "owner_id": owner, "name": name, "description": description, "visibility": visibility, "pinned": pinned, "created_at": created, "updated_at": updated, "members_count": count})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LISTS_UNAVAILABLE", "Lists could not be loaded", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items}, "error": nil, "request_id": rid})
}

func (s *service) socialListCreate(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "social_list_create", 20) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in socialListInput
	if err := d.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST", "List payload is invalid", rid))
		return
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST", "List payload must contain one JSON object", rid))
		return
	}
	if !normalizeSocialListInput(&in) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST", "List name, description or visibility is invalid", rid))
		return
	}
	var id string
	var created time.Time
	err := s.db.QueryRow(r.Context(), `INSERT INTO testagram_local.social_lists(owner_id,name,description,visibility)
VALUES($1::uuid,$2,$3,$4) RETURNING id::text,created_at`, uid, in.Name, in.Description, in.Visibility).Scan(&id, &created)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_CREATE_FAILED", "List could not be created", rid))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": map[string]any{"id": id, "owner_id": uid, "name": in.Name, "description": in.Description, "visibility": in.Visibility, "created_at": created, "members_count": 0}, "error": nil, "request_id": rid})
}

func (s *service) socialListRead(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid list ID is required", rid))
		return
	}
	var owner, name, description, visibility string
	var pinned bool
	var created, updated time.Time
	var count int64
	err := s.db.QueryRow(r.Context(), `
SELECT l.owner_id::text,l.name,l.description,l.visibility,l.is_pinned,l.created_at,l.updated_at,
       (SELECT count(*) FROM testagram_local.social_list_members m WHERE m.list_id=l.id)
FROM testagram_local.social_lists l
WHERE l.id=$1::uuid AND l.deleted_at IS NULL
  AND (l.owner_id=$2::uuid OR l.visibility IN ('public','unlisted'))`, id, uid).Scan(&owner, &name, &description, &visibility, &pinned, &created, &updated, &count)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_NOT_FOUND", "List is unavailable", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"id": id, "owner_id": owner, "name": name, "description": description, "visibility": visibility, "pinned": pinned, "created_at": created, "updated_at": updated, "members_count": count, "is_owner": owner == uid}, "error": nil, "request_id": rid})
}

func (s *service) socialListMembers(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid list ID is required", rid))
		return
	}
	var visibility, owner string
	err := s.db.QueryRow(r.Context(), `SELECT owner_id::text,visibility FROM testagram_local.social_lists WHERE id=$1::uuid AND deleted_at IS NULL`, id).Scan(&owner, &visibility)
	if err != nil || (owner != uid && visibility == "private") {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_NOT_FOUND", "List is unavailable", rid))
		return
	}
	rows, err := s.db.Query(r.Context(), `
SELECT a.user_id::text,a.username,a.display_name,a.avatar_key,m.created_at
FROM testagram_local.social_list_members m
JOIN testagram_local.accounts a ON a.user_id=m.member_user_id
WHERE m.list_id=$1::uuid AND a.account_status='active'
  AND (NOT a.protected_account OR a.user_id=$2::uuid OR EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$2::uuid AND f.following_id=a.user_id AND f.status='accepted'))
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b WHERE (b.blocker_id=$2::uuid AND b.blocked_id=a.user_id) OR (b.blocker_id=a.user_id AND b.blocked_id=$2::uuid))
ORDER BY m.created_at DESC,m.member_user_id
LIMIT 500`, id, uid)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBERS_UNAVAILABLE", "List members could not be loaded", rid))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var member, username, display, avatar string
		var added time.Time
		if err := rows.Scan(&member, &username, &display, &avatar, &added); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBERS_UNAVAILABLE", "Member data could not be decoded", rid))
			return
		}
		items = append(items, map[string]any{"user_id": member, "username": username, "display_name": display, "avatar_key": avatar, "added_at": added, "is_self": member == uid})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBERS_UNAVAILABLE", "List members could not be loaded", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "owner_id": owner}, "error": nil, "request_id": rid})
}

func (s *service) socialListMemberAdd(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "social_list_member_add", 60) {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid list ID is required", rid))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in socialListMemberInput
	if err := d.Decode(&in); err != nil || d.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEMBER", "A valid user_id is required", rid))
		return
	}
	in.UserID = strings.ToLower(strings.TrimSpace(in.UserID))
	if !socialUUIDPattern.MatchString(in.UserID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEMBER", "A valid user_id is required", rid))
		return
	}
	if in.UserID == uid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MEMBER", "You cannot add yourself to your own list", rid))
		return
	}

	// Serialize additions per list so the 500-member cap cannot be exceeded
	// by concurrent requests. Lock and validate the active owner-controlled
	// list in the same transaction as membership creation.
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_ADD_FAILED", "Account could not be added", rid))
		return
	}
	defer tx.Rollback(context.Background())

	var owner string
	if err := tx.QueryRow(r.Context(), `SELECT owner_id::text FROM testagram_local.social_lists
WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, id).Scan(&owner); err != nil || owner != uid {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_NOT_FOUND", "List is unavailable", rid))
		return
	}

	// Match the canonical username-resolution privacy policy at the write
	// boundary too: hidden, protected-without-an-accepted-follow, inactive,
	// and blocked accounts cannot be added by guessing a UUID.
	var available bool
	err = tx.QueryRow(r.Context(), `
SELECT EXISTS(
  SELECT 1 FROM testagram_local.accounts a
  WHERE a.user_id=$2::uuid
    AND a.account_status='active'
    AND (a.discoverable_by_username OR EXISTS(
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1::uuid AND f.following_id=a.user_id AND f.status='accepted'
    ))
    AND (NOT a.protected_account OR EXISTS(
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1::uuid AND f.following_id=a.user_id AND f.status='accepted'
    ))
    AND NOT EXISTS(
      SELECT 1 FROM testagram_local.blocks b
      WHERE (b.blocker_id=$1::uuid AND b.blocked_id=a.user_id)
         OR (b.blocker_id=a.user_id AND b.blocked_id=$1::uuid)
    )
)`, uid, in.UserID).Scan(&available)
	if err != nil || !available {
		writeJSON(w, http.StatusNotFound, errorBody("MEMBER_UNAVAILABLE", "This account cannot be added", rid))
		return
	}

	var alreadyMember bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(
  SELECT 1 FROM testagram_local.social_list_members
  WHERE list_id=$1::uuid AND member_user_id=$2::uuid
)`, id, in.UserID).Scan(&alreadyMember); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_ADD_FAILED", "List membership could not be checked", rid))
		return
	}
	var count int64
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM testagram_local.social_list_members WHERE list_id=$1::uuid`, id).Scan(&count); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_ADD_FAILED", "List capacity could not be checked", rid))
		return
	}
	if socialListMemberCapacityExceeded(count, alreadyMember) {
		writeJSON(w, http.StatusConflict, errorBody("LIST_CAPACITY_REACHED", "A list can contain at most 500 accounts", rid))
		return
	}

	tag, err := tx.Exec(r.Context(), `INSERT INTO testagram_local.social_list_members(list_id,member_user_id,added_by)
VALUES($1::uuid,$2::uuid,$3::uuid)
ON CONFLICT(list_id,member_user_id) DO NOTHING`, id, in.UserID, uid)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_ADD_FAILED", "Account could not be added", rid))
		return
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM testagram_local.social_list_members WHERE list_id=$1::uuid AND member_user_id=$2::uuid)`, id, in.UserID).Scan(&exists); err != nil || !exists {
			writeJSON(w, http.StatusNotFound, errorBody("MEMBER_UNAVAILABLE", "Account is unavailable", rid))
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE testagram_local.social_lists SET updated_at=now() WHERE id=$1::uuid`, id)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_ADD_FAILED", "List could not be updated", rid))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_ADD_FAILED", "Account could not be added", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"list_id": id, "user_id": in.UserID, "added": tag.RowsAffected() == 1}, "error": nil, "request_id": rid})
}
func (s *service) socialListMemberRemove(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "social_list_member_remove", 60) {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	member := strings.ToLower(strings.TrimSpace(r.PathValue("member_id")))
	if !socialUUIDPattern.MatchString(id) || !socialUUIDPattern.MatchString(member) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_MEMBER", "Valid list and member IDs are required", rid))
		return
	}
	tag, err := s.db.Exec(r.Context(), `
DELETE FROM testagram_local.social_list_members m
USING testagram_local.social_lists l
WHERE m.list_id=l.id AND l.id=$1::uuid AND m.member_user_id=$2::uuid
  AND l.deleted_at IS NULL AND (l.owner_id=$3::uuid OR m.member_user_id=$3::uuid)`, id, member, uid)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_MEMBER_REMOVE_FAILED", "Member could not be removed", rid))
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_MEMBER_NOT_FOUND", "List member is unavailable", rid))
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE testagram_local.social_lists SET updated_at=now() WHERE id=$1::uuid`, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"list_id": id, "user_id": member, "removed": true}, "error": nil, "request_id": rid})
}

func (s *service) socialListDelete(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "social_list_delete", 20) {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid list ID is required", rid))
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE testagram_local.social_lists SET deleted_at=now(),updated_at=now()
WHERE id=$1::uuid AND owner_id=$2::uuid AND deleted_at IS NULL`, id, uid)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_DELETE_FAILED", "List could not be deleted", rid))
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_NOT_FOUND", "List is unavailable", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"id": id, "deleted": true}, "error": nil, "request_id": rid})
}
