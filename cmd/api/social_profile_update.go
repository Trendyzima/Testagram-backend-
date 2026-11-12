package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

type socialProfileUpdateRequest struct {
	DisplayName string            `json:"display_name"`
	Bio         string            `json:"bio"`
	Website     string            `json:"website"`
	Location    string            `json:"location"`
	AvatarKey   string            `json:"avatar_key"`
	CoverKey    string            `json:"cover_key"`
	SocialLinks map[string]string `json:"social_links"`
}

func validProfileWebsite(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") &&
		parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func validProfileMediaKey(value string) bool {
	if value == "" {
		return true
	}
	if len(value) < 40 || len(value) > 512 || !strings.HasPrefix(value, "users/") {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) != 3 || len(parts[1]) < 1 || len(parts[1]) > 128 || len(parts[2]) != 32 {
		return false
	}
	for _, r := range parts[1] {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' {
			return false
		}
	}
	for _, r := range parts[2] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validProfileSocialLinks(links map[string]string) bool {
	if len(links) > 5 {
		return false
	}
	for key, value := range links {
		switch key {
		case "x", "twitter", "instagram", "linkedin", "facebook":
		default:
			return false
		}
		value = strings.TrimSpace(value)
		if len(value) > 256 || strings.ContainsAny(value, "\r\n") {
			return false
		}
		if value != "" && strings.Contains(value, "://") && !validProfileWebsite(value) {
			return false
		}
	}
	return true
}

func (s *service) socialMyProfileUpdate(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-profile-update", 10) {
		return
	}
	var input socialProfileUpdateRequest
	if err := decodeMediaJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE", "Profile details must be valid JSON", requestID))
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Bio = strings.TrimSpace(input.Bio)
	input.Website = strings.TrimSpace(input.Website)
	input.Location = strings.TrimSpace(input.Location)
	input.AvatarKey = strings.TrimSpace(input.AvatarKey)
	input.CoverKey = strings.TrimSpace(input.CoverKey)
	if len(input.DisplayName) > 80 || len(input.Bio) > 500 || len(input.Website) > 2048 ||
		len(input.Location) > 120 || !validProfileWebsite(input.Website) ||
		!validProfileMediaKey(input.AvatarKey) || !validProfileMediaKey(input.CoverKey) ||
		!validProfileSocialLinks(input.SocialLinks) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE", "One or more profile fields are invalid", requestID))
		return
	}
	if input.SocialLinks == nil {
		input.SocialLinks = map[string]string{}
	}
	linksJSON, err := json.Marshal(input.SocialLinks)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE", "Social links could not be encoded", requestID))
		return
	}

	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UPDATE_UNAVAILABLE", "Profile could not be updated", requestID))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var activeID string
	err = tx.QueryRow(ctx, "SELECT user_id::text FROM testagram_local.accounts WHERE user_id=$1 AND account_status='active' FOR UPDATE", userID).Scan(&activeID)
	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot update its profile", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UPDATE_UNAVAILABLE", "Profile could not be updated", requestID))
		return
	}
	for _, key := range []string{input.AvatarKey, input.CoverKey} {
		if key == "" {
			continue
		}
		var owned bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM testagram_media.assets
			WHERE object_key=$1 AND owner_id=$2 AND visibility='public'
			  AND status='ready' AND content_type LIKE 'image/%'
		)`, key, userID).Scan(&owned)
		if err != nil || !owned {
			writeJSON(w, http.StatusBadRequest, errorBody("PROFILE_MEDIA_NOT_OWNED", "Choose an uploaded public image owned by your account", requestID))
			return
		}
	}
	_, err = tx.Exec(ctx, `UPDATE testagram_local.accounts
		SET display_name=$2, bio=$3, website=$4, location=$5, social_links=$6::jsonb,
		    avatar_key=$7, cover_key=$8, updated_at=now()
		WHERE user_id=$1`,
		userID, input.DisplayName, input.Bio, input.Website, input.Location,
		string(linksJSON), input.AvatarKey, input.CoverKey)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UPDATE_FAILED", "Profile could not be saved", requestID))
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UPDATE_FAILED", "Profile could not be saved", requestID))
		return
	}
	s.socialProfileReadFor(w, r, userID, userID, requestID)
}
