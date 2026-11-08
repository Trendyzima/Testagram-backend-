package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type communityProfileUpdate struct {
	Name        *string  `json:"display_name"`
	Description *string  `json:"description"`
	IconURL     *string  `json:"icon_url"`
	BannerURL   *string  `json:"banner_url"`
	Category    *string  `json:"category"`
	Rules       []string `json:"rules"`
}

type communityPostInput struct {
	Text     string   `json:"text"`
	MediaIDs []string `json:"media_ids"`
	Language string   `json:"language"`
}

func (s *service) communityIdentity(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
		return "", true
	}
	userID, _, ok := s.socialAuth(w, r)
	return userID, ok
}

func (s *service) communityCanView(ctx context.Context, communityID, userID string) (bool, string, error) {
	var visibility string
	err := s.db.QueryRow(ctx, "SELECT visibility FROM testagram_local.communities WHERE id=$1::uuid", communityID).Scan(&visibility)
	if err != nil {
		return false, "", err
	}
	if visibility == "public" {
		return true, visibility, nil
	}
	if userID == "" {
		return false, visibility, nil
	}
	var member bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testagram_local.community_memberships
		WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active')`, communityID, userID).Scan(&member)
	return member, visibility, err
}

func (s *service) communityDetail(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_DATABASE_UNAVAILABLE", "Community data service is unavailable", id))
		return
	}
	userID, ok := s.communityIdentity(w, r)
	if !ok {
		return
	}
	slug := strings.ToLower(strings.TrimSpace(r.PathValue("slug")))
	var communityID, name, description, visibility, iconURL, bannerURL, ownerID, category string
	var members, posts int64
	var rulesJSON, pinnedJSON []byte
	var createdAt time.Time
	err := s.db.QueryRow(r.Context(), `SELECT id::text,name,description,visibility,icon_url,banner_url,
		owner_id::text,category,member_count,post_count,rules,pinned_post_ids,created_at
		FROM testagram_local.communities WHERE slug=$1`, slug).Scan(
		&communityID, &name, &description, &visibility, &iconURL, &bannerURL,
		&ownerID, &category, &members, &posts, &rulesJSON, &pinnedJSON, &createdAt)
	if err == pgx.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_LOOKUP_FAILED", "Community is unavailable", id))
		return
	}
	allowed, _, err := s.communityCanView(r.Context(), communityID, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_ACCESS_FAILED", "Community access could not be verified", id))
		return
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	var rules, pinned []string
	_ = json.Unmarshal(rulesJSON, &rules)
	_ = json.Unmarshal(pinnedJSON, &pinned)
	isMember := false
	role := ""
	if userID != "" {
		_ = s.db.QueryRow(r.Context(), `SELECT role FROM testagram_local.community_memberships
			WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'`, communityID, userID).Scan(&role)
		isMember = role != ""
	}
	community := map[string]any{
		"id": communityID, "name": slug, "display_name": name, "description": description,
		"visibility": visibility, "is_private": visibility == "private", "icon_url": iconURL,
		"banner_url": bannerURL, "owner_id": ownerID, "created_by": ownerID, "category": category,
		"member_count": members, "post_count": posts, "rules": rules, "pinned_post_ids": pinned,
		"created_at": createdAt, "is_member": isMember, "my_role": role,
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"community": community}})
}

func (s *service) communityMembers(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_DATABASE_UNAVAILABLE", "Community data service is unavailable", id))
		return
	}
	userID, ok := s.communityIdentity(w, r)
	if !ok {
		return
	}
	communityID := r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_COMMUNITY_ID", "Community ID is invalid", id))
		return
	}
	allowed, _, err := s.communityCanView(r.Context(), communityID, userID)
	if err == pgx.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_ACCESS_FAILED", "Community access could not be verified", id))
		return
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil {
			if n < 1 { n = 1 }
			if n > 100 { n = 100 }
			limit = n
		}
	}
	rows, err := s.db.Query(r.Context(), `SELECT m.user_id::text,a.username,a.display_name,a.avatar_key,m.role,m.joined_at
		FROM testagram_local.community_memberships m
		JOIN testagram_local.accounts a ON a.user_id=m.user_id
		WHERE m.community_id=$1::uuid AND m.status='active'
		ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'moderator' THEN 2 ELSE 3 END,m.joined_at
		LIMIT $2`, communityID, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_MEMBERS_FAILED", "Community members could not be loaded", id))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var memberID, username, displayName, avatarKey, role string
		var joinedAt time.Time
		if err = rows.Scan(&memberID, &username, &displayName, &avatarKey, &role, &joinedAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_MEMBERS_FAILED", "Community member records could not be read", id))
			return
		}
		items = append(items, map[string]any{"id": memberID, "user_id": memberID, "username": username,
			"display_name": displayName, "avatar_url": avatarKey, "role": role, "status": "active", "joined_at": joinedAt})
	}
	if err = rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_MEMBERS_FAILED", "Community member records could not be read", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "next_cursor": nil}})
}

func (s *service) communityPosts(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_DATABASE_UNAVAILABLE", "Community data service is unavailable", id))
		return
	}
	userID, ok := s.communityIdentity(w, r)
	if !ok {
		return
	}
	communityID := r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_COMMUNITY_ID", "Community ID is invalid", id))
		return
	}
	allowed, visibility, err := s.communityCanView(r.Context(), communityID, userID)
	if err == pgx.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_ACCESS_FAILED", "Community access could not be verified", id))
		return
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil {
			if n < 1 { n = 1 }
			if n > 100 { n = 100 }
			limit = n
		}
	}
	rows, err := s.db.Query(r.Context(), `SELECT p.id::text,p.author_id::text,a.username,a.display_name,a.avatar_key,
		p.text,p.media_ids,p.visibility,p.likes_count,p.replies_count,p.reposts_count,p.created_at
		FROM testagram_local.posts p JOIN testagram_local.accounts a ON a.user_id=p.author_id
		WHERE p.community_id=$1::uuid AND p.deleted_at IS NULL
		AND ($2<>'public' OR p.visibility='public')
		ORDER BY p.created_at DESC,p.id DESC LIMIT $3`, communityID, visibility, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_POSTS_FAILED", "Community posts could not be loaded", id))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var postID, authorID, username, displayName, avatarKey, body, visibility string
		var mediaJSON []byte
		var likes, replies, reposts int64
		var createdAt time.Time
		if err = rows.Scan(&postID, &authorID, &username, &displayName, &avatarKey, &body, &mediaJSON,
			&visibility, &likes, &replies, &reposts, &createdAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_POSTS_FAILED", "Community post records could not be read", id))
			return
		}
		var mediaIDs []string
		_ = json.Unmarshal(mediaJSON, &mediaIDs)
		items = append(items, map[string]any{"id": postID, "author_id": authorID, "content": body,
			"text": body, "media_ids": mediaIDs, "visibility": visibility, "likes_count": likes,
			"replies_count": replies, "reposts_count": reposts, "created_at": createdAt,
			"user_profiles": map[string]any{"id": authorID, "username": username, "display_name": displayName, "avatar_url": avatarKey}})
	}
	if err = rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("COMMUNITY_POSTS_FAILED", "Community post records could not be read", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "next_cursor": nil}})
}

func (s *service) communityPostCreate(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := s.socialAuth(w, r)
	if !ok { return }
	if !s.socialWriteAllowed(w, r, userID, id, "community_post_create", 20) { return }
	communityID := r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_COMMUNITY_ID", "Community ID is invalid", id)); return
	}
	var member bool
	err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM testagram_local.community_memberships
		WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active')`, communityID, userID).Scan(&member)
	if err != nil || !member {
		writeJSON(w, http.StatusForbidden, errorBody("COMMUNITY_MEMBERSHIP_REQUIRED", "Join this community before posting", id)); return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	defer r.Body.Close()
	var in communityPostInput
	if err = decodeSingleJSON(r.Body, &in); err != nil || len(in.Text)>10000 || len(in.MediaIDs)>20 || len(in.Language)>32 || (strings.TrimSpace(in.Text)=="" && len(in.MediaIDs)==0) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_COMMUNITY_POST", "Community post fields are invalid", id)); return
	}
	if in.MediaIDs==nil { in.MediaIDs=[]string{} }
	mediaJSON,_:=json.Marshal(in.MediaIDs)
	var postID string
	err=s.db.QueryRow(r.Context(), `INSERT INTO testagram_local.posts(author_id,community_id,text,visibility,media_ids,language)
		VALUES($1::uuid,$2::uuid,$3,'public',$4::jsonb,$5) RETURNING id::text`,userID,communityID,in.Text,string(mediaJSON),in.Language).Scan(&postID)
	if err!=nil { writeJSON(w,http.StatusConflict,errorBody("COMMUNITY_POST_CREATE_FAILED","Community post could not be saved",id));return }
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":postID,"community_id":communityID,"author_id":userID,"text":in.Text,"content":in.Text,"visibility":"public"}})
}

func (s *service) communityProfileUpdate(w http.ResponseWriter, r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r); if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_update",10){return}
	communityID:=r.PathValue("community_id")
	if !socialUUIDPattern.MatchString(communityID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_ID","Community ID is invalid",id));return}
	var role string
	err:=s.db.QueryRow(r.Context(),`SELECT role FROM testagram_local.community_memberships
		WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'`,communityID,userID).Scan(&role)
	if err!=nil || (role!="owner" && role!="admin") {writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_ADMIN_REQUIRED","Community administrator access is required",id));return}
	r.Body=http.MaxBytesReader(w,r.Body,16<<10);defer r.Body.Close()
	var in communityProfileUpdate
	if err=decodeSingleJSON(r.Body,&in);err!=nil {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_UPDATE","Community update is invalid",id));return}
	if in.Name!=nil && (len(strings.TrimSpace(*in.Name))<3 || len(*in.Name)>80) {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_NAME","Community display name is invalid",id));return}
	if in.Description!=nil && len(*in.Description)>2000 || in.IconURL!=nil && len(*in.IconURL)>2048 || in.BannerURL!=nil && len(*in.BannerURL)>2048 || in.Category!=nil && (len(*in.Category)<1 || len(*in.Category)>48) || len(in.Rules)>20 {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_UPDATE","One or more community fields exceed their limits",id));return
	}
	var rulesJSON any
	if in.Rules!=nil { for _,rule:=range in.Rules {if len(rule)>240 {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_RULES","Community rules must be at most 240 characters each",id));return}}; encoded,_:=json.Marshal(in.Rules);rulesJSON=string(encoded) }
	_,err=s.db.Exec(r.Context(),`UPDATE testagram_local.communities SET
		name=COALESCE($2,name),description=COALESCE($3,description),icon_url=COALESCE($4,icon_url),
		banner_url=COALESCE($5,banner_url),category=COALESCE($6,category),
		rules=COALESCE($7::jsonb,rules),updated_at=now() WHERE id=$1::uuid`,
		communityID,nullableTrimmed(in.Name),nullableString(in.Description),nullableString(in.IconURL),nullableString(in.BannerURL),nullableString(in.Category),rulesJSON)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_UPDATE_FAILED","Community profile could not be updated",id));return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"updated":true,"community_id":communityID}})
}

func nullableTrimmed(value *string) any {
	if value==nil{return nil}
	trimmed:=strings.TrimSpace(*value)
	return trimmed
}
func nullableString(value *string) any {
	if value==nil{return nil}
	return *value
}

func (s *service) communityPostModerate(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"community_post_moderate",20){return}
	communityID,postID:=r.PathValue("community_id"),r.PathValue("post_id")
	if !socialUUIDPattern.MatchString(communityID)||!socialUUIDPattern.MatchString(postID){writeJSON(w,http.StatusBadRequest,errorBody("INVALID_COMMUNITY_POST_ID","Community or post ID is invalid",id));return}
	var role string
	err:=s.db.QueryRow(r.Context(),`SELECT role FROM testagram_local.community_memberships
		WHERE community_id=$1::uuid AND user_id=$2::uuid AND status='active'`,communityID,userID).Scan(&role)
	if err!=nil || (role!="owner" && role!="admin" && role!="moderator"){writeJSON(w,http.StatusForbidden,errorBody("COMMUNITY_MODERATOR_REQUIRED","Community moderator access is required",id));return}
	tag,err:=s.db.Exec(r.Context(),`UPDATE testagram_local.posts SET deleted_at=now(),updated_at=now()
		WHERE id=$1::uuid AND community_id=$2::uuid AND deleted_at IS NULL`,postID,communityID)
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("COMMUNITY_POST_MODERATION_FAILED","Post could not be moderated",id));return}
	if tag.RowsAffected()==0 {http.NotFound(w,r);return}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"post_id":postID,"moderated":true}})
}
