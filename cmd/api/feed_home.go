package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

func parseFollowingOnly(value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func parseHomeFeedListID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if !socialUUIDPattern.MatchString(value) {
		return "", errors.New("invalid list ID")
	}
	return value, nil
}

func parseVideoOnly(value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func parseHomeFeedMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "for_you", nil
	}
	switch value {
	case "for_you", "following", "latest", "media", "discover":
		return value, nil
	default:
		return "", errors.New("unsupported feed mode")
	}
}

// homeFeedCursor identifies a position in one source's canonical keyspace.
// Native IDs remain UUIDs; ActivityPub uses canonical HTTPS object URIs and
// Bluesky uses AT URIs. Never coerce a remote identifier into a UUID.
type homeFeedCursor struct {
	Version   int                       `json:"v,omitempty"`
	CreatedAt time.Time                 `json:"created_at,omitempty"`
	Source    string                    `json:"source,omitempty"`
	ID        string                    `json:"id,omitempty"`
	ListID    string                    `json:"list_id,omitempty"`
	Sources   []homeFeedSourcePosition  `json:"sources,omitempty"`
	Pending   []homeFeedPendingIdentity `json:"pending,omitempty"`
}

// Sources stores the last fetched position for each source. Pending stores the
// already-ranked remainder of that bounded candidate window. Source positions
// must not advance past a window until every pending identity has been served
// or re-authorized and discarded; this prevents ranked pagination from skipping
// candidates when rank order differs from chronological order.
type homeFeedSourcePosition struct {
	Source    string    `json:"source"`
	Identity  string    `json:"identity,omitempty"`
	Cursor    string    `json:"cursor,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	ID        string    `json:"id,omitempty"`
}

type homeFeedPendingIdentity struct {
	Source    string    `json:"source"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	homeFeedSourceNative      = "native"
	homeFeedSourceActivityPub = "activitypub"
	homeFeedSourceBluesky     = "bluesky"
)

func validHomeFeedDID(did string) bool {
	if strings.HasPrefix(did, "did:plc:") {
		identifier := strings.TrimPrefix(did, "did:plc:")
		if len(identifier) != 24 {
			return false
		}
		for _, char := range identifier {
			if !(char >= 'a' && char <= 'z') && !(char >= '2' && char <= '7') {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(did, "did:web:") {
		identifier := strings.TrimPrefix(did, "did:web:")
		return identifier != "" && !strings.ContainsAny(identifier, " /?#@") && !strings.Contains(identifier, string([]byte{92}))
	}
	return false
}

func validHomeFeedCursorIdentity(source, id string) bool {
	unsafeWhitespace := string([]byte{13, 10, 9, 32})
	switch source {
	case "", homeFeedSourceNative:
		return localUUIDPattern.MatchString(id)
	case homeFeedSourceActivityPub:
		if len(id) < 12 || len(id) > 2048 || strings.ContainsAny(id, unsafeWhitespace) {
			return false
		}
		u, err := url.Parse(id)
		return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
	case homeFeedSourceBluesky:
		if !strings.HasPrefix(id, "at://") || len(id) > 2048 || strings.ContainsAny(id, unsafeWhitespace) {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(id, "at://"), "/")
		return len(parts) == 3 && validHomeFeedDID(parts[0]) && parts[1] == "app.bsky.feed.post" && parts[2] != ""
	default:
		return false
	}
}
func nativeSingleHomeFeedCursor(cursor *homeFeedCursor) bool {
	return cursor == nil || (cursor.Source == homeFeedSourceNative && cursor.ID != "" &&
		!cursor.CreatedAt.IsZero() && len(cursor.Sources) == 0 && len(cursor.Pending) == 0)
}

func homeFeedCursorMatchesList(cursor *homeFeedCursor, listID string) bool {
	return cursor == nil || cursor.ListID == listID
}

type homeFeedPost struct {
	ID                string            `json:"id"`
	UserID            string            `json:"user_id"`
	Content           string            `json:"content"`
	Visibility        string            `json:"visibility"`
	MediaIDs          []string          `json:"media_ids"`
	MediaURLs         []string          `json:"media_urls"`
	MediaContentTypes []string          `json:"media_content_types"`
	MediaAuthHeaders  map[string]string `json:"media_auth_headers,omitempty"`
	IsVideo           bool              `json:"is_video"`
	IsFollowing       bool              `json:"is_following"`
	Language          string            `json:"language"`
	CreatedAt         time.Time         `json:"created_at"`
	LikesCount        int64             `json:"likes_count"`
	RepliesCount      int64             `json:"replies_count"`
	RepostsCount      int64             `json:"reposts_count"`
	Author            map[string]any    `json:"author"`
	Origin            string            `json:"origin"`
	RankingScore      float64           `json:"ranking_score,omitempty"`
	RankingReasons    []string          `json:"ranking_reasons,omitempty"`
}

func encodeHomeFeedCursor(cursor homeFeedCursor) string {
	if cursor.Source == "" {
		cursor.Source = homeFeedSourceNative
	}
	cursor.Version = 2
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeHomeFeedCursor(value string) (*homeFeedCursor, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > 16384 {
		return nil, errors.New("invalid feed cursor")
	}
	var cursor homeFeedCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, errors.New("invalid feed cursor")
	}
	// Version 1 cursors predate source identity and are native-only. Accept
	// them for existing clients, but emit version 2 on the next page.
	if cursor.Version == 0 {
		cursor.Source = homeFeedSourceNative
		cursor.Version = 1
	}
	if cursor.Version != 1 && cursor.Version != 2 {
		return nil, errors.New("invalid feed cursor")
	}
	if cursor.Source == "" {
		cursor.Source = homeFeedSourceNative
	}
	if cursor.ID != "" && (cursor.CreatedAt.IsZero() || !validHomeFeedCursorIdentity(cursor.Source, cursor.ID)) {
		return nil, errors.New("invalid feed cursor")
	}
	if cursor.ID == "" && len(cursor.Sources) == 0 {
		return nil, errors.New("invalid feed cursor")
	}
	if len(cursor.Sources) > 120 || len(cursor.Pending) > 150 {
		return nil, errors.New("invalid feed cursor")
	}
	seenSources := make(map[string]struct{}, len(cursor.Sources))
	for _, position := range cursor.Sources {
		if position.Source != homeFeedSourceNative && position.Source != homeFeedSourceActivityPub && position.Source != homeFeedSourceBluesky {
			return nil, errors.New("invalid feed cursor")
		}
		if len(position.Identity) > 2048 || len(position.Cursor) > 2048 {
			return nil, errors.New("invalid feed cursor")
		}
		key := position.Source + "\x00" + position.Identity
		if _, exists := seenSources[key]; exists {
			return nil, errors.New("invalid feed cursor")
		}
		seenSources[key] = struct{}{}
		switch position.Source {
		case homeFeedSourceNative:
			if position.Identity != "" || position.Cursor != "" || position.CreatedAt.IsZero() || !validHomeFeedCursorIdentity(position.Source, position.ID) {
				return nil, errors.New("invalid feed cursor")
			}
		case homeFeedSourceActivityPub:
			if position.Identity != "" || position.Cursor == "" {
				return nil, errors.New("invalid feed cursor")
			}
		case homeFeedSourceBluesky:
			if !validHomeFeedDID(position.Identity) || position.Cursor == "" || position.ID != "" {
				return nil, errors.New("invalid feed cursor")
			}
		}
	}
	seenPending := make(map[string]struct{}, len(cursor.Pending))
	for _, item := range cursor.Pending {
		if item.CreatedAt.IsZero() || !validHomeFeedCursorIdentity(item.Source, item.ID) {
			return nil, errors.New("invalid feed cursor")
		}
		key := item.Source + "\x00" + item.ID
		if _, exists := seenPending[key]; exists {
			return nil, errors.New("invalid feed cursor")
		}
		seenPending[key] = struct{}{}
	}
	return &cursor, nil
}

func orderHomeFeedByRanking(posts []homeFeedPost, ranked []algorithmclient.RankedItem) ([]homeFeedPost, bool) {
	if len(posts) == 0 || len(ranked) != len(posts) {
		return posts, false
	}
	byID := make(map[string]homeFeedPost, len(posts))
	for _, post := range posts {
		if post.ID == "" {
			return posts, false
		}
		if _, duplicate := byID[post.ID]; duplicate {
			return posts, false
		}
		byID[post.ID] = post
	}
	ordered := make([]homeFeedPost, 0, len(posts))
	for _, item := range ranked {
		post, ok := byID[item.ID]
		if !ok {
			return posts, false
		}
		post.RankingScore = item.Score
		post.RankingReasons = item.Reasons
		ordered = append(ordered, post)
	}
	return ordered, true
}

func (s *service) homeFeed(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	w.Header().Set("Cache-Control", "private, no-store")
	userID, status, authErr := s.authenticatedMediaUser(r)
	if authErr != nil {
		writeNativeAuthFailure(w, status, authErr, requestID)
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_STORE_UNAVAILABLE", "Feed storage is unavailable", requestID))
		return
	}
	limit := 20
	feedMode, feedModeErr := parseHomeFeedMode(r.URL.Query().Get("feed"))
	if feedModeErr != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FEED_MODE", "feed must be for_you, following, latest, media, or discover", requestID))
		return
	}
	followingOnly, followingErr := parseFollowingOnly(r.URL.Query().Get("following_only"))
	if followingErr != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FOLLOWING_FILTER", "following_only must be a boolean", requestID))
		return
	}
	if feedMode == "following" {
		followingOnly = true
	} else if feedMode == "for_you" && followingOnly {
		feedMode = "following"
	}
	mediaOnly := feedMode == "media"
	discoverOnly := feedMode == "discover"
	rankingEnabled := feedMode != "latest"
	listID, listIDErr := parseHomeFeedListID(r.URL.Query().Get("list_id"))
	if listIDErr != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIST_ID", "list_id must be a valid list UUID", requestID))
		return
	}
	if discoverOnly && followingOnly {
		writeJSON(w, http.StatusBadRequest, errorBody("CONFLICTING_FEED_FILTERS", "discover cannot be combined with following_only=true", requestID))
		return
	}
	videoOnly, videoOnlyErr := parseVideoOnly(r.URL.Query().Get("video_only"))
	if videoOnlyErr != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_VIDEO_FILTER", "video_only must be a boolean", requestID))
		return
	}
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
  AND (NOT $5::boolean OR p.author_id=$1::uuid OR EXISTS (
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted'
  ))
  AND (NOT $7::boolean OR EXISTS (
      SELECT 1
      FROM jsonb_array_elements_text(COALESCE(p.media_ids,'[]'::jsonb)) AS media_key(object_key)
      JOIN testagram_media.assets ma
        ON ma.object_key=media_key.object_key
       AND ma.owner_id=p.author_id
       AND ma.status='ready'
  ))
  AND (NOT $8::boolean OR (
      p.author_id<>$1::uuid AND p.visibility='public' AND NOT a.protected_account
      AND NOT EXISTS (SELECT 1 FROM testagram_local.follows f
          WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted')
  ))
  AND (NOT $6::boolean OR EXISTS (
      SELECT 1
      FROM jsonb_array_elements_text(COALESCE(p.media_ids,'[]'::jsonb)) AS media_key(object_key)
      JOIN testagram_media.assets ma
        ON ma.object_key=media_key.object_key
       AND ma.owner_id=p.author_id
       AND ma.status='ready'
       AND ma.visibility='public'
       AND ma.content_type LIKE 'video/%'
  ))
  AND ($2::timestamptz IS NULL OR (p.created_at,p.id) < ($2::timestamptz,$3::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT $4`
	if listID != "" {
		// Keep the global feed independent of the optional Lists schema so an
		// existing database remains healthy until the additive Lists migration
		// is explicitly applied. The custom-feed branch is activated only after
		// the List access check above has succeeded.
		const cursorPredicate = "  AND ($2::timestamptz IS NULL OR (p.created_at,p.id) < ($2::timestamptz,$3::uuid))"
		listPredicate := `  AND EXISTS (
      SELECT 1 FROM testagram_local.social_list_members lm
      WHERE lm.list_id=$9::uuid AND lm.member_user_id=p.author_id
  )
` + cursorPredicate
		query = strings.Replace(query, cursorPredicate, listPredicate, 1)
	}
	var before any
	var beforeID any
	if !nativeSingleHomeFeedCursor(cursor) {
		writeJSON(w, http.StatusBadRequest, errorBody("CURSOR_SOURCE_MISMATCH", "This feed page cursor does not belong to the current native feed window", requestID))
		return
	}
	if !homeFeedCursorMatchesList(cursor, listID) {
		writeJSON(w, http.StatusBadRequest, errorBody("CURSOR_FEED_MISMATCH", "This cursor belongs to a different custom feed", requestID))
		return
	}
	var listIDArg any
	if listID != "" {
		// A list is a saved, user-controlled author set. Private lists remain
		// owner-only; public/unlisted lists are readable, but every post still
		// passes the normal account, follow, block, mute and visibility gates.
		var accessible bool
		listCtx, listCancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := s.db.QueryRow(listCtx, `SELECT true
FROM testagram_local.social_lists
WHERE id=$1::uuid AND deleted_at IS NULL
  AND (owner_id=$2::uuid OR visibility IN ('public','unlisted'))`, listID, userID).Scan(&accessible)
		listCancel()
		if err != nil {
			writeJSON(w, http.StatusNotFound, errorBody("FEED_NOT_FOUND", "Custom feed is unavailable", requestID))
			return
		}
		if !accessible {
			writeJSON(w, http.StatusNotFound, errorBody("FEED_NOT_FOUND", "Custom feed is unavailable", requestID))
			return
		}
		listIDArg = listID
	}
	if cursor != nil {
		before, beforeID = cursor.CreatedAt, cursor.ID
	}
	queryArgs := []any{userID, before, beforeID, limit + 1, followingOnly, videoOnly, mediaOnly, discoverOnly}
	if listIDArg != nil {
		queryArgs = append(queryArgs, listIDArg)
	}
	rows, err := s.db.Query(queryCtx, query, queryArgs...)
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
		post.Author = map[string]any{"id": post.UserID, "username": username, "display_name": displayName, "avatar_key": avatarKey, "avatar_url": publicMediaURL(avatarKey), "protected_account": protected}
		post.IsFollowing = following
		post.Origin = "local"
		quality := math.Min(1, float64(post.LikesCount+2*post.RepliesCount+2*post.RepostsCount)/50)
		conversation := math.Min(1, float64(post.RepliesCount)/20)
		posts = append(posts, post)
		candidates = append(candidates, algorithmclient.Candidate{
			ID: post.ID, AuthorID: post.UserID, Source: "native", CreatedAt: post.CreatedAt, Visible: true,
			Features: algorithmclient.Features{Following: following, Quality: quality, Conversation: conversation},
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_QUERY_FAILED", "Feed could not be loaded", requestID))
		return
	}
	rankingMode := "chronological"
	if rankingEnabled && s.algorithm != nil && len(candidates) > 0 {
		s.algorithmRequests.Add(1)
		rankCtx, cancel := context.WithTimeout(r.Context(), 1400*time.Millisecond)
		ranked, rankErr := s.algorithm.Rank(rankCtx, algorithmclient.Request{
			Now: time.Now().UTC(), FeedMode: feedMode, Candidates: candidates, Limit: limit, AllowSensitive: false,
		})
		cancel()
		if rankErr == nil {
			if ordered, complete := orderHomeFeedByRanking(posts, ranked.Items); complete {
				posts = ordered
				rankingMode = "algorithm"
			} else {
				// A partial/empty ranker response must never drop authorized posts
				// while the cursor advances past the complete chronological window.
				s.algorithmFailures.Add(1)
			}
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
		nextCursor = encodeHomeFeedCursor(homeFeedCursor{CreatedAt: last.CreatedAt, Source: homeFeedSourceNative, ID: last.ID, ListID: listID})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"data":  map[string]any{"posts": posts, "next_cursor": nextCursor, "has_more": hasMore, "ranking_mode": rankingMode, "feed_mode": feedMode, "video_only": videoOnly, "list_id": listID},
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
	StorageNamespace string
	StorageProvider  string
	R2Verified       bool
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
SELECT a.object_key, a.owner_id, a.visibility, a.content_type, a.storage_namespace,
       a.storage_provider,
       (CASE WHEN a.storage_provider='data-centre' THEN length(coalesce(a.checksum_sha256,''))=64
             ELSE EXISTS (
               SELECT 1 FROM testagram_media.storage_replicas sr
               WHERE sr.object_key=a.object_key AND sr.provider='cloudflare-r2' AND sr.state='verified'
             ) END) AS r2_verified
FROM testagram_media.assets a
WHERE a.status='ready' AND a.object_key = ANY($1::text[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	assets := make(map[string]homeFeedMediaAsset, len(ids)*2)
	for rows.Next() {
		var asset homeFeedMediaAsset
		if err := rows.Scan(&asset.ObjectKey, &asset.OwnerID, &asset.Visibility, &asset.ContentType, &asset.StorageNamespace, &asset.StorageProvider, &asset.R2Verified); err != nil {
			return err
		}
		assets[asset.ObjectKey] = asset
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range posts {
		posts[i].MediaURLs = []string{}
		posts[i].MediaContentTypes = []string{}
		posts[i].MediaAuthHeaders = make(map[string]string)
		posts[i].IsVideo = false
		for _, id := range posts[i].MediaIDs {
			asset, ok := assets[id]
			if !ok || asset.OwnerID != posts[i].UserID {
				continue
			}
			mediaURL := ""
			if asset.Visibility == "public" {
				mediaURL = publicMediaURLForStorage(asset.ObjectKey, asset.OwnerID, asset.StorageNamespace, asset.StorageProvider)
			} else if s.cdnMediaAssertions != nil && s.cdnMediaBaseURL != "" {
				if !asset.R2Verified {
					return errors.New("private media CDN origin is not verified; refusing direct-storage fallback")
				}
				assertion, _, issueErr := s.cdnMediaAssertions.IssueForViewer(asset.ObjectKey, asset.OwnerID, viewerID, time.Now())
				if issueErr != nil {
					return issueErr
				}
				mediaURL = s.cdnMediaBaseURL + "/v1/" + asset.ObjectKey
				posts[i].MediaAuthHeaders[mediaURL] = "Bearer " + assertion
			} else if asset.StorageNamespace != "legacy-media" && s.virtualStorage != nil {
				mediaURL, err = s.virtualStorage.DownloadURL(ctx, viewerID, asset.OwnerID, asset.StorageNamespace, asset.ObjectKey)
			} else if s.media != nil {
				mediaURL, err = s.media.PresignPrivateDownload(viewerID, asset.ObjectKey, 5*time.Minute, time.Now())
			}
			if err != nil {
				return err
			}
			if mediaURL != "" {
				posts[i].MediaURLs = append(posts[i].MediaURLs, mediaURL)
				posts[i].MediaContentTypes = append(posts[i].MediaContentTypes, asset.ContentType)
			}
			if strings.HasPrefix(strings.ToLower(asset.ContentType), "video/") {
				posts[i].IsVideo = true
			}
		}
	}
	return nil
}
