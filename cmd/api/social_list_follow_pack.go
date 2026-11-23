package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const socialListFollowBatchMax = 50

func parseSocialListFollowLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return socialListFollowBatchMax, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > socialListFollowBatchMax {
		return 0, errors.New("limit must be between 1 and 50")
	}
	return limit, nil
}

// socialListFollowMembers turns a public/unlisted curated List into a
// consent-preserving Starter Pack action. Public accounts are followed;
// protected accounts receive ordinary follow requests. Work is bounded so
// large packs can be processed in repeatable batches without a huge lock set.
func (s *service) socialListFollowMembers(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-list-follow-pack", 5) {
		return
	}

	listID := strings.ToLower(strings.TrimSpace(r.PathValue("list_id")))
	if !socialUUIDPattern.MatchString(listID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "A valid List ID is required", requestID))
		return
	}
	limit, err := parseSocialListFollowLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", err.Error(), requestID))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack could not be processed", requestID))
		return
	}
	defer tx.Rollback(ctx)

	var ownerID, visibility string
	err = tx.QueryRow(ctx, `SELECT owner_id::text,visibility
FROM testagram_local.social_lists
WHERE id=$1::uuid AND deleted_at IS NULL
FOR SHARE`, listID).Scan(&ownerID, &visibility)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && visibility == "private" && ownerID != userID) {
		writeJSON(w, http.StatusNotFound, errorBody("LIST_NOT_FOUND", "Starter Pack is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack could not be checked", requestID))
		return
	}

	rows, err := tx.Query(ctx, `SELECT a.user_id::text,a.protected_account
FROM testagram_local.social_list_members lm
JOIN testagram_local.accounts a ON a.user_id=lm.member_user_id
WHERE lm.list_id=$1::uuid
  AND a.user_id<>$2::uuid
  AND a.account_status='active'
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$2::uuid AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$2::uuid))
  AND NOT EXISTS(SELECT 1 FROM testagram_local.follows f
    WHERE f.follower_id=$2::uuid AND f.following_id=a.user_id
      AND f.status IN ('accepted','pending','blocked'))
ORDER BY lm.created_at,a.user_id
LIMIT $3
FOR SHARE OF lm`, listID, userID, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack members could not be loaded", requestID))
		return
	}
	type packMember struct {
		id        string
		protected bool
	}
	members := make([]packMember, 0, limit)
	for rows.Next() {
		var member packMember
		if err := rows.Scan(&member.id, &member.protected); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack members could not be decoded", requestID))
			return
		}
		members = append(members, member)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack members could not be loaded", requestID))
		return
	}

	lockIDs := make([]string, 0, len(members)+1)
	lockIDs = append(lockIDs, userID)
	for _, member := range members {
		lockIDs = append(lockIDs, member.id)
	}
	if len(members) > 0 {
		lockedRows, lockErr := tx.Query(ctx, `SELECT user_id::text
FROM testagram_local.accounts
WHERE user_id=ANY($1::uuid[])
ORDER BY user_id
FOR UPDATE`, lockIDs)
		if lockErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack accounts could not be locked", requestID))
			return
		}
		locked := make(map[string]bool, len(lockIDs))
		for lockedRows.Next() {
			var id string
			if scanErr := lockedRows.Scan(&id); scanErr != nil {
				lockedRows.Close()
				writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack account locks could not be read", requestID))
				return
			}
			locked[id] = true
		}
		lockErr = lockedRows.Err()
		lockedRows.Close()
		if lockErr != nil || len(locked) != len(lockIDs) {
			writeJSON(w, http.StatusConflict, errorBody("LIST_MEMBERS_CHANGED", "Starter Pack membership changed; refresh and try again", requestID))
			return
		}
	}

	var followedCount, requestedCount, skippedCount int
	for _, member := range members {
		if member.id == userID {
			skippedCount++
			continue
		}
		var targetStatus string
		var protected bool
		if err := tx.QueryRow(ctx, `SELECT account_status,protected_account
FROM testagram_local.accounts WHERE user_id=$1::uuid`, member.id).Scan(&targetStatus, &protected); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack account status could not be checked", requestID))
			return
		}
		if targetStatus != "active" {
			skippedCount++
			continue
		}
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testagram_local.blocks
WHERE (blocker_id=$1::uuid AND blocked_id=$2::uuid)
   OR (blocker_id=$2::uuid AND blocked_id=$1::uuid))`, userID, member.id).Scan(&blocked); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack block state could not be checked", requestID))
			return
		}
		if blocked {
			skippedCount++
			continue
		}

		desired := "accepted"
		if protected {
			desired = "pending"
		}
		var previous string
		prevErr := tx.QueryRow(ctx, `SELECT status FROM testagram_local.follows
WHERE follower_id=$1::uuid AND following_id=$2::uuid
FOR UPDATE`, userID, member.id).Scan(&previous)
		if prevErr == nil {
			if previous == "blocked" {
				skippedCount++
				continue
			}
			if previous == "accepted" || previous == "pending" {
				skippedCount++
				continue
			}
		} else if !errors.Is(prevErr, pgx.ErrNoRows) {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Existing follow state could not be checked", requestID))
			return
		}

		tag, execErr := tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
VALUES($1::uuid,$2::uuid,$3)
ON CONFLICT(follower_id,following_id) DO UPDATE
SET status=EXCLUDED.status,updated_at=now()
WHERE testagram_local.follows.status<>'blocked'`, userID, member.id, desired)
		if execErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack follow could not be saved", requestID))
			return
		}
		if tag.RowsAffected() == 0 {
			skippedCount++
			continue
		}

		var createdAt time.Time
		if err := tx.QueryRow(ctx, `SELECT created_at FROM testagram_local.follows
WHERE follower_id=$1::uuid AND following_id=$2::uuid`, userID, member.id).Scan(&createdAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack follow timestamp could not be read", requestID))
			return
		}
		kind := "follow"
		if desired == "pending" {
			kind = "follow_request"
			requestedCount++
		} else {
			followedCount++
		}
		eventKey := fmt.Sprintf("follow:%s:%s:%d", userID, member.id, createdAt.UnixNano())
		if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,event_key)
VALUES($1::uuid,$2::uuid,$3,$4)
ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL
DO UPDATE SET kind=EXCLUDED.kind,actor_id=EXCLUDED.actor_id,created_at=now()`,
			member.id, userID, kind, eventKey); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack notifications could not be saved", requestID))
			return
		}
	}

	var remainingCount, alreadyFollowingCount int
	if err := tx.QueryRow(ctx, `SELECT
  count(*) FILTER (WHERE NOT EXISTS(
    SELECT 1 FROM testagram_local.follows f
    WHERE f.follower_id=$2::uuid AND f.following_id=lm.member_user_id
      AND f.status IN ('accepted','pending','blocked')
  ) AND NOT EXISTS(
    SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$2::uuid AND b.blocked_id=lm.member_user_id)
       OR (b.blocker_id=lm.member_user_id AND b.blocked_id=$2::uuid)
  ))::int,
  count(*) FILTER (WHERE EXISTS(
    SELECT 1 FROM testagram_local.follows f
    WHERE f.follower_id=$2::uuid AND f.following_id=lm.member_user_id
      AND f.status IN ('accepted','pending')
  ))::int
FROM testagram_local.social_list_members lm
JOIN testagram_local.accounts a ON a.user_id=lm.member_user_id
WHERE lm.list_id=$1::uuid AND lm.member_user_id<>$2::uuid
  AND a.account_status='active'`, listID, userID).Scan(&remainingCount, &alreadyFollowingCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack progress could not be calculated", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("LIST_FOLLOW_FAILED", "Starter Pack could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"list_id": listID,
			"followed_count": followedCount,
			"requested_count": requestedCount,
			"already_following_count": alreadyFollowingCount,
			"skipped_count": skippedCount,
			"remaining_count": remainingCount,
			"has_more": remainingCount > 0,
			"batch_limit": limit,
		},
		"error": nil,
		"request_id": requestID,
	})
}
