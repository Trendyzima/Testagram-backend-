package freshstorage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

func retentionMediaDoc(id, status, class, scope, created, deleted string, bytes int64) map[string]any {
	return map[string]any{"$id": id, "owner_id": "user_123", "object_key": "v1/0123456789abcdef0123456789abcdef", "bytes_decimal": strconv.FormatInt(bytes, 10), "mime_type": "image/jpeg", "sha256": strings.Repeat("a", 64), "status": status, "scope": scope, "retention_class": class, "created_at": created, "deleted_at": deleted}
}

func TestRetentionScannerDryRunUsesOnlyPostCutoffEventsAndReportsAllClasses(t *testing.T) {
	cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := cutoff.Add(31 * 24 * time.Hour)
	createdDocs := []map[string]any{
		retentionMediaDoc("med_archive", "ready", "permanent", "post_media", cutoff.Add(time.Hour).Format(time.RFC3339Nano), "", 10),
		retentionMediaDoc("med_story", "ready", "story", "story_media", cutoff.Add(25*time.Hour).Format(time.RFC3339Nano), "", 20),
		retentionMediaDoc("med_legacy", "ready", "permanent", "post_media", cutoff.Add(-time.Hour).Format(time.RFC3339Nano), "", 30),
		retentionMediaDoc("med_profile", "ready", "permanent", "profile_media", cutoff.Add(40*24*time.Hour).Format(time.RFC3339Nano), "", 40),
	}
	deletedDocs := []map[string]any{retentionMediaDoc("med_deleted", "user_deleted", "user_deleted", "post_media", cutoff.Add(-365*24*time.Hour).Format(time.RFC3339Nano), cutoff.Add(24*time.Hour).Format(time.RFC3339Nano), 50)}
	requests := map[string]int{}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			http.Error(w, "dry-run must not enqueue", 500)
			return
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/media_objects/documents") {
			http.Error(w, "unexpected", 404)
			return
		}
		query := r.URL.Query().Get("queries[]")
		var q struct {
			Method    string   `json:"method"`
			Attribute string   `json:"column"`
			Values    []string `json:"values"`
		}
		if err := json.Unmarshal([]byte(query), &q); err != nil {
			t.Errorf("bad query: %v", err)
		}
		if q.Method != "greaterThanEqual" || len(q.Values) != 1 || q.Values[0] != cutoff.Format(time.RFC3339Nano) {
			t.Errorf("bad retention cutoff query: %s", query)
		}
		requests[q.Attribute]++
		docs := createdDocs
		if q.Attribute != "created_at" {
			docs = deletedDocs
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 {
			t.Errorf("missing limit")
		}
		if offset > len(docs) {
			offset = len(docs)
		}
		end := offset + limit
		if end > len(docs) {
			end = len(docs)
		}
		page := make([]map[string]any, 0, end-offset)
		page = append(page, docs[offset:end]...)
		_ = json.NewEncoder(w).Encode(map[string]any{"documents": page, "total": len(docs)})
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := NewAppwriteStore(db)
	jobs, _ := NewAppwriteRetentionJobs(db)
	scanner, _ := NewRetentionScanner(store, jobs)
	policy := RetentionPolicy{Enabled: false, DryRun: true, StartAt: cutoff, StoryTTL: 24 * time.Hour, ArchiveAfter: 30 * 24 * time.Hour, RecoveryWindow: 30 * 24 * time.Hour, BatchSize: 100}
	report, err := scanner.Scan(context.Background(), policy, now, RetentionScanCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedCreated != 4 || report.ScannedDeleted != 1 || !report.CreatedDone || !report.DeletedDone {
		t.Fatalf("scan pagination/report incorrect: %#v", report)
	}
	if report.Actions[RetentionArchive].Count != 1 || report.Actions[RetentionArchive].LogicalBytes != 10 || report.Actions[RetentionExpireStory].Count != 1 || report.Actions[RetentionExpireStory].LogicalBytes != 20 || report.Actions[RetentionPurgeDeleted].Count != 1 || report.Actions[RetentionPurgeDeleted].LogicalBytes != 50 {
		t.Fatalf("unexpected dry-run actions: %#v", report.Actions)
	}
	if report.JobsConfirmed != 0 || posts != 0 || requests["created_at"] != 1 || requests["deleted_at"] != 1 {
		t.Fatalf("dry-run caused writes or missed scans: report=%#v posts=%d requests=%v", report, posts, requests)
	}
}

func TestRetentionScannerCursorAdvancesByRawDocumentsNotEligibleRows(t *testing.T) {
	cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := cutoff.Add(40 * 24 * time.Hour)
	docs := []map[string]any{
		retentionMediaDoc("med_profile", "ready", "permanent", "profile_media", cutoff.Add(time.Hour).Format(time.RFC3339Nano), "", 10),
		retentionMediaDoc("med_archive", "ready", "permanent", "post_media", cutoff.Add(time.Hour).Format(time.RFC3339Nano), "", 20),
		retentionMediaDoc("med_story", "ready", "story", "story_media", cutoff.Add(26*time.Hour).Format(time.RFC3339Nano), "", 30),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("queries[]")
		var v struct {
			Attribute string `json:"column"`
		}
		_ = json.Unmarshal([]byte(q), &v)
		page := docs
		if v.Attribute == "deleted_at" {
			page = nil
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		end := offset + limit
		if end > len(page) {
			end = len(page)
		}
		if offset > len(page) {
			offset = len(page)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"documents": page[offset:end], "total": len(page)})
	}))
	defer server.Close()
	db, _ := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	store, _ := NewAppwriteStore(db)
	jobs, _ := NewAppwriteRetentionJobs(db)
	scanner, _ := NewRetentionScanner(store, jobs)
	policy := RetentionPolicy{DryRun: true, StartAt: cutoff, StoryTTL: 24 * time.Hour, ArchiveAfter: 30 * 24 * time.Hour, RecoveryWindow: 30 * 24 * time.Hour, BatchSize: 2}
	first, err := scanner.Scan(context.Background(), policy, now, RetentionScanCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ScannedCreated != 2 || first.NextCursor.CreatedOffset != 2 || first.CreatedDone {
		t.Fatalf("first page cursor wrong: %#v", first)
	}
	second, err := scanner.Scan(context.Background(), policy, now, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if second.ScannedCreated != 1 || second.NextCursor.CreatedOffset != 3 || !second.CreatedDone {
		t.Fatalf("second page cursor wrong: %#v", second)
	}
}
