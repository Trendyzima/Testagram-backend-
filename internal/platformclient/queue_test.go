package platformclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueueClaimUsesNormalizedMessageContract(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/queue/claim" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer unit-test-secret" {
			t.Fatal("missing internal bearer token")
		}
		var request struct {
			Queue string `json:"queue"`
			Group string `json:"group"`
			Consumer string `json:"consumer"`
			Count int64 `json:"count"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Queue != "federation" || request.Group != "delivery" || request.Consumer != "worker-1" || request.Count != 7 {
			t.Fatalf("unexpected claim body: %+v", request)
		}
		_, _ = w.Write([]byte(`{"messages":[{"id":"1720000000000-0","values":{"job_id":"job-1","type":"federation.deliver"}}]}`))
	})
	messages, err := client.Claim(context.Background(), "federation", "delivery", "worker-1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "1720000000000-0" || messages[0].Values["job_id"] != "job-1" {
		t.Fatalf("unexpected claimed messages: %+v", messages)
	}
	if _, err := client.Claim(context.Background(), "federation", "delivery", "worker-1", 101); err == nil {
		t.Fatal("expected invalid count to be rejected")
	}
}

func TestQueueReclaimAndAck(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer unit-test-secret" {
			t.Fatal("missing internal bearer token")
		}
		switch r.URL.Path {
		case "/v1/queue/reclaim":
			calls++
			if r.Method != http.MethodGet || r.URL.Query().Get("queue") != "mail" ||
				r.URL.Query().Get("group") != "sender" || r.URL.Query().Get("consumer") != "worker-2" ||
				r.URL.Query().Get("min_idle_ms") != "30000" || r.URL.Query().Get("count") != "5" || r.URL.Query().Get("start") != "0-0" {
				t.Fatalf("unexpected reclaim request: %s %s", r.Method, r.URL)
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"1720000000001-0","values":{"job_id":"mail-1"}}],"next_start":"0-0"}`))
		case "/v1/queue/ack":
			calls++
			if r.Method != http.MethodPost || r.URL.Query().Get("queue") != "mail" ||
				r.URL.Query().Get("group") != "sender" || r.URL.Query().Get("id") != "1720000000001-0" {
				t.Fatalf("unexpected ack request: %s %s", r.Method, r.URL)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	messages, cursor, err := client.Reclaim(context.Background(), "mail", "sender", "worker-2", 30*time.Second, 5, "0-0")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Values["job_id"] != "mail-1" || cursor != "0-0" {
		t.Fatalf("unexpected reclaim response: messages=%+v cursor=%q", messages, cursor)
	}
	if err := client.Ack(context.Background(), "mail", "sender", messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
	if err := client.Ack(context.Background(), "", "sender", "id"); err == nil {
		t.Fatal("expected missing queue to be rejected")
	}
	if _, _, err := client.Reclaim(context.Background(), "mail", "sender", "worker-2", 0, 5); err == nil {
		t.Fatal("expected invalid idle time to be rejected")
	}
}
