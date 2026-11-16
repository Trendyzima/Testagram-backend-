package federationclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRequiresPrivateServiceCredential(t *testing.T) {
	if _, err := New("http://127.0.0.1:8081", "short", time.Second); err == nil {
		t.Fatal("expected short service token to be rejected")
	}
	if _, err := New("http://example.com", strings.Repeat("x", 40), time.Second); err == nil {
		t.Fatal("expected public HTTP endpoint to be rejected")
	}
}

func TestPublishSendsVerifiedServiceRequestAndChecksAcknowledgement(t *testing.T) {
	token := strings.Repeat("s", 40)
	activity := json.RawMessage(`{"id":"https://testagram.site/activities/1","type":"Create","actor":"https://testagram.site/users/alice","object":{"id":"https://testagram.site/posts/1"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/activities" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing service bearer")
		}
		if r.Header.Get("Idempotency-Key") != "https://testagram.site/activities/1" {
			t.Error("missing activity idempotency key")
		}
		var input PublishRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if input.ActorUserID != "account-1" || string(input.Activity) != string(activity) {
			t.Errorf("unexpected request payload: %#v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"activity_id":"https://testagram.site/activities/1","recipient_count":2,"accepted":true},"request_id":"r1","error":null}`))
	}))
	defer server.Close()

	client, err := New(server.URL, token, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Publish(context.Background(), PublishRequest{
		ActivityID: "https://testagram.site/activities/1",
		ActorUserID: "account-1",
		Activity: activity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.RecipientCount != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestPublishRejectsMismatchedAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"activity_id":"other","recipient_count":1,"accepted":true}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, strings.Repeat("s", 40), time.Second)
	if err != nil { t.Fatal(err) }
	_, err = client.Publish(context.Background(), PublishRequest{
		ActivityID: "https://testagram.site/activities/1",
		ActorUserID: "account-1",
		Activity: json.RawMessage(`{"id":"https://testagram.site/activities/1","type":"Create"}`),
	})
	if err == nil { t.Fatal("expected mismatched acknowledgement to fail") }
}

func TestFetchRemoteObjectsSendsBackendAllowlistAndChecksSource(t *testing.T) {
    token := strings.Repeat("f", 40)
    actor := "https://remote.example/users/alice"
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/remote-objects" { t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path) }
        if r.Header.Get("Authorization") != "Bearer "+token { t.Error("missing private Federation bearer") }
        var input RemoteObjectsRequest
        if err := json.NewDecoder(r.Body).Decode(&input); err != nil { t.Fatal(err) }
        if len(input.ActorURIs) != 1 || input.ActorURIs[0] != actor { t.Errorf("wrong actor allowlist: %#v", input) }
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true,"data":{"source":"activitypub","items":[{"object_uri":"https://remote.example/objects/1","actor_uri":"https://remote.example/users/alice","object_type":"Note","content":"hello","visibility":"public","created_at":"2026-11-16T08:00:00Z"}],"next_cursor":"","has_more":false},"error":null}`))
    }))
    defer server.Close()
    client, err := New(server.URL, token, time.Second)
    if err != nil { t.Fatal(err) }
    page, err := client.FetchRemoteObjects(context.Background(), RemoteObjectsRequest{ActorURIs: []string{actor}, Limit: 20})
    if err != nil { t.Fatal(err) }
    if len(page.Items) != 1 || page.Items[0].ActorURI != actor || page.Items[0].Visibility != "public" { t.Fatalf("unexpected remote object page: %#v", page) }
}

func TestFetchRemoteObjectsRejectsActorOutsideBackendAllowlist(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true,"data":{"items":[{"object_uri":"https://evil.example/o/1","actor_uri":"https://evil.example/users/eve","visibility":"public"}],"next_cursor":"","has_more":false}}`))
    }))
    defer server.Close()
    client, err := New(server.URL, strings.Repeat("f", 40), time.Second)
    if err != nil { t.Fatal(err) }
    _, err = client.FetchRemoteObjects(context.Background(), RemoteObjectsRequest{ActorURIs: []string{"https://remote.example/users/alice"}, Limit: 20})
    if err == nil { t.Fatal("Federation response outside Backend actor allowlist was accepted") }
}

func TestFetchATProtoFeedsVerifiesDIDBoundPosts(t *testing.T) {
    token := strings.Repeat("f", 40)
    did := "did:plc:222222222222222222222222"
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/internal/v1/atproto/feed" { t.Errorf("unexpected path: %s", r.URL.Path) }
        if r.Header.Get("Authorization") != "Bearer "+token { t.Error("missing private Federation bearer") }
        var input ATProtoFeedRequest
        if err := json.NewDecoder(r.Body).Decode(&input); err != nil { t.Fatal(err) }
        if len(input.ActorDIDs) != 1 || input.ActorDIDs[0] != did { t.Errorf("wrong DID allowlist: %#v", input) }
        if input.Cursors[did] != "cursor-page-2" { t.Errorf("wrong per-DID cursor: %#v", input.Cursors) }
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true,"data":{"source":"bluesky-atproto","actors":[{"did":"did:plc:222222222222222222222222","handle":"alice.bsky.social","cursor":"cursor-page-3","posts":[{"uri":"at://did:plc:222222222222222222222222/app.bsky.feed.post/1","cid":"bafy1","text":"hello","author":{"did":"did:plc:222222222222222222222222","handle":"alice.bsky.social"}}]}]}}`))
    }))
    defer server.Close()
    client, err := New(server.URL, token, time.Second)
    if err != nil { t.Fatal(err) }
    feeds, err := client.FetchATProtoFeeds(context.Background(), ATProtoFeedRequest{ActorDIDs: []string{did}, LimitPerActor: 10, Cursors: map[string]string{did: "cursor-page-2"}})
    if err != nil { t.Fatal(err) }
    if len(feeds) != 1 || len(feeds[0].Posts) != 1 || feeds[0].Posts[0].Author.DID != did { t.Fatalf("unexpected AT Protocol feed: %#v", feeds) }
}

func TestFetchATProtoFeedsRejectsDIDSubstitution(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true,"data":{"source":"bluesky-atproto","actors":[{"did":"did:plc:333333333333333333333333","handle":"other.bsky.social","posts":[]}]}}`))
    }))
    defer server.Close()
    client, err := New(server.URL, strings.Repeat("f", 40), time.Second)
    if err != nil { t.Fatal(err) }
    _, err = client.FetchATProtoFeeds(context.Background(), ATProtoFeedRequest{ActorDIDs: []string{"did:plc:222222222222222222222222"}, LimitPerActor: 10})
    if err == nil { t.Fatal("DID substitution in Federation response was accepted") }
}

func TestFetchATProtoFeedsRejectsCursorForUnauthorizedDID(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        t.Fatal("unauthorized cursor must be rejected before network access")
    }))
    defer server.Close()
    client, err := New(server.URL, strings.Repeat("f", 40), time.Second)
    if err != nil { t.Fatal(err) }
    _, err = client.FetchATProtoFeeds(context.Background(), ATProtoFeedRequest{
        ActorDIDs: []string{"did:plc:222222222222222222222222"},
        LimitPerActor: 10,
        Cursors: map[string]string{"did:plc:333333333333333333333333": "foreign"},
    })
    if err == nil { t.Fatal("cursor for a DID outside the authorized list was accepted") }
}
