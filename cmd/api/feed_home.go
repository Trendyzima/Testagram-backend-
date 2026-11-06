package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

type homeFeedCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type homeFeedPost struct {
	ID             string         `json:"id"`
	UserID         string         `json:"user_id"`
	Content        string         `json:"content"`
	Visibility     string         `json:"visibility"`
	MediaIDs       []string       `json:"media_ids"`
	MediaURLs      []string       `json:"media_urls"`
	IsVideo        bool           `json:"is_video"`
	Language       string         `json:"language"`
	CreatedAt      time.Time      `json:"created_at"`
	LikesCount     int64          `json:"likes_count"`
	RepliesCount   int64          `json:"replies_count"`
	RepostsCount   int64          `json:"reposts_count"`
	Author         map[string]any `json:"author"`
	Origin         string         `json:"origin"`
	RankingScore   float64        `json:"ranking_score,omitempty"`
	RankingReasons []string       `json:"ranking_reasons,omitempty"`
}

func encodeHomeFeedCursor(cursor homeFeedCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeHomeFeedCursor(value string) (*homeFeedCursor, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > 1024 {
		return nil, errors.New("invalid feed cursor")
	}
	var cursor homeFeedCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.CreatedAt.IsZero() || !localUUIDPattern.MatchString(cursor.ID) {
		return nil, errors.New("invalid feed cursor")
	}
	return &cursor, nil
}

func (s *service) homeFeed(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	w.Header().Set("Cache-Control", "private, no-store")
	userID, status, authErr := s.authenticatedMediaUser(r)
	if authErr != nil {
		message := "A valid authenticated session is required"
		if status >= 500 {
			message = "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody("AUTH_REQUIRED", message, requestID))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_STORE_UNAVAILABLE", "Feed storage is unavailable", requestID))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Feed limit must be between 1 and 50", requestID))
			return
		}
		limit = parsed
	}
	cursor, err := decodeHomeFeedCursor(r.URL.Query().Get("before"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "Feed cursor is invalid", requestID))
		return
	}

	queryCtx, queryCancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer queryCancel()
	query := `
SELECT p.id::text, p.author_id::text, p.text, p.visibility, p.media_ids,
       p.language, p.created_at, p.likes_count, p.replies_count, p.reposts_count,
       a.username, a.display_name, a.avatar_key, a.protected_account,
       (p.author_id = $1::uuid OR EXISTS (
          SELECT 1 FROM testagram_local.follows f
          WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted'
       )) AS is_following
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
WHERE p.deleted_at IS NULL
  AND a.account_status='active'
  AND (p.author_id=$1::uuid
       OR (p.visibility='public' AND NOT a.protected_account)
       OR (p.visibility='followers' AND EXISTS (
          SELECT 1 FROM testagram_local.follows f
          WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted'
       )))
  AND NOT EXISTS (
      SELECT 1 FROM testagram_local.blocks b
      WHERE (b.blocker_id=$1::uuid AND b.blocked_id=p.author_id)
         OR (b.blocker_id=p.author_id AND b.blocked_id=$1::uuid)
  )
  AND NOT EXISTS (
      SELECT 1 FROM testagram_local.mutes m
      WHERE m.muter_id=$1::uuid AND m.muted_id=p.author_id
  )
  AND ($2::timestamptz IS NULL OR (p.created_at,p.id) < ($2::timestamptz,$3::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT $4`
	var before any
	var beforeID any
	if cursor != nil {
		before, beforeID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.Query(queryCtx, query, userID, before, beforeID, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_QUERY_FAILED", "Feed could not be loaded", requestID))
		return
	}
	defer rows.Close()

	posts := make([]homeFeedPost, 0, limit)
	candidates := make([]algorithmclient.Candidate, 0, limit)
	hasMore := false
	for rows.Next() {
		var post homeFeedPost
		var mediaRaw []byte
		var username, displayName, avatarKey string
		var protected, following bool
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &mediaRaw,
			&post.Language, &post.CreatedAt, &post.LikesCount, &post.RepliesCount, &post.RepostsCount,
			&username, &displayName, &avatarKey, &protected, &following); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_SCAN_FAILED", "Feed could not be loaded", requestID))
			return
		}
		if len(posts) == limit {
			hasMore = true
			break
		}
		post.MediaIDs = []string{}
		_ = json.Unmarshal(mediaRaw, &post.MediaIDs)
		post.Author = map[string]any{"id": post.UserID, "username": username, "display_name": displayName, "avatar_key": avatarKey, "protected_account": protected}
		post.Origin = "local"
		quality := math.Min(1, float64(post.LikesCount+2*post.RepliesCount+2*post.RepostsCount)/50)
		conversation := math.Min(1, float64(post.RepliesCount)/20)
		posts = append(posts, post)
		candidates = append(candidates, algorithmclient.Candidate{
			ID: post.ID, AuthorID: post.UserID, CreatedAt: post.CreatedAt, Visible: true,
			Features: algorithmclient.Features{Following: following, Quality: quality, Conversation: conversation},
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_QUERY_FAILED", "Feed could not be loaded", requestID))
		return
	}
	rankingMode := "chronological"
	if s.algorithm != nil && len(candidates) > 0 {
		s.algorithmRequests.Add(1)
		rankCtx, cancel := context.WithTimeout(r.Context(), 1400*time.Millisecond)
		ranked, rankErr := s.algorithm.Rank(rankCtx, algorithmclient.Request{
			Now: time.Now().UTC(), Candidates: candidates, Limit: limit, AllowSensitive: false,
		})
		cancel()
		if rankErr == nil {
			byID := make(map[string]homeFeedPost, len(posts))
			for _, post := range posts {
				byID[post.ID] = post
			}
			ordered := make([]homeFeedPost, 0, len(ranked.Items))
			used := make(map[string]struct{}, len(ranked.Items))
			for _, item := range ranked.Items {
				post, ok := byID[item.ID]
				if !ok {
					continue
				} // never hydrate or trust an ID outside this authorized candidate page
				if _, duplicate := used[item.ID]; duplicate {
					continue
				}
				used[item.ID] = struct{}{}
				post.RankingScore = item.Score
				post.RankingReasons = item.Reasons
				ordered = append(ordered, post)
			}
			posts = ordered
			rankingMode = "algorithm"
		} else {
			s.algorithmFailures.Add(1)
		}
	}
	// Re-check authorization after the external ranker call. A block, mute,
	// privacy change, or deletion may have happened while ranking was in flight.
	if len(posts) > 0 {
		checkCtx, checkCancel := context.WithTimeout(r.Context(), 2*time.Second)
		visible, checkErr := s.recheckHomeFeedVisibility(checkCtx, userID, posts)
		checkCancel()
		if checkErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_VISIBILITY_CHECK_FAILED", "Feed permissions could not be verified", requestID))
			return
		}
		posts = visible
	}
	if err := s.attachHomeFeedMediaURLs(r.Context(), userID, posts); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_MEDIA_FAILED", "Feed media could not be authorized", requestID))
		return
	}
	var nextCursor string
	if hasMore && len(candidates) > 0 {
		// Cursor is derived from the oldest row in the chronological candidate window,
		// not from the rank order. This prevents score ordering from corrupting pagination.
		// Re-fetch the chronological boundary using the last candidate timestamp and ID.
		last := candidates[len(candidates)-1]
		nextCursor = encodeHomeFeedCursor(homeFeedCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"data":  map[string]any{"posts": posts, "next_cursor": nextCursor, "has_more": hasMore, "ranking_mode": rankingMode},
		"error": nil, "request_id": requestID,
	})
}

func (s *service) recheckHomeFeedVisibility(ctx context.Context, userID string, posts []homeFeedPost) ([]homeFeedPost, error) {
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	rows, err := s.db.Query(ctx, `
SELECT p.id::text
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
WHERE p.id::text = ANY($2::text[])
  AND p.deleted_at IS NULL
  AND a.account_status='active'
  AND (p.author_id=$1::uuid
       OR (p.visibility='public' AND NOT a.protected_account)
       OR (p.visibility='followers' AND EXISTS (
          SELECT 1 FROM testagram_local.follows f
          WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted'
       )))
  AND NOT EXISTS (
      SELECT 1 FROM testagram_local.blocks b
      WHERE (b.blocker_id=$1::uuid AND b.blocked_id=p.author_id)
         OR (b.blocker_id=p.author_id AND b.blocked_id=$1::uuid)
  )
  AND NOT EXISTS (
      SELECT 1 FROM testagram_local.mutes m
      WHERE m.muter_id=$1::uuid AND m.muted_id=p.author_id
  )`, userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := make(map[string]struct{}, len(posts))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]homeFeedPost, 0, len(visible))
	for _, post := range posts {
		if _, ok := visible[post.ID]; ok {
			out = append(out, post)
		}
	}
	return out, nil
}

type homeFeedMediaAsset struct {
	ObjectKey        string
	OwnerID          string
	Visibility       string
	ContentType      string
	ExternalID       string
	StorageNamespace string
}

func (s *service) attachHomeFeedMediaURLs(ctx context.Context, viewerID string, posts []homeFeedPost) error {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, post := range posts {
		for _, id := range post.MediaIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	mediaCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := s.db.Query(mediaCtx, `
SELECT object_key, owner_id, visibility, content_type,
       COALESCE(external_media_id::text,''), storage_namespace
FROM testagram_media.assets
WHERE status='ready'
  AND (object_key = ANY($1::text[]) OR external_media_id::text = ANY($1::text[]))`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	assets := make(map[string]homeFeedMediaAsset, len(ids)*2)
	for rows.Next() {
		var asset homeFeedMediaAsset
		if err := rows.Scan(&asset.ObjectKey, &asset.OwnerID, &asset.Visibility, &asset.ContentType, &asset.ExternalID, &asset.StorageNamespace); err != nil {
			return err
		}
		assets[asset.ObjectKey] = asset
		if asset.ExternalID != "" {
			assets[asset.ExternalID] = asset
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range posts {
		posts[i].MediaURLs = []string{}
		posts[i].IsVideo = false
		// Private/follower posts never expose public media URLs to a non-owner.
		if posts[i].Visibility != "public" && posts[i].UserID != viewerID {
			continue
		}
		for _, id := range posts[i].MediaIDs {
			asset, ok := assets[id]
			if !ok || asset.OwnerID != posts[i].UserID {
				continue
			}
			mediaURL := ""
			if asset.Visibility == "public" {
				mediaURL = publicMediaURLForStorage(asset.ObjectKey, asset.OwnerID, asset.StorageNamespace)
			} else if posts[i].UserID == viewerID {
				if asset.StorageNamespace != "legacy-media" && s.virtualStorage != nil {
					mediaURL, err = s.virtualStorage.DownloadURL(ctx, viewerID, asset.OwnerID, asset.StorageNamespace, asset.ObjectKey)
				} else if s.media != nil {
					mediaURL, err = s.media.PresignPrivateDownload(viewerID, asset.ObjectKey, 5*time.Minute, time.Now())
				}
				if err != nil {
					return err
				}
			}
			if mediaURL != "" {
				posts[i].MediaURLs = append(posts[i].MediaURLs, mediaURL)
			}
			if strings.HasPrefix(strings.ToLower(asset.ContentType), "video/") {
				posts[i].IsVideo = true
			}
		}
	}
	return nil
}
