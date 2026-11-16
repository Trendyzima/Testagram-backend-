package main

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
	"github.com/Trendyzima/Testagram-backend-/internal/federationclient"
)

type homeFeedRemoteSubscription struct {
	Protocol string
	Identity string
	Handle   string
}

func homeFeedSourcePositionFor(cursor *homeFeedCursor, source, identity string) (homeFeedSourcePosition, bool) {
	if cursor == nil {
		return homeFeedSourcePosition{}, false
	}
	for _, position := range cursor.Sources {
		if position.Source == source && position.Identity == identity {
			return position, true
		}
	}
	return homeFeedSourcePosition{}, false
}

func upsertHomeFeedSourcePosition(positions []homeFeedSourcePosition, next homeFeedSourcePosition) []homeFeedSourcePosition {
	for i := range positions {
		if positions[i].Source == next.Source && positions[i].Identity == next.Identity {
			positions[i] = next
			return positions
		}
	}
	return append(positions, next)
}

func (s *service) loadHomeFeedRemoteSubscriptions(ctx context.Context, userID string) ([]homeFeedRemoteSubscription, error) {
	rows, err := s.db.Query(ctx, `
SELECT protocol, canonical_identity, display_handle
FROM testagram_local.remote_author_subscriptions
WHERE local_user_id=$1::uuid
  AND subscription_status='active'
  AND verification_status='verified'
ORDER BY created_at DESC, id DESC
LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]homeFeedRemoteSubscription, 0, 100)
	for rows.Next() {
		var item homeFeedRemoteSubscription
		if err := rows.Scan(&item.Protocol, &item.Identity, &item.Handle); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// fetchHomeFeedRemoteCandidates retrieves at most one post per selected remote
// author and one ActivityPub page item. This deliberate one-item-per-source
// budget means every fetched remote identity is either returned in this page
// or represented by an advanced per-source cursor; no ranked remainder can be
// lost between page requests. Remote source positions remain independent.
func (s *service) fetchHomeFeedRemoteCandidates(
	ctx context.Context,
	userID string,
	cursor *homeFeedCursor,
	feedMode string,
	limit int,
	videoOnly bool,
) ([]homeFeedPost, []algorithmclient.Candidate, []homeFeedSourcePosition, int, int, bool, error) {
	if s.federationClient == nil || feedMode == "media" || feedMode == "discover" || videoOnly || limit <= 1 {
		return nil, nil, nil, 0, 0, false, nil
	}
	subscriptions, err := s.loadHomeFeedRemoteSubscriptions(ctx, userID)
	if err != nil {
		return nil, nil, nil, 0, 0, false, err
	}
	activityPub := make([]homeFeedRemoteSubscription, 0)
	bluesky := make([]homeFeedRemoteSubscription, 0)
	for _, item := range subscriptions {
		switch item.Protocol {
		case "activitypub":
			if validRemoteAuthorIdentity(item.Protocol, item.Identity) {
				activityPub = append(activityPub, item)
			}
		case "bluesky":
			if validRemoteAuthorIdentity(item.Protocol, item.Identity) {
				bluesky = append(bluesky, item)
			}
		}
	}
	if len(activityPub) == 0 && len(bluesky) == 0 {
		return nil, nil, nil, 0, 0, false, nil
	}

	remoteBudget := limit - 1
	if remoteBudget > 5 {
		remoteBudget = 5
	}
	positions := append([]homeFeedSourcePosition(nil), cursorPositions(cursor)...)
	remoteOffset := 0
	if cursor != nil && cursor.RemoteOffset >= 0 {
		remoteOffset = cursor.RemoteOffset
	}
	posts := make([]homeFeedPost, 0, remoteBudget)
	candidates := make([]algorithmclient.Candidate, 0, remoteBudget)
	reserved := 0
	hasMore := false

	apPosition, apPositionExists := homeFeedSourcePositionFor(cursor, homeFeedSourceActivityPub, "")
	apSlot := len(activityPub) > 0 && remoteBudget > 0 && !apPosition.Exhausted
	blueskySlots := remoteBudget
	alternateSingleSlot := remoteBudget == 1 && len(activityPub) > 0 && len(bluesky) > 0
	if apSlot {
		if alternateSingleSlot {
			apSlot = remoteOffset%2 == 0
		}
	}
	if apSlot {
		reserved++
		actorURIs := make([]string, 0, len(activityPub))
		handleByActor := make(map[string]string, len(activityPub))
		for _, actor := range activityPub {
			actorURIs = append(actorURIs, actor.Identity)
			handleByActor[actor.Identity] = actor.Handle
		}
		page, fetchErr := s.federationClient.FetchRemoteObjects(ctx, federationclient.RemoteObjectsRequest{
			ActorURIs: actorURIs,
			Limit:     1,
			Cursor:    apPosition.Cursor,
		})
		if fetchErr != nil {
			hasMore = true
			if !apPositionExists {
				apPosition = homeFeedSourcePosition{Source: homeFeedSourceActivityPub}
			}
			positions = upsertHomeFeedSourcePosition(positions, apPosition)
		} else {
			apPosition = homeFeedSourcePosition{
				Source: homeFeedSourceActivityPub,
				Cursor: page.NextCursor,
				Exhausted: !page.HasMore,
			}
			positions = upsertHomeFeedSourcePosition(positions, apPosition)
			if page.HasMore {
				hasMore = true
			}
			for _, item := range page.Items {
				createdAt := item.CreatedAt
				if item.PublishedAt != nil && !item.PublishedAt.IsZero() {
					createdAt = *item.PublishedAt
				}
				if createdAt.IsZero() || item.ObjectURI == "" || item.ActorURI == "" {
					continue
				}
				handle := handleByActor[item.ActorURI]
				if handle == "" {
					if parsed, parseErr := url.Parse(item.ActorURI); parseErr == nil {
						handle = parsed.Hostname()
					}
				}
				post := homeFeedPost{
					ID: item.ObjectURI, UserID: item.ActorURI, Content: item.Content,
					Visibility: item.Visibility, CreatedAt: createdAt.UTC(), IsFollowing: true,
					Author: map[string]any{"id": item.ActorURI, "username": handle, "display_name": handle},
					Origin: homeFeedSourceActivityPub,
					Capabilities: map[string]bool{"like": false, "reply": false, "repost": false, "quote": false, "bookmark": false},
				}
				posts = append(posts, post)
				candidates = append(candidates, algorithmclient.Candidate{
					ID: post.ID, AuthorID: post.UserID, Source: homeFeedSourceActivityPub,
					SourceIdentity: post.UserID, CreatedAt: post.CreatedAt, Visible: true,
					Features: algorithmclient.Features{Following: true, Quality: 0.1},
				})
			}
		}
	}
	if alternateSingleSlot {
		blueskySlots = 0
		if !apSlot {
			blueskySlots = 1
		}
	} else if apSlot {
		blueskySlots--
	}
	if blueskySlots > 4 {
		blueskySlots = 4
	}
	if blueskySlots > len(bluesky) {
		blueskySlots = len(bluesky)
	}
	if alternateSingleSlot && apSlot {
		remoteOffset = (remoteOffset + 1) % (2 * len(bluesky))
	}
	if blueskySlots > 0 {
		start := remoteOffset
		if alternateSingleSlot {
			start = (remoteOffset / 2) % len(bluesky)
		} else {
			start %= len(bluesky)
		}
		selected := make([]homeFeedRemoteSubscription, 0, blueskySlots)
		for i := 0; i < blueskySlots; i++ {
			selected = append(selected, bluesky[(start+i)%len(bluesky)])
		}
		actorDIDs := make([]string, 0, len(selected))
		cursors := make(map[string]string, len(selected))
		selectedByDID := make(map[string]homeFeedRemoteSubscription, len(selected))
		for _, actor := range selected {
			position, exists := homeFeedSourcePositionFor(cursor, homeFeedSourceBluesky, actor.Identity)
			if exists && position.Exhausted {
				continue
			}
			actorDIDs = append(actorDIDs, actor.Identity)
			selectedByDID[actor.Identity] = actor
			if exists && position.Cursor != "" {
				cursors[actor.Identity] = position.Cursor
			}
			reserved++
		}
		if len(actorDIDs) > 0 {
			feeds, fetchErr := s.federationClient.FetchATProtoFeeds(ctx, federationclient.ATProtoFeedRequest{
				ActorDIDs: actorDIDs, LimitPerActor: 1, Cursors: cursors,
			})
			if fetchErr != nil {
				hasMore = true
				for _, did := range actorDIDs {
					position, exists := homeFeedSourcePositionFor(cursor, homeFeedSourceBluesky, did)
					if !exists {
						position = homeFeedSourcePosition{Source: homeFeedSourceBluesky, Identity: did}
					}
					positions = upsertHomeFeedSourcePosition(positions, position)
				}
			} else {
				for _, actorFeed := range feeds {
					position := homeFeedSourcePosition{
						Source: homeFeedSourceBluesky, Identity: actorFeed.DID,
						Cursor: actorFeed.Cursor, Exhausted: actorFeed.Cursor == "",
					}
					positions = upsertHomeFeedSourcePosition(positions, position)
					if actorFeed.Cursor != "" {
						hasMore = true
					}
					if len(actorFeed.Posts) == 0 {
						continue
					}
					remote := actorFeed.Posts[0]
					createdAt, parseErr := time.Parse(time.RFC3339Nano, remote.CreatedAt)
					if parseErr != nil {
						createdAt, parseErr = time.Parse(time.RFC3339Nano, remote.IndexedAt)
					}
					if parseErr != nil || remote.URI == "" || remote.Author.DID != actorFeed.DID {
						continue
					}
					handle := actorFeed.Handle
					displayName := remote.Author.DisplayName
					if displayName == "" {
						displayName = handle
					}
					post := homeFeedPost{
						ID: remote.URI, UserID: actorFeed.DID, Content: remote.Text,
						Visibility: "public", CreatedAt: createdAt.UTC(), IsFollowing: true,
						LikesCount: remote.LikeCount, RepliesCount: remote.ReplyCount, RepostsCount: remote.RepostCount,
						Author: map[string]any{"id": actorFeed.DID, "username": handle, "display_name": displayName},
						Origin: "atproto",
						Capabilities: map[string]bool{"like": false, "reply": false, "repost": false, "quote": false, "bookmark": false},
					}
					posts = append(posts, post)
					quality := 0.0
					if remote.LikeCount+remote.ReplyCount+remote.RepostCount > 0 {
						quality = 0.2
					}
					candidates = append(candidates, algorithmclient.Candidate{
						ID: post.ID, AuthorID: post.UserID, Source: "atproto",
						SourceIdentity: post.UserID, CreatedAt: post.CreatedAt, Visible: true,
						Features: algorithmclient.Features{Following: true, Quality: quality, Conversation: min(1, float64(remote.ReplyCount)/20)},
					})
				}
			}
		}
		if alternateSingleSlot {
			remoteOffset = (remoteOffset + 1) % (2 * len(bluesky))
		} else {
			remoteOffset = (start + len(selected)) % len(bluesky)
		}
	}
	for _, actor := range bluesky {
		position, exists := homeFeedSourcePositionIn(positions, homeFeedSourceBluesky, actor.Identity)
		if !exists || !position.Exhausted {
			hasMore = true
			break
		}
	}
	// Keep the source positions bounded and deterministic in the encoded cursor.
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Source != positions[j].Source {
			return positions[i].Source < positions[j].Source
		}
		return positions[i].Identity < positions[j].Identity
	})
	return posts, candidates, positions, reserved, remoteOffset, hasMore, nil
}

func homeFeedSourcePositionIn(positions []homeFeedSourcePosition, source, identity string) (homeFeedSourcePosition, bool) {
	for _, position := range positions {
		if position.Source == source && position.Identity == identity {
			return position, true
		}
	}
	return homeFeedSourcePosition{}, false
}

func cursorPositions(cursor *homeFeedCursor) []homeFeedSourcePosition {
	if cursor == nil {
		return nil
	}
	return append([]homeFeedSourcePosition(nil), cursor.Sources...)
}

func (s *service) recheckRemoteHomeFeedVisibility(ctx context.Context, userID string, posts []homeFeedPost) ([]homeFeedPost, error) {
	if len(posts) == 0 {
		return posts, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT protocol, canonical_identity
FROM testagram_local.remote_author_subscriptions
WHERE local_user_id=$1::uuid
  AND subscription_status='active'
  AND verification_status='verified'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := make(map[string]struct{})
	for rows.Next() {
		var protocol, identity string
		if err := rows.Scan(&protocol, &identity); err != nil {
			return nil, err
		}
		allowed[protocol+"\x00"+identity] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	visible := make([]homeFeedPost, 0, len(posts))
	for _, post := range posts {
		protocol := post.Origin
		if protocol == "atproto" {
			protocol = "bluesky"
		}
		if _, ok := allowed[protocol+"\x00"+post.UserID]; ok {
			visible = append(visible, post)
		}
	}
	return visible, nil
}
