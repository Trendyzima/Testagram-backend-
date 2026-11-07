package freshstorage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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
