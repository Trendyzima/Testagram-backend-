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
