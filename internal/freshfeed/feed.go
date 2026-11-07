package freshfeed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

const (
	pageFetchLimit = 100
	maxScanned     = 500
)

type Store interface {
	ListDocumentsWithQueries(context.Context, string, []string, int) ([]json.RawMessage, error)
}
type Ranker interface {
	Rank(context.Context, algorithmclient.Request) (algorithmclient.Response, error)
}
type MediaSigner interface {
	DownloadURL(context.Context, string, string) (string, error)
}
type Service struct {
	store         Store
	ranker        Ranker
	media         MediaSigner
	now           func() time.Time
	requireRanker bool
}
type Result struct {
	Posts       []map[string]any `json:"posts"`
	NextCursor  string           `json:"next_cursor"`
	HasMore     bool             `json:"has_more"`
	RankingMode string           `json:"ranking_mode"`
}
type candidatePost struct {
	id        string
	authorID  string
	created   time.Time
	visible   bool
	sensitive bool
	following bool
	raw       map[string]any
	score     float64
	reasons   []string
}

func New(store Store, ranker Ranker, media MediaSigner) (*Service, error) {
	if store == nil {
		return nil, errors.New("fresh feed requires an Appwrite document store")
	}
	return &Service{store: store, ranker: ranker, media: media, now: time.Now}, nil
}

func (s *Service) RequireRanker(required bool) *Service {
	s.requireRanker = required
	return s
}

func (s *Service) Ready(ctx context.Context) error {
	if s.store == nil {
		return errors.New("Appwrite feed store is not configured")
	}
	probe := "testagram_ready_probe"
	checks := []struct {
		collection string
		queries    []string
	}{
		{"posts", []string{equalQuery("status", "published"), orderDescQuery("created_at")}},
		{"posts", []string{equalQuery("$id", probe)}},
		{"follows", []string{equalQuery("follower_id", probe), equalQuery("followed_id", probe), equalQuery("state", "accepted")}},
		{"blocks", []string{equalQuery("blocker_id", probe), equalQuery("blocked_id", probe)}},
		{"blocks", []string{equalQuery("blocked_id", probe), equalQuery("blocker_id", probe)}},
		{"account_settings", []string{equalQuery("user_id", probe)}},
		{"profiles", []string{equalQuery("user_id", probe)}},
		{"post_media", []string{equalQuery("post_id", probe), orderAscQuery("position")}},
	}
	for _, check := range checks {
		if _, err := s.store.ListDocumentsWithQueries(ctx, check.collection, check.queries, 1); err != nil {
			return fmt.Errorf("Appwrite feed schema not ready for %s: %w", check.collection, err)
		}
	}
	if s.requireRanker {
		ranker, ok := s.ranker.(interface{ Ready(context.Context) error })
		if !ok {
			return errors.New("required Algorithm client does not expose readiness verification")
		}
		if err := ranker.Ready(ctx); err != nil {
			return fmt.Errorf("required Algorithm service is not ready: %w", err)
		}
	}
	return nil
}

func (s *Service) Home(ctx context.Context, viewerID string, limit int, cursor string) (Result, error) {
	if strings.TrimSpace(viewerID) == "" || len(viewerID) > 36 {
		return Result{}, errors.New("invalid feed viewer")
	}
	if limit < 1 || limit > 50 {
		return Result{}, errors.New("feed limit must be between 1 and 50")
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return Result{}, err
	}

	candidates := make([]candidatePost, 0, limit)
	authorCounts := map[string]int{}
	lastScanned := after
	scanned := 0
	hasMore := false
	for scanned < maxScanned && len(candidates) < limit {
		queries := []string{equalQuery("status", "published"), orderDescQuery("created_at")}
		if lastScanned != "" {
			queries = append(queries, cursorAfterQuery(lastScanned))
		}
		rawPage, listErr := s.store.ListDocumentsWithQueries(ctx, "posts", queries, pageFetchLimit)
		if listErr != nil {
			return Result{}, fmt.Errorf("load published posts: %w", listErr)
		}
		if len(rawPage) == 0 {
			break
		}
		page := make([]map[string]any, 0, len(rawPage))
		for _, raw := range rawPage {
			doc, parseErr := decodeDocument(raw)
			if parseErr != nil {
				return Result{}, errors.New("Appwrite returned an invalid post document")
			}
			page = append(page, doc)
		}
		authorIDs := make([]string, 0, len(page))
		seenAuthors := map[string]struct{}{}
		for _, post := range page {
			author := stringField(post, "author_id")
			if author != "" {
				if _, ok := seenAuthors[author]; !ok {
					seenAuthors[author] = struct{}{}
					authorIDs = append(authorIDs, author)
				}
			}
		}
		settings, settingErr := s.settingsByUser(ctx, authorIDs)
		if settingErr != nil {
			return Result{}, fmt.Errorf("load account visibility: %w", settingErr)
		}
		following, blocked, relationshipErr := s.relationshipsForAuthors(ctx, viewerID, authorIDs)
		if relationshipErr != nil {
			return Result{}, fmt.Errorf("load candidate relationships: %w", relationshipErr)
		}
		for _, post := range page {
			if len(candidates) >= limit || scanned >= maxScanned {
				hasMore = true
				break
			}
			scanned++
			lastScanned = stringField(post, "$id")
			if lastScanned == "" {
				return Result{}, errors.New("Appwrite post has no document ID")
			}
			authorID := stringField(post, "author_id")
			if authorID == "" {
				continue
			}
			if _, denied := blocked[authorID]; denied {
				continue
			}
			_, isFollowing := following[authorID]
			if !visibleToViewer(post, settings[authorID], viewerID, authorID, isFollowing) {
				continue
			}
			if boolField(post, "sensitive") {
				continue
			}
			createdAt, parseErr := time.Parse(time.RFC3339Nano, stringField(post, "created_at"))
			if parseErr != nil {
				continue
			}
			if authorCounts[authorID] >= 2 {
				continue
			}
			authorCounts[authorID]++
			candidates = append(candidates, candidatePost{id: lastScanned, authorID: authorID, created: createdAt, visible: true, following: isFollowing, raw: post})
		}
		if len(candidates) >= limit {
			hasMore = len(page) == pageFetchLimit || scanned < len(page)
			break
		}
		if len(page) < pageFetchLimit {
			break
		}
		if scanned >= maxScanned {
			hasMore = true
			break
		}
	}
	if len(candidates) == 0 {
		return Result{Posts: []map[string]any{}, RankingMode: "chronological", HasMore: hasMore, NextCursor: encodeCursor(lastScanned)}, nil
	}

	mode := "chronological"
	if s.ranker == nil && s.requireRanker {
		return Result{}, errors.New("required Algorithm ranker is not configured")
	}
	if s.ranker != nil {
		input := algorithmclient.Request{Now: s.now().UTC(), Limit: len(candidates), AllowSensitive: false, Candidates: make([]algorithmclient.Candidate, 0, len(candidates))}
		byID := make(map[string]*candidatePost, len(candidates))
		for i := range candidates {
			c := &candidates[i]
			input.Candidates = append(input.Candidates, algorithmclient.Candidate{ID: c.id, AuthorID: c.authorID, CreatedAt: c.created, Visible: c.visible, Sensitive: c.sensitive, Features: algorithmclient.Features{Following: c.following, Quality: 0.5}})
			byID[c.id] = c
		}
		ranked, rankErr := s.ranker.Rank(ctx, input)
		if rankErr != nil && s.requireRanker {
			return Result{}, fmt.Errorf("required Algorithm ranker unavailable: %w", rankErr)
		}
		if rankErr == nil && len(ranked.Items) == len(candidates) {
			ordered := make([]candidatePost, 0, len(candidates))
			valid := true
			for _, item := range ranked.Items {
				candidate, ok := byID[item.ID]
				if !ok {
					valid = false
					break
				}
				candidate.score = item.Score
				candidate.reasons = append([]string(nil), item.Reasons...)
				ordered = append(ordered, *candidate)
			}
			if valid && len(ordered) == len(candidates) {
				candidates = ordered
				mode = "algorithm"
			} else if s.requireRanker {
				return Result{}, errors.New("required Algorithm ranker returned an incompatible candidate set")
			}
		} else if rankErr == nil && s.requireRanker {
			return Result{}, errors.New("required Algorithm ranker returned an incomplete candidate set")
		}
	}
	candidates, err = s.reauthorize(ctx, viewerID, candidates)
	if err != nil {
		return Result{}, fmt.Errorf("recheck feed authorization: %w", err)
	}
	if len(candidates) == 0 {
		return Result{Posts: []map[string]any{}, NextCursor: encodeCursor(lastScanned), HasMore: hasMore, RankingMode: mode}, nil
	}
	posts, err := s.renderPosts(ctx, candidates)
	if err != nil {
		return Result{}, err
	}
	return Result{Posts: posts, NextCursor: encodeCursor(lastScanned), HasMore: hasMore, RankingMode: mode}, nil
}

func visibleToViewer(post, settings map[string]any, viewerID, authorID string, following bool) bool {
	if viewerID == authorID {
		return true
	}
	profileVisibility := stringField(settings, "profile_visibility")
	visibility := stringField(post, "visibility")
	if profileVisibility != "public" && !following {
		return false
	}
	switch visibility {
	case "public":
		return profileVisibility == "public" || following
	case "followers":
		return following
	default:
		return false
	}
}
func (s *Service) relationshipsForAuthors(ctx context.Context, viewerID string, authorIDs []string) (map[string]struct{}, map[string]struct{}, error) {
	following := make(map[string]struct{}, len(authorIDs))
	blocked := make(map[string]struct{}, len(authorIDs))
	if len(authorIDs) == 0 {
		return following, blocked, nil
	}
	followRows, err := s.store.ListDocumentsWithQueries(ctx, "follows", []string{
		equalQuery("follower_id", viewerID), equalQuery("followed_id", authorIDs...), equalQuery("state", "accepted"),
	}, 100)
	if err != nil {
		return nil, nil, err
	}
	for _, raw := range followRows {
		doc, decodeErr := decodeDocument(raw)
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		if id := stringField(doc, "followed_id"); id != "" {
			following[id] = struct{}{}
		}
	}
	outgoingRows, err := s.store.ListDocumentsWithQueries(ctx, "blocks", []string{
		equalQuery("blocker_id", viewerID), equalQuery("blocked_id", authorIDs...),
	}, 100)
	if err != nil {
		return nil, nil, err
	}
	incomingRows, err := s.store.ListDocumentsWithQueries(ctx, "blocks", []string{
		equalQuery("blocked_id", viewerID), equalQuery("blocker_id", authorIDs...),
	}, 100)
	if err != nil {
		return nil, nil, err
	}
	for _, raw := range append(outgoingRows, incomingRows...) {
		doc, decodeErr := decodeDocument(raw)
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		if id := stringField(doc, "blocked_id"); id != "" {
			blocked[id] = struct{}{}
		}
		if id := stringField(doc, "blocker_id"); id != "" {
			blocked[id] = struct{}{}
		}
	}
	return following, blocked, nil
}

func (s *Service) settingsByUser(ctx context.Context, userIDs []string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.store.ListDocumentsWithQueries(ctx, "account_settings", []string{equalQuery("user_id", userIDs...)}, 100)
	if err != nil {
		return nil, err
	}
	for _, raw := range rows {
		doc, e := decodeDocument(raw)
		if e != nil {
			return nil, e
		}
		id := stringField(doc, "user_id")
		if id != "" {
			out[id] = doc
		}
	}
	return out, nil
}
func (s *Service) reauthorize(ctx context.Context, viewerID string, candidates []candidatePost) ([]candidatePost, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	postIDs := make([]string, 0, len(candidates))
	authorIDs := make([]string, 0, len(candidates))
	seenAuthors := map[string]struct{}{}
	for _, candidate := range candidates {
		postIDs = append(postIDs, candidate.id)
		if _, exists := seenAuthors[candidate.authorID]; !exists {
			seenAuthors[candidate.authorID] = struct{}{}
			authorIDs = append(authorIDs, candidate.authorID)
		}
	}
	// Re-read canonical post status and visibility after the ranking round trip.
	postRows, err := s.store.ListDocumentsWithQueries(ctx, "posts", []string{
		equalQuery("$id", postIDs...), equalQuery("status", "published"),
	}, 100)
	if err != nil {
		return nil, err
	}
	postsByID := make(map[string]map[string]any, len(postRows))
	for _, raw := range postRows {
		doc, decodeErr := decodeDocument(raw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if id := stringField(doc, "$id"); id != "" {
			postsByID[id] = doc
		}
	}
	settings, err := s.settingsByUser(ctx, authorIDs)
	if err != nil {
		return nil, err
	}
	following, blocked, err := s.relationshipsForAuthors(ctx, viewerID, authorIDs)
	if err != nil {
		return nil, err
	}
	authorized := make([]candidatePost, 0, len(candidates))
	for _, candidate := range candidates {
		post, exists := postsByID[candidate.id]
		if !exists || stringField(post, "author_id") != candidate.authorID || boolField(post, "sensitive") {
			continue
		}
		if _, denied := blocked[candidate.authorID]; denied {
			continue
		}
		_, isFollowing := following[candidate.authorID]
		if !visibleToViewer(post, settings[candidate.authorID], viewerID, candidate.authorID, isFollowing) {
			continue
		}
		createdAt, parseErr := time.Parse(time.RFC3339Nano, stringField(post, "created_at"))
		if parseErr != nil {
			continue
		}
		candidate.raw = post
		candidate.created = createdAt
		candidate.following = isFollowing
		authorized = append(authorized, candidate)
	}
	return authorized, nil
}

func (s *Service) renderPosts(ctx context.Context, candidates []candidatePost) ([]map[string]any, error) {
	authorIDs := make([]string, 0, len(candidates))
	postIDs := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, c := range candidates {
		postIDs = append(postIDs, c.id)
		if _, ok := seen[c.authorID]; !ok {
			seen[c.authorID] = struct{}{}
			authorIDs = append(authorIDs, c.authorID)
		}
	}
	profiles, err := s.profilesByUser(ctx, authorIDs)
	if err != nil {
		return nil, fmt.Errorf("load feed authors: %w", err)
	}
	mediaByPost, err := s.mediaByPost(ctx, postIDs)
	if err != nil {
		return nil, fmt.Errorf("load post media: %w", err)
	}
	out := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		profile := profiles[c.authorID]
		author := map[string]any{"id": c.authorID, "handle": stringField(profile, "handle"), "display_name": stringField(profile, "display_name"), "bio": stringField(profile, "bio")}
		attachments := mediaByPost[c.id]
		urls := make([]string, 0, len(attachments))
		isVideo := false
		for _, attachment := range attachments {
			if s.media != nil {
				ownerID := stringField(attachment, "owner_id")
				if ownerID == "" {
					ownerID = c.authorID
				}
				if signed, e := s.media.DownloadURL(ctx, ownerID, stringField(attachment, "object_id")); e == nil && signed != "" {
					urls = append(urls, signed)
					if stringField(attachment, "media_type") == "video" && len(urls) == 1 {
						isVideo = true
					}
				}
			}
		}
		out = append(out, map[string]any{"id": c.id, "content": stringField(c.raw, "body"), "created_at": c.created.UTC().Format(time.RFC3339Nano), "author": author, "user_profiles": author, "visibility": stringField(c.raw, "visibility"), "origin": "local", "media_urls": urls, "media_count": len(urls), "attachment_count": len(attachments), "is_video": isVideo, "ranking_score": c.score, "ranking_reasons": c.reasons})
	}
	return out, nil
}
func (s *Service) profilesByUser(ctx context.Context, userIDs []string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.store.ListDocumentsWithQueries(ctx, "profiles", []string{equalQuery("user_id", userIDs...)}, 100)
	if err != nil {
		return nil, err
	}
	for _, raw := range rows {
		doc, e := decodeDocument(raw)
		if e != nil {
			return nil, e
		}
		id := stringField(doc, "user_id")
		if id != "" {
			out[id] = doc
		}
	}
	return out, nil
}
func (s *Service) mediaByPost(ctx context.Context, postIDs []string) (map[string][]map[string]any, error) {
	out := make(map[string][]map[string]any, len(postIDs))
	if len(postIDs) == 0 {
		return out, nil
	}
	rows, err := s.store.ListDocumentsWithQueries(ctx, "post_media", []string{equalQuery("post_id", postIDs...), orderAscQuery("position")}, 100)
	if err != nil {
		return nil, err
	}
	for _, raw := range rows {
		doc, e := decodeDocument(raw)
		if e != nil {
			return nil, e
		}
		id := stringField(doc, "post_id")
		if id != "" {
			out[id] = append(out[id], doc)
		}
	}
	return out, nil
}
func decodeDocument(raw json.RawMessage) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, errors.New("invalid Appwrite document")
	}
	return doc, nil
}
func stringField(doc map[string]any, key string) string {
	value, _ := doc[key].(string)
	return strings.TrimSpace(value)
}
func boolField(doc map[string]any, key string) bool { value, _ := doc[key].(bool); return value }
func equalQuery(attribute string, values ...string) string {
	encoded, _ := json.Marshal(map[string]any{"method": "equal", "column": attribute, "values": values})
	return string(encoded)
}
func orderDescQuery(attribute string) string {
	encoded, _ := json.Marshal(map[string]any{"method": "orderDesc", "column": attribute})
	return string(encoded)
}
func orderAscQuery(attribute string) string {
	encoded, _ := json.Marshal(map[string]any{"method": "orderAsc", "column": attribute})
	return string(encoded)
}
func cursorAfterQuery(id string) string {
	encoded, _ := json.Marshal(map[string]any{"method": "cursorAfter", "values": []string{id}})
	return string(encoded)
}
func encodeCursor(id string) string {
	if id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}
func ValidateCursor(cursor string) error {
	_, err := decodeCursor(cursor)
	return err
}

func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 256 {
		return "", errors.New("invalid feed cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) == 0 || len(decoded) > 128 {
		return "", errors.New("invalid feed cursor")
	}
	id := string(decoded)
	if strings.TrimSpace(id) != id {
		return "", errors.New("invalid feed cursor")
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", errors.New("invalid feed cursor")
		}
	}
	return id, nil
}
