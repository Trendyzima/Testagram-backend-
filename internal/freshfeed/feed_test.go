package freshfeed

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

type emptyStore struct{}

func (emptyStore) ListDocumentsWithQueries(context.Context, string, []string, int) ([]json.RawMessage, error) {
	return nil, nil
}

type failingStore struct{}

func (failingStore) ListDocumentsWithQueries(context.Context, string, []string, int) ([]json.RawMessage, error) {
	return nil, errors.New("database unavailable")
}

type noOpRanker struct{}

func (noOpRanker) Rank(context.Context, algorithmclient.Request) (algorithmclient.Response, error) {
	return algorithmclient.Response{}, nil
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(nil, nil, nil); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := New(emptyStore{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}
func TestHomeLoadsAuthorizationGraphBeforePosts(t *testing.T) {
	service, err := New(failingStore{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Home(context.Background(), "viewer", 20, ""); err == nil {
		t.Fatal("feed succeeded when follow/block graph could not be loaded")
	}
}
func TestHomeRejectsInvalidViewerLimitAndCursor(t *testing.T) {
	service, err := New(emptyStore{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		viewer string
		limit  int
		cursor string
	}{
		{"", 20, ""}, {"viewer", 0, ""}, {"viewer", 51, ""}, {"viewer", 20, "invalid!!!"},
	} {
		if _, err := service.Home(context.Background(), input.viewer, input.limit, input.cursor); err == nil {
			t.Fatalf("invalid input accepted: %#v", input)
		}
	}
}
func TestVisibilityFailsClosed(t *testing.T) {
	if visibleToViewer(map[string]any{"visibility": "public"}, nil, "viewer", "author", false) {
		t.Fatal("missing profile settings were treated as public")
	}
	if visibleToViewer(map[string]any{"visibility": "private"}, map[string]any{"profile_visibility": "public"}, "viewer", "author", false) {
		t.Fatal("private post was exposed")
	}
	if !visibleToViewer(map[string]any{"visibility": "private"}, nil, "author", "author", false) {
		t.Fatal("author cannot see their own private post")
	}
	if !visibleToViewer(map[string]any{"visibility": "followers"}, map[string]any{"profile_visibility": "private"}, "viewer", "author", true) {
		t.Fatal("accepted follower cannot see followers-only post")
	}
}

type fixtureFeedStore struct {
	follows    []map[string]any
	outgoing   []map[string]any
	incoming   []map[string]any
	posts      []map[string]any
	settings   []map[string]any
	profiles   []map[string]any
	media      []map[string]any
	failBlocks bool
}

func (f *fixtureFeedStore) ListDocumentsWithQueries(_ context.Context, collection string, queries []string, limit int) ([]json.RawMessage, error) {
	var rows []map[string]any
	switch collection {
	case "follows":
		rows = f.follows
	case "blocks":
		if f.failBlocks {
			return nil, errors.New("blocks unavailable")
		}
		if len(queries) > 0 && strings.Contains(queries[0], "blocker_id") {
			rows = f.outgoing
		} else {
			rows = f.incoming
		}
	case "posts":
		rows = f.posts
	case "account_settings":
		rows = f.settings
	case "profiles":
		rows = f.profiles
	case "post_media":
		rows = f.media
	default:
		return nil, errors.New("unexpected collection: " + collection)
	}
	copyRows := append([]map[string]any(nil), rows...)
	for _, raw := range queries {
		var q map[string]any
		if json.Unmarshal([]byte(raw), &q) != nil {
			continue
		}
		if q["method"] == "equal" {
			attribute, _ := q["column"].(string)
			values, _ := q["values"].([]any)
			filtered := copyRows[:0]
			for _, row := range copyRows {
				for _, value := range values {
					if row[attribute] == value {
						filtered = append(filtered, row)
						break
					}
				}
			}
			copyRows = filtered
		}
		if q["method"] == "cursorAfter" {
			values, _ := q["values"].([]any)
			if len(values) > 0 {
				cursor := values[0]
				for i, row := range copyRows {
					if row["$id"] == cursor {
						copyRows = copyRows[i+1:]
						break
					}
				}
			}
		}
	}
	if len(copyRows) > limit {
		copyRows = copyRows[:limit]
	}
	out := make([]json.RawMessage, 0, len(copyRows))
	for _, row := range copyRows {
		b, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

type reverseFeedRanker struct {
	candidateIDs []string
	afterRank    func()
}

func (r *reverseFeedRanker) Rank(_ context.Context, in algorithmclient.Request) (algorithmclient.Response, error) {
	out := algorithmclient.Response{Items: make([]algorithmclient.RankedItem, 0, len(in.Candidates))}
	for _, c := range in.Candidates {
		r.candidateIDs = append(r.candidateIDs, c.ID)
	}
	for i := len(in.Candidates) - 1; i >= 0; i-- {
		out.Items = append(out.Items, algorithmclient.RankedItem{ID: in.Candidates[i].ID, Score: 0.8, Reasons: []string{"recent"}})
	}
	if r.afterRank != nil {
		r.afterRank()
	}
	return out, nil
}

type recordingMediaSigner struct{ ownerIDs []string }

func (m *recordingMediaSigner) DownloadURL(_ context.Context, ownerID, objectID string) (string, error) {
	m.ownerIDs = append(m.ownerIDs, ownerID)
	return "https://media.example/" + objectID, nil
}
func feedDoc(id string, fields map[string]any) map[string]any {
	doc := map[string]any{"$id": id}
	for k, v := range fields {
		doc[k] = v
	}
	return doc
}
func newFixtureFeedStore() *fixtureFeedStore {
	return &fixtureFeedStore{
		follows:  []map[string]any{feedDoc("f1", map[string]any{"follower_id": "viewer", "followed_id": "u2", "state": "accepted", "created_at": "2026-11-07T10:00:00Z"})},
		outgoing: []map[string]any{feedDoc("b1", map[string]any{"blocker_id": "viewer", "blocked_id": "u3", "created_at": "2026-11-07T10:00:00Z"})},
		incoming: []map[string]any{feedDoc("b2", map[string]any{"blocker_id": "u4", "blocked_id": "viewer", "created_at": "2026-11-07T09:00:00Z"})},
		posts: []map[string]any{
			feedDoc("p7", map[string]any{"author_id": "u6", "status": "published", "visibility": "public", "body": "latest public", "created_at": "2026-11-07T07:00:00Z"}),
			feedDoc("p6", map[string]any{"author_id": "u2", "status": "published", "visibility": "followers", "body": "followed post", "created_at": "2026-11-07T06:00:00Z"}),
			feedDoc("p5", map[string]any{"author_id": "u5", "status": "published", "visibility": "public", "body": "private profile", "created_at": "2026-11-07T05:00:00Z"}),
			feedDoc("p4", map[string]any{"author_id": "u4", "status": "published", "visibility": "public", "body": "incoming block", "created_at": "2026-11-07T04:00:00Z"}),
			feedDoc("p3", map[string]any{"author_id": "u3", "status": "published", "visibility": "public", "body": "outgoing block", "created_at": "2026-11-07T03:00:00Z"}),
			feedDoc("p2", map[string]any{"author_id": "u2", "status": "published", "visibility": "followers", "body": "older followed post", "created_at": "2026-11-07T02:00:00Z"}),
			feedDoc("p1", map[string]any{"author_id": "u7", "status": "published", "visibility": "private", "body": "private post", "created_at": "2026-11-07T01:00:00Z"}),
		},
		settings: []map[string]any{
			feedDoc("s2", map[string]any{"user_id": "u2", "profile_visibility": "public"}),
			feedDoc("s3", map[string]any{"user_id": "u3", "profile_visibility": "public"}),
			feedDoc("s4", map[string]any{"user_id": "u4", "profile_visibility": "public"}),
			feedDoc("s5", map[string]any{"user_id": "u5", "profile_visibility": "private"}),
			feedDoc("s6", map[string]any{"user_id": "u6", "profile_visibility": "public"}),
			feedDoc("s7", map[string]any{"user_id": "u7", "profile_visibility": "public"}),
		},
		profiles: []map[string]any{
			feedDoc("pr2", map[string]any{"user_id": "u2", "handle": "two", "display_name": "Two"}),
			feedDoc("pr6", map[string]any{"user_id": "u6", "handle": "six", "display_name": "Six"}),
		},
		media: []map[string]any{feedDoc("pm6", map[string]any{"post_id": "p6", "owner_id": "u2", "object_id": "obj6", "media_type": "image", "position": 0})},
	}
}
func TestHomeFiltersBothBlockDirectionsAndPrivateContentBeforeRanking(t *testing.T) {
	store := newFixtureFeedStore()
	ranker := &reverseFeedRanker{}
	signer := &recordingMediaSigner{}
	service, err := New(store, ranker, signer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Home(context.Background(), "viewer", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.RankingMode != "algorithm" {
		t.Fatalf("ranking mode=%s", result.RankingMode)
	}
	if len(result.Posts) != 3 {
		t.Fatalf("expected 3 authorized posts, got %#v", result.Posts)
	}
	if result.Posts[0]["id"] != "p2" || result.Posts[1]["id"] != "p6" || result.Posts[2]["id"] != "p7" {
		t.Fatalf("unexpected ranking: %#v", result.Posts)
	}
	for _, id := range ranker.candidateIDs {
		if id == "p1" || id == "p3" || id == "p4" || id == "p5" {
			t.Fatalf("ineligible ID reached ranker: %s", id)
		}
	}
	if len(signer.ownerIDs) != 1 || signer.ownerIDs[0] != "u2" {
		t.Fatalf("media signing owner mismatch: %#v", signer.ownerIDs)
	}
}
func TestHomeCursorDoesNotSkipEligiblePostsBehindFilteredRows(t *testing.T) {
	service, err := New(newFixtureFeedStore(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Home(context.Background(), "viewer", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Posts) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := service.Home(context.Background(), "viewer", 2, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Posts) != 1 || second.Posts[0]["id"] != "p2" || second.HasMore {
		t.Fatalf("unexpected second page: %#v", second)
	}
}
func TestHomeFailsClosedWhenBlockGraphIsUnavailable(t *testing.T) {
	store := newFixtureFeedStore()
	store.failBlocks = true
	service, err := New(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Home(context.Background(), "viewer", 20, ""); err == nil {
		t.Fatal("feed served without authoritative block graph")
	}
}

type failingFeedRanker struct{}

func (failingFeedRanker) Rank(context.Context, algorithmclient.Request) (algorithmclient.Response, error) {
	return algorithmclient.Response{}, errors.New("ranker offline")
}

type malformedFeedRanker struct{ kind string }

func (r malformedFeedRanker) Rank(_ context.Context, in algorithmclient.Request) (algorithmclient.Response, error) {
	items := make([]algorithmclient.RankedItem, 0, len(in.Candidates))
	for _, candidate := range in.Candidates {
		items = append(items, algorithmclient.RankedItem{ID: candidate.ID, Score: 0.5})
	}
	if len(items) < 2 {
		return algorithmclient.Response{}, errors.New("test requires at least two candidates")
	}
	switch r.kind {
	case "duplicate":
		items[1].ID = items[0].ID
	case "unknown":
		items[0].ID = "forged-post-id"
	case "nan":
		items[0].Score = math.NaN()
	}
	return algorithmclient.Response{Items: items}, nil
}
func TestRequiredRankerRejectsDuplicateUnknownAndInvalidScores(t *testing.T) {
	for _, kind := range []string{"duplicate", "unknown", "nan"} {
		service, err := New(newFixtureFeedStore(), malformedFeedRanker{kind: kind}, nil)
		if err != nil {
			t.Fatal(err)
		}
		service.RequireRanker(true)
		if _, err := service.Home(context.Background(), "viewer", 10, ""); err == nil {
			t.Fatalf("malformed Algorithm response %q was accepted", kind)
		}
	}
}

func TestRequiredRankerFailsClosedInsteadOfSilentlyFallingBack(t *testing.T) {
	for _, ranker := range []Ranker{nil, failingFeedRanker{}, noOpRanker{}} {
		service, err := New(newFixtureFeedStore(), ranker, nil)
		if err != nil {
			t.Fatal(err)
		}
		service.RequireRanker(true)
		if _, err := service.Home(context.Background(), "viewer", 10, ""); err == nil {
			t.Fatal("required ranker missing, unavailable or incompatible but feed silently fell back")
		}
	}
}

func TestReadyVerifiesAllFeedCollectionsAndIndexes(t *testing.T) {
	service, err := New(newFixtureFeedStore(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Ready(context.Background()); err != nil {
		t.Fatalf("feed schema should be ready: %v", err)
	}
}

func TestHomeRechecksBlocksAfterAlgorithmRoundTrip(t *testing.T) {
	store := newFixtureFeedStore()
	ranker := &reverseFeedRanker{afterRank: func() {
		store.outgoing = append(store.outgoing, feedDoc("b-new", map[string]any{
			"blocker_id": "viewer", "blocked_id": "u6", "created_at": "2026-11-07T11:00:00Z",
		}))
	}}
	service, err := New(store, ranker, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Home(context.Background(), "viewer", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Posts) != 2 {
		t.Fatalf("newly blocked author was not removed: %#v", result.Posts)
	}
	for _, post := range result.Posts {
		if post["id"] == "p7" {
			t.Fatal("newly blocked post survived final authorization")
		}
	}
}
