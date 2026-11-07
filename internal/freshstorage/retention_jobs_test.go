package freshstorage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

func TestRetentionJobDocumentIDIsStableAndAppwriteSafe(t *testing.T) {
	key := strings.Repeat("a", 64)
	first, second := retentionJobDocumentID(key), retentionJobDocumentID(key)
	if first != second || len(first) > 36 || !strings.HasPrefix(first, "rt_") {
		t.Fatalf("invalid deterministic document ID %q", first)
	}
}

func TestDecodeRetentionJobAndRejectInvalidState(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	raw, _ := json.Marshal(map[string]any{"$id": "rt_123", "job_key": strings.Repeat("a", 64), "object_id": "med_123", "action": "archive", "policy_version": RetentionPolicyVersion, "due_at": now, "status": "running", "worker_id": "worker-a", "lease_token": "token", "lease_until": now, "attempts": 2, "created_at": now, "updated_at": now})
	job, err := decodeRetentionJob(raw)
	if err != nil {
		t.Fatal(err)
	}
	if job.Action != RetentionArchive || job.Attempts != 2 || job.WorkerID != "worker-a" || job.LeaseToken != "token" {
		t.Fatalf("decoded wrong job: %#v", job)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["status"] = "deleting-without-approval"
	bad, _ := json.Marshal(doc)
	if _, err := decodeRetentionJob(bad); err == nil {
		t.Fatal("unknown job state accepted")
	}
}

func TestRetentionJobClaimValidation(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		worker string
		lease  time.Duration
	}{{"", time.Minute}, {"worker", time.Second}, {"worker", 16 * time.Minute}}
	for _, tc := range cases {
		if validDocID("rt_123") && validOwnerID(tc.worker) && tc.lease >= 30*time.Second && tc.lease <= 15*time.Minute {
			t.Fatalf("invalid claim fixture unexpectedly valid: %#v", tc)
		}
	}
	_ = now
}

func TestAppwriteRetentionJobsClaimReclaimsExpiredLeaseTransactionally(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	oldLease := now.Add(-time.Minute)
	jobID := "rt_123"
	var stagedUpdate map[string]any
	var committed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_claim"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/retention_jobs/documents/"+jobID):
			if r.URL.Query().Get("transactionId") != "tx_claim" {
				t.Errorf("claim read missing transaction")
			}
			doc := map[string]any{"$id": jobID, "job_key": strings.Repeat("a", 64), "object_id": "med_1", "action": "archive", "policy_version": RetentionPolicyVersion, "due_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "status": "running", "worker_id": "worker-old", "lease_token": "old-token", "lease_until": oldLease.Format(time.RFC3339Nano), "attempts": 1, "created_at": now.Add(-2 * time.Hour).Format(time.RFC3339Nano), "updated_at": oldLease.Format(time.RFC3339Nano)}
			_ = json.NewEncoder(w).Encode(doc)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/retention_jobs/documents/"+jobID):
			if r.URL.Query().Get("transactionId") != "tx_claim" {
				t.Errorf("claim update missing transaction")
			}
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			stagedUpdate = body.Data
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"$id":"rt_123"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/transactions/tx_claim":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			committed = body["commit"] == true
			_, _ = w.Write([]byte(`{"$id":"tx_claim"}`))
		default:
			t.Errorf("unexpected Appwrite request %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := NewAppwriteRetentionJobs(db)
	claimed, err := repo.Claim(context.Background(), jobID, "worker-new", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "running" || claimed.WorkerID != "worker-new" || claimed.Attempts != 2 || claimed.LeaseUntil != now.Add(time.Minute) || len(claimed.LeaseToken) != 64 {
		t.Fatalf("stale lease was not safely reclaimed: %#v", claimed)
	}
	if stagedUpdate["status"] != "running" || stagedUpdate["lease_token"] != claimed.LeaseToken || !committed {
		t.Fatalf("claim not transactionally committed: %#v committed=%v", stagedUpdate, committed)
	}
}

func TestAppwriteRetentionJobsEnqueueIsIdempotentOnConflict(t *testing.T) {
	decision := RetentionDecision{Action: RetentionArchive, DueAt: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), JobKey: strings.Repeat("b", 64)}
	id := retentionJobDocumentID(decision.JobKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/retention_jobs/documents") {
			http.Error(w, "document exists", http.StatusConflict)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/retention_jobs/documents/"+id) {
			doc := map[string]any{"$id": id, "job_key": decision.JobKey, "object_id": "med_2", "action": "archive", "policy_version": RetentionPolicyVersion, "due_at": decision.DueAt.Format(time.RFC3339Nano), "status": "pending", "attempts": 0, "created_at": decision.DueAt.Format(time.RFC3339Nano), "updated_at": decision.DueAt.Format(time.RFC3339Nano)}
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", 404)
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := NewAppwriteRetentionJobs(db)
	got, err := repo.Enqueue(context.Background(), decision, "med_2")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.JobKey != decision.JobKey || got.ObjectID != "med_2" || got.Status != "pending" {
		t.Fatalf("idempotent enqueue returned wrong job: %#v", got)
	}
}

func TestAppwriteRetentionJobsCannotCompleteAfterLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	jobID := "rt_123"
	lease := now.Add(-time.Second)
	var committed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_finish"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/retention_jobs/documents/"+jobID):
			doc := map[string]any{"$id": jobID, "job_key": strings.Repeat("c", 64), "object_id": "med_3", "action": "archive", "policy_version": RetentionPolicyVersion, "due_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "status": "running", "worker_id": "worker", "lease_token": "claim-token", "lease_until": lease.Format(time.RFC3339Nano), "attempts": 1, "created_at": now.Add(-2 * time.Hour).Format(time.RFC3339Nano), "updated_at": lease.Format(time.RFC3339Nano)}
			_ = json.NewEncoder(w).Encode(doc)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/transactions/tx_finish":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			committed = body["commit"] == true
			_, _ = w.Write([]byte(`{"$id":"tx_finish"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := NewAppwriteRetentionJobs(db)
	claimed := RetentionJob{ID: jobID, WorkerID: "worker", LeaseToken: "claim-token"}
	if err = repo.Complete(context.Background(), claimed, now); !errors.Is(err, ErrRetentionLeaseLost) {
		t.Fatalf("expired lease completion accepted: %v", err)
	}
	if committed {
		t.Fatal("expired lease transaction was committed")
	}
}
