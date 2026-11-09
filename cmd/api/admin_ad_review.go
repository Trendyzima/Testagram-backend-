package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) requireAdReviewAdmin(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	reqID := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", reqID))
		return "", reqID, false
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("ADMIN_DATABASE_UNAVAILABLE", "Admin service is unavailable", reqID))
		return "", reqID, false
	}
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 8 {
		writeJSON(w, http.StatusUnauthorized, errorBody("AUTHENTICATION_REQUIRED", "A valid Appwrite session is required", reqID))
		return "", reqID, false
	}
	userID, status, err := s.authenticatedAppwritePrivilegedUser(r, parts[1])
	if err != nil {
		code := "ADMIN_AUTHENTICATION_FAILED"
		if status == http.StatusForbidden {
			code = "ADMIN_MFA_REQUIRED"
		}
		writeJSON(w, status, errorBody(code, err.Error(), reqID))
		return "", reqID, false
	}

	ownerID := strings.TrimSpace(os.Getenv("TESTAGRAM_PLATFORM_OWNER_USER_ID"))
	if ownerID != "" && userID == ownerID {
		return userID, reqID, true
	}
	var allowed bool
	err = s.db.QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1
		  FROM testagram_identity.staff_permissions
		  WHERE user_id = $1::uuid
		    AND permission_key = 'platform.ads'
		    AND revoked_at IS NULL
		)
	`, userID).Scan(&allowed)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("ADMIN_PERMISSION_CHECK_FAILED", "Admin permission could not be verified", reqID))
		return "", reqID, false
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, errorBody("ADMIN_PERMISSION_REQUIRED", "The platform.ads permission is required", reqID))
		return "", reqID, false
	}
	return userID, reqID, true
}

func (s *service) adReviewQueue(w http.ResponseWriter, r *http.Request) {
	_, reqID, ok := s.requireAdReviewAdmin(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "pending"
	}
	if status != "pending" && status != "approved" && status != "rejected" && status != "all" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_MODERATION_STATUS", "Status must be pending, approved, rejected or all", reqID))
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REVIEW_LIMIT", "Limit must be between 1 and 200", reqID))
			return
		}
		limit = n
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT c.id::text, a.id::text, a.name, a.owner_user_id::text,
		       c.name, c.objective, c.status, c.payment_status, c.currency,
		       c.lifetime_budget_micros, c.funded_micros, c.starts_at, c.ends_at,
		       c.created_at, c.moderation_status, c.moderation_note,
		       cr.id::text, cr.headline, cr.body, cr.asset_url, cr.click_through_url
		FROM testagram_local.ad_campaigns c
		JOIN testagram_local.advertisers a ON a.id = c.advertiser_id
		LEFT JOIN LATERAL (
		  SELECT creative.id, creative.headline, creative.body, creative.asset_url, creative.click_through_url
		  FROM testagram_local.ad_creatives creative
		  WHERE creative.campaign_id = c.id AND creative.enabled
		  ORDER BY creative.created_at ASC
		  LIMIT 1
		) cr ON true
		WHERE $1 = 'all' OR c.moderation_status = $1
		ORDER BY c.created_at DESC
		LIMIT $2
	`, status, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_QUEUE_FAILED", "Ad review queue could not be loaded", reqID))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var campaignID, advertiserID, advertiserName, ownerID string
		var campaignName, objective, campaignStatus, paymentStatus, currency string
		var moderationStatus string
		var budget, funded int64
		var startsAt, createdAt time.Time
		var endsAt *time.Time
		var moderationNote, creativeID, headline, creativeBody, assetURL, clickURL *string
		if err := rows.Scan(
			&campaignID, &advertiserID, &advertiserName, &ownerID,
			&campaignName, &objective, &campaignStatus, &paymentStatus, &currency,
			&budget, &funded, &startsAt, &endsAt, &createdAt, &moderationStatus, &moderationNote,
			&creativeID, &headline, &creativeBody, &assetURL, &clickURL,
		); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_QUEUE_FAILED", "Ad review records could not be read", reqID))
			return
		}
		creative := any(nil)
		if creativeID != nil {
			creative = map[string]any{
				"id": *creativeID, "headline": valueOrEmpty(headline),
				"body": valueOrEmpty(creativeBody), "asset_url": valueOrEmpty(assetURL),
				"click_through_url": valueOrEmpty(clickURL),
			}
		}
		items = append(items, map[string]any{
			"campaign_id": campaignID, "advertiser_id": advertiserID,
			"advertiser_name": advertiserName, "owner_user_id": ownerID,
			"campaign_name": campaignName, "objective": objective,
			"campaign_status": campaignStatus, "payment_status": paymentStatus,
			"currency": currency, "lifetime_budget_micros": budget, "funded_micros": funded,
			"starts_at": startsAt, "ends_at": endsAt, "created_at": createdAt,
			"moderation_status": moderationStatus, "moderation_note": valueOrEmpty(moderationNote),
			"creative": creative,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_QUEUE_FAILED", "Ad review records could not be read", reqID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{"items": items},
		"error": nil, "request_id": reqID,
	})
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *service) reviewAdCampaign(w http.ResponseWriter, r *http.Request) {
	userID, reqID, ok := s.requireAdReviewAdmin(w, r)
	if !ok {
		return
	}
	campaignID := strings.ToLower(strings.TrimSpace(r.PathValue("campaign_id")))
	if !socialUUIDPattern.MatchString(campaignID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CAMPAIGN_ID", "Campaign ID is invalid", reqID))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	defer r.Body.Close()
	var input struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REVIEW_REQUEST", "Review request is invalid", reqID))
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REVIEW_REQUEST", "Review request must contain one JSON object", reqID))
		return
	}
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Note = strings.TrimSpace(input.Note)
	if input.Decision != "approved" && input.Decision != "rejected" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REVIEW_DECISION", "Decision must be approved or rejected", reqID))
		return
	}
	if input.Decision == "rejected" && input.Note == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("REJECTION_REASON_REQUIRED", "A rejection reason is required", reqID))
		return
	}
	if len(input.Note) > 500 {
		writeJSON(w, http.StatusBadRequest, errorBody("REVIEW_NOTE_TOO_LONG", "Review note must be 500 characters or fewer", reqID))
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_FAILED", "Campaign could not be reviewed", reqID))
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var ownerID, campaignName, previousStatus, paymentStatus string
	var endsAt *time.Time
	err = tx.QueryRow(r.Context(), `
		SELECT a.owner_user_id::text, c.name, c.status, c.payment_status, c.ends_at
		FROM testagram_local.ad_campaigns c
		JOIN testagram_local.advertisers a ON a.id = c.advertiser_id
		WHERE c.id = $1::uuid
		FOR UPDATE OF c
	`, campaignID).Scan(&ownerID, &campaignName, &previousStatus, &paymentStatus, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_FAILED", "Campaign could not be loaded", reqID))
		return
	}

	now := time.Now().UTC()
	resultingStatus := "pending_payment"
	switch {
	case input.Decision == "rejected":
		resultingStatus = "rejected"
	case paymentStatus == "funded" && endsAt != nil && !endsAt.After(now):
		resultingStatus = "completed"
	case paymentStatus == "funded" && previousStatus == "paused":
		resultingStatus = "paused"
	case paymentStatus == "funded":
		resultingStatus = "active"
	}
	var note any
	if input.Note != "" {
		note = input.Note
	}
	_, err = tx.Exec(r.Context(), `
		UPDATE testagram_local.ad_campaigns
		SET moderation_status = $2,
		    moderation_note = $3,
		    moderated_by = $4::uuid,
		    moderated_at = now(),
		    status = $5,
		    updated_at = now()
		WHERE id = $1::uuid
	`, campaignID, input.Decision, note, userID, resultingStatus)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_FAILED", "Campaign review could not be saved", reqID))
		return
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO testagram_local.ad_review_audit(
		  campaign_id, advertiser_owner_id, actor_user_id, decision, note,
		  previous_status, resulting_status, payment_status
		) VALUES($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8)
	`, campaignID, ownerID, userID, input.Decision, note, previousStatus, resultingStatus, paymentStatus)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_AUDIT_FAILED", "Campaign review audit could not be recorded", reqID))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AD_REVIEW_FAILED", "Campaign review could not be committed", reqID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"success": true, "campaign_id": campaignID,
			"moderation_status": input.Decision, "campaign_status": resultingStatus,
			"payment_status": paymentStatus, "campaign_name": campaignName,
		},
		"error": nil, "request_id": reqID,
	})
}

func (s *service) updateAdReviewPermission(w http.ResponseWriter, r *http.Request) {
	userID, reqID, ok := s.requireAdReviewAdmin(w, r)
	if !ok {
		return
	}
	ownerID := strings.TrimSpace(os.Getenv("TESTAGRAM_PLATFORM_OWNER_USER_ID"))
	if ownerID == "" || userID != ownerID {
		writeJSON(w, http.StatusForbidden, errorBody("PLATFORM_OWNER_REQUIRED", "Only the configured platform owner can change staff permissions", reqID))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input struct {
		UserID       string `json:"user_id"`
		Permission   string `json:"permission_key"`
		Enabled      bool   `json:"enabled"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PERMISSION_REQUEST", "Permission request is invalid", reqID))
		return
	}
	targetID := strings.ToLower(strings.TrimSpace(input.UserID))
	if !socialUUIDPattern.MatchString(targetID) || input.Permission != "platform.ads" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PERMISSION_REQUEST", "A valid user ID and platform.ads permission are required", reqID))
		return
	}
	if input.Enabled {
		_, err := s.db.Exec(r.Context(), `
			INSERT INTO testagram_identity.staff_permissions(user_id, permission_key, granted_by, granted_at, revoked_at)
			VALUES($1::uuid, $2, $3::uuid, now(), NULL)
			ON CONFLICT(user_id, permission_key) DO UPDATE
			SET granted_by = EXCLUDED.granted_by, granted_at = now(), revoked_at = NULL
		`, targetID, input.Permission, userID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PERMISSION_UPDATE_FAILED", "Staff permission could not be granted", reqID))
			return
		}
	} else {
		_, err := s.db.Exec(r.Context(), `
			UPDATE testagram_identity.staff_permissions
			SET revoked_at = now()
			WHERE user_id = $1::uuid AND permission_key = $2 AND revoked_at IS NULL
		`, targetID, input.Permission)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PERMISSION_UPDATE_FAILED", "Staff permission could not be revoked", reqID))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"user_id": targetID, "permission_key": input.Permission, "enabled": input.Enabled},
		"error": nil, "request_id": reqID,
	})
}
