package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This test runs against the isolated PostgreSQL instance used by CI. It is
// skipped for ordinary unit-test runs where TESTAGRAM_TEST_DATABASE_URL is not
// configured. Every fixture is rolled back, including replication-outbox rows.
func TestSocialPostVisibilityPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("TESTAGRAM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TESTAGRAM_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to PostgreSQL integration database: %v", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	// Capability gateway rate limiting now uses the Backend's PostgreSQL
	// security table directly; it has no Redis/Upstash dependency.
	rateBucket := strings.Repeat("a", 64)
	for want := 1; want <= 3; want++ {
		var attempts int
		err := tx.QueryRow(ctx, `
INSERT INTO testagram_auth.rate_limits(action,bucket_hash,window_start,attempts)
VALUES('capability-api',$1,date_trunc('minute',now()),1)
ON CONFLICT(action,bucket_hash,window_start) DO UPDATE
SET attempts=testagram_auth.rate_limits.attempts+1
RETURNING attempts`, rateBucket).Scan(&attempts)
		if err != nil {
			t.Fatalf("increment PostgreSQL capability rate limit: %v", err)
		}
		if attempts != want {
			t.Fatalf("PostgreSQL capability rate-limit attempts = %d, want %d", attempts, want)
		}
	}

	suffix := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	var viewerID, authorID, protectedAuthorID string
	for _, fixture := range []struct {
		username string
		target   *string
	}{
		{"ci_viewer_" + suffix, &viewerID},
		{"ci_author_" + suffix, &authorID},
		{"ci_protected_" + suffix, &protectedAuthorID},
	} {
		if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.accounts(user_id,username,protected_account)
			VALUES(gen_random_uuid(),$1,$2) RETURNING user_id::text`,
			fixture.username, fixture.target == &protectedAuthorID).Scan(fixture.target); err != nil {
			t.Fatalf("insert account %s: %v", fixture.username, err)
		}
	}

	var publicPostID, followersPostID, protectedPostID, privatePostID string
	for _, fixture := range []struct {
		authorID   string
		visibility string
		target     *string
	}{
		{authorID, "public", &publicPostID},
		{authorID, "followers", &followersPostID},
		{protectedAuthorID, "public", &protectedPostID},
		{authorID, "private", &privatePostID},
	} {
		if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.posts(author_id,text,visibility)
			VALUES($1,$2,$3) RETURNING id::text`,
			fixture.authorID, "visibility integration "+suffix, fixture.visibility).Scan(fixture.target); err != nil {
			t.Fatalf("insert %s post: %v", fixture.visibility, err)
		}
	}

	assertVisible := func(name, viewer, post string, want bool) {
		t.Helper()
		_, err := socialCanViewPostWith(ctx, tx, viewer, post)
		got := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			got = false
		} else if err != nil {
			t.Fatalf("%s: visibility query failed: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: visible=%v, want %v", name, got, want)
		}
	}

	// Exercise the same aggregate queries used by the independent interaction
	// summary endpoint, including emoji breakdown and per-viewer state.
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.post_reactions(post_id,user_id,emoji) VALUES($1,$2,'❤️')`, publicPostID, viewerID); err != nil {
		t.Fatalf("insert reaction fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.replies(post_id,user_id,content) VALUES($1,$2,'summary reply')`, publicPostID, viewerID); err != nil {
		t.Fatalf("insert reply fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.reposts(post_id,user_id) VALUES($1,$2)`, publicPostID, viewerID); err != nil {
		t.Fatalf("insert repost fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.bookmarks(post_id,user_id) VALUES($1,$2)`, publicPostID, viewerID); err != nil {
		t.Fatalf("insert bookmark fixture: %v", err)
	}
	var likes, replies, reposts, bookmarks int64
	summaryQuery := `SELECT likes_count,replies_count,reposts_count,
		(SELECT count(*) FROM testagram_local.bookmarks b WHERE b.post_id=p.id)
		FROM testagram_local.posts p WHERE p.id=$1`
	if err := tx.QueryRow(ctx, summaryQuery, publicPostID).Scan(&likes, &replies, &reposts, &bookmarks); err != nil {
		t.Fatalf("read interaction summary counts: %v", err)
	}
	if likes != 1 || replies != 1 || reposts != 1 || bookmarks != 1 {
		t.Fatalf("interaction summary counts = likes:%d replies:%d reposts:%d bookmarks:%d, want all 1", likes, replies, reposts, bookmarks)
	}
	var emoji string
	if err := tx.QueryRow(ctx, `SELECT emoji FROM testagram_local.post_reactions WHERE post_id=$1 AND user_id=$2`, publicPostID, viewerID).Scan(&emoji); err != nil || emoji != "❤️" {
		t.Fatalf("viewer reaction state = %q, err=%v; want heart reaction", emoji, err)
	}
	var reposted, bookmarked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testagram_local.reposts WHERE post_id=$1 AND user_id=$2),
		EXISTS(SELECT 1 FROM testagram_local.bookmarks WHERE post_id=$1 AND user_id=$2)`, publicPostID, viewerID).Scan(&reposted, &bookmarked); err != nil {
		t.Fatalf("read viewer repost/bookmark state: %v", err)
	}
	if !reposted || !bookmarked {
		t.Fatalf("viewer repost/bookmark state = %v/%v, want true/true", reposted, bookmarked)
	}

	// Exercise the same visibility-filtered SQL projections used by the
	// likes/reposts/quotes listing endpoint, including quoted repost content.
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.reposts(post_id,user_id,quote)
		VALUES($1,$2,'integration quote')`, publicPostID, protectedAuthorID); err != nil {
		t.Fatalf("insert quote fixture: %v", err)
	}
	interactionQueries := map[string]string{
		"likes": `SELECT r.id::text,r.user_id::text,''::text,r.created_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.post_reactions r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.post_id=$1 AND r.emoji='❤️' AND a.account_status='active'`,
		"reposts": `SELECT r.id::text,r.user_id::text,r.quote,r.created_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.reposts r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.post_id=$1 AND a.account_status='active'`,
		"quotes": `SELECT r.id::text,r.user_id::text,r.quote,r.created_at,a.username,a.display_name,a.avatar_key
			FROM testagram_local.reposts r JOIN testagram_local.accounts a ON a.user_id=r.user_id
			WHERE r.post_id=$1 AND btrim(r.quote)<>'' AND a.account_status='active'`,
	}
	for kind, query := range interactionQueries {
		rows, err := tx.Query(ctx, query, publicPostID)
		if err != nil {
			t.Fatalf("query %s interaction list: %v", kind, err)
		}
		count := 0
		quoteFound := false
		for rows.Next() {
			var id, actorID, quote, username, displayName, avatarKey string
			var createdAt time.Time
			if err := rows.Scan(&id, &actorID, &quote, &createdAt, &username, &displayName, &avatarKey); err != nil {
				rows.Close()
				t.Fatalf("decode %s interaction: %v", kind, err)
			}
			count++
			if kind == "quotes" && quote == "integration quote" && actorID == protectedAuthorID {
				quoteFound = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s interactions: %v", kind, err)
		}
		rows.Close()
		want := map[string]int{"likes": 1, "reposts": 2, "quotes": 1}[kind]
		if count != want {
			t.Errorf("%s interaction count = %d, want %d", kind, count, want)
		}
		if kind == "quotes" && !quoteFound {
			t.Error("quotes listing did not return the quoted repost content and actor")
		}
	}

	assertVisible("public post visible to another active account", viewerID, publicPostID, true)
	assertVisible("private post hidden from another account", viewerID, privatePostID, false)
	assertVisible("author can view own private post", authorID, privatePostID, true)
	assertVisible("followers-only post hidden without accepted follow", viewerID, followersPostID, false)

	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES($1,$2,'accepted')`, viewerID, authorID); err != nil {
		t.Fatalf("insert accepted follow: %v", err)
	}
	assertVisible("followers-only post visible after accepted follow", viewerID, followersPostID, true)

	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES($1,$2,'pending')`, viewerID, protectedAuthorID); err != nil {
		t.Fatalf("insert pending protected-account follow: %v", err)
	}
	assertVisible("protected author's public post hidden while follow is pending", viewerID, protectedPostID, false)

	if _, err := tx.Exec(ctx, `UPDATE testagram_local.follows SET status='accepted'
		WHERE follower_id=$1 AND following_id=$2`, viewerID, protectedAuthorID); err != nil {
		t.Fatalf("accept protected-account follow: %v", err)
	}
	var protectedFollowerCount int64
	if err := tx.QueryRow(ctx, `SELECT follower_count FROM testagram_local.accounts WHERE user_id=$1`, protectedAuthorID).Scan(&protectedFollowerCount); err != nil {
		t.Fatalf("read protected account follower count: %v", err)
	}
	if protectedFollowerCount != 1 {
		t.Fatalf("pending-to-accepted follow transition produced follower_count=%d, want 1", protectedFollowerCount)
	}
	assertVisible("protected author's post visible after follow acceptance", viewerID, protectedPostID, true)

	// The profile connections endpoint must not reveal a protected account's
	// social graph to a non-follower, but must allow an accepted follower.
	var viewerCanEnumerate, strangerCanEnumerate bool
	profileVisibilitySQL := `
SELECT a.user_id=$1 OR EXISTS(
  SELECT 1 FROM testagram_local.follows f
  WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'
) OR (a.discoverable_by_username AND NOT a.protected_account)
FROM testagram_local.accounts a
WHERE a.user_id=$2 AND a.account_status='active'
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$1))`
	if err := tx.QueryRow(ctx, profileVisibilitySQL, viewerID, protectedAuthorID).Scan(&viewerCanEnumerate); err != nil {
		t.Fatalf("check accepted follower profile visibility: %v", err)
	}
	if err := tx.QueryRow(ctx, profileVisibilitySQL, authorID, protectedAuthorID).Scan(&strangerCanEnumerate); err != nil {
		t.Fatalf("check stranger profile visibility: %v", err)
	}
	if !viewerCanEnumerate || strangerCanEnumerate {
		t.Fatalf("protected profile connections visible to accepted follower/stranger = %v/%v, want true/false", viewerCanEnumerate, strangerCanEnumerate)
	}

	// Profile-post visibility is stricter than merely being an accepted
	// follower: private posts remain owner-only.
	profilePostsSQL := `
SELECT p.id::text
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
WHERE p.author_id=$2 AND p.deleted_at IS NULL AND a.account_status='active'
  AND (
    p.author_id=$1
    OR (p.visibility='public' AND NOT a.protected_account)
    OR (p.visibility IN ('public','followers') AND EXISTS(
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'
    ))
  )
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$1))`
	profilePostRows, err := tx.Query(ctx, profilePostsSQL, viewerID, protectedAuthorID)
	if err != nil {
		t.Fatalf("query protected profile posts: %v", err)
	}
	visibleProfilePosts := map[string]bool{}
	for profilePostRows.Next() {
		var postID string
		if err := profilePostRows.Scan(&postID); err != nil {
			profilePostRows.Close()
			t.Fatalf("scan protected profile post: %v", err)
		}
		visibleProfilePosts[postID] = true
	}
	if err := profilePostRows.Err(); err != nil {
		profilePostRows.Close()
		t.Fatalf("iterate protected profile posts: %v", err)
	}
	profilePostRows.Close()
	if !visibleProfilePosts[protectedPostID] || visibleProfilePosts[privatePostID] {
		t.Fatalf("profile post visibility returned public=%v private=%v; want true/false",
			visibleProfilePosts[protectedPostID], visibleProfilePosts[privatePostID])
	}

	// Exercise the same actor joins and direction filters used by the profile
	// followers/following endpoint.
	connectionQueries := map[string]struct {
		query string
		owner string
		want  string
	}{
		"followers": {
			query: `SELECT a.user_id::text FROM testagram_local.follows f
				JOIN testagram_local.accounts a ON a.user_id=f.follower_id
				WHERE f.following_id=$1 AND f.status='accepted' AND a.account_status='active'`,
			owner: authorID,
			want: viewerID,
		},
		"following": {
			query: `SELECT a.user_id::text FROM testagram_local.follows f
				JOIN testagram_local.accounts a ON a.user_id=f.following_id
				WHERE f.follower_id=$1 AND f.status='accepted' AND a.account_status='active'`,
			owner: viewerID,
			want: authorID,
		},
	}
	for kind, fixture := range connectionQueries {
		rows, err := tx.Query(ctx, fixture.query, fixture.owner)
		if err != nil {
			t.Fatalf("query profile %s: %v", kind, err)
		}
		found := false
		for rows.Next() {
			var accountID string
			if err := rows.Scan(&accountID); err != nil {
				rows.Close()
				t.Fatalf("scan profile %s: %v", kind, err)
			}
			if accountID == fixture.want {
				found = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate profile %s: %v", kind, err)
		}
		rows.Close()
		if !found {
			t.Errorf("profile %s list did not include expected account %s", kind, fixture.want)
		}
	}

	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.blocks(blocker_id,blocked_id)
		VALUES($1,$2)`, authorID, viewerID); err != nil {
		t.Fatalf("insert block: %v", err)
	}
	assertVisible("block hides author's public post", viewerID, publicPostID, false)
	assertVisible("block hides author's followers-only post", viewerID, followersPostID, false)

	if _, err := tx.Exec(ctx, `UPDATE testagram_local.accounts SET account_status='suspended' WHERE user_id=$1`, viewerID); err != nil {
		t.Fatalf("suspend viewer fixture: %v", err)
	}
	assertVisible("suspended viewer cannot read a public post", viewerID, publicPostID, false)
}
