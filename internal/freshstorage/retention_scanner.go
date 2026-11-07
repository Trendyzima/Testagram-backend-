package freshstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type RetentionScanCursor struct {
	CreatedOffset int `json:"created_offset"`
	DeletedOffset int `json:"deleted_offset"`
}
type RetentionActionSummary struct {
	Count        int   `json:"count"`
	LogicalBytes int64 `json:"logical_bytes_decimal"`
}
type RetentionScanReport struct {
	GeneratedAt    time.Time                                  `json:"generated_at"`
	RolloutCutoff  time.Time                                  `json:"rollout_cutoff"`
	DryRun         bool                                       `json:"dry_run"`
	ScannedCreated int                                        `json:"scanned_created"`
	ScannedDeleted int                                        `json:"scanned_deleted"`
	Actions        map[RetentionAction]RetentionActionSummary `json:"actions"`
	JobsConfirmed  int                                        `json:"jobs_confirmed"`
	NextCursor     RetentionScanCursor                        `json:"next_cursor"`
	CreatedDone    bool                                       `json:"created_done"`
	DeletedDone    bool                                       `json:"deleted_done"`
}

type RetentionScanner struct {
	store *AppwriteStore
	jobs  *AppwriteRetentionJobs
}

func NewRetentionScanner(store *AppwriteStore, jobs *AppwriteRetentionJobs) (*RetentionScanner, error) {
	if store == nil || jobs == nil {
		return nil, errors.New("Appwrite media store and retention job repository are required")
	}
	return &RetentionScanner{store: store, jobs: jobs}, nil
}

// Scan reads only records whose creation or user-deletion event is at/after the
// rollout cutoff. In disabled+dry-run mode it evaluates the policy without
// creating jobs or mutating Appwrite. Cursors let operators inspect all pages.
func (s *RetentionScanner) Scan(ctx context.Context, policy RetentionPolicy, now time.Time, cursor RetentionScanCursor) (RetentionScanReport, error) {
	if policy.StartAt.IsZero() {
		return RetentionScanReport{}, errors.New("retention scan requires an explicit rollout cutoff")
	}
	if !policy.DryRun && !policy.Enabled {
		return RetentionScanReport{}, errors.New("non-dry-run scan requires retention to be explicitly enabled")
	}
	if cursor.CreatedOffset < 0 || cursor.DeletedOffset < 0 {
		return RetentionScanReport{}, errors.New("retention scan offsets cannot be negative")
	}
	limit := policy.BatchSize
	if limit < 1 || limit > 100 {
		return RetentionScanReport{}, errors.New("retention scan batch size must be between 1 and 100")
	}
	evalPolicy := policy
	if !evalPolicy.Enabled && evalPolicy.DryRun {
		evalPolicy.Enabled = true
	}
	report := RetentionScanReport{GeneratedAt: now.UTC(), RolloutCutoff: policy.StartAt.UTC(), DryRun: policy.DryRun, Actions: map[RetentionAction]RetentionActionSummary{}, NextCursor: cursor}
	created, totalCreated, readCreated, err := s.store.listRetentionMediaPage(ctx, "created_at", policy.StartAt, cursor.CreatedOffset, limit)
	if err != nil {
		return report, err
	}
	report.ScannedCreated = readCreated
	report.NextCursor.CreatedOffset += readCreated
	report.CreatedDone = report.NextCursor.CreatedOffset >= totalCreated
	deleted, totalDeleted, readDeleted, err := s.store.listRetentionMediaPage(ctx, "deleted_at", policy.StartAt, cursor.DeletedOffset, limit)
	if err != nil {
		return report, err
	}
	report.ScannedDeleted = readDeleted
	report.NextCursor.DeletedOffset += readDeleted
	report.DeletedDone = report.NextCursor.DeletedOffset >= totalDeleted
	seen := map[string]struct{}{}
	for _, record := range append(created, deleted...) {
		if _, ok := seen[record.ObjectID]; ok {
			continue
		}
		seen[record.ObjectID] = struct{}{}
		candidate := RetentionCandidate{ObjectID: record.ObjectID, Class: record.RetentionClass, Scope: record.Scope, CreatedAt: record.CreatedAt, DeletedAt: record.DeletedAt, StoryExpiry: record.StoryExpiry}
		decision, eligible := evalPolicy.Decide(candidate, now)
		if !eligible {
			continue
		}
		summary := report.Actions[decision.Action]
		summary.Count++
		summary.LogicalBytes += record.Bytes
		report.Actions[decision.Action] = summary
		if policy.Enabled && !policy.DryRun {
			if _, err = s.jobs.Enqueue(ctx, decision, record.ObjectID); err != nil {
				return report, fmt.Errorf("enqueue %s job for %s: %w", decision.Action, record.ObjectID, err)
			}
			report.JobsConfirmed++
		}
	}
	return report, nil
}

func (s *AppwriteStore) listRetentionMediaPage(ctx context.Context, field string, cutoff time.Time, offset, limit int) ([]MediaRecord, int, int, error) {
	if field != "created_at" && field != "deleted_at" {
		return nil, 0, 0, errors.New("unsupported retention cutoff field")
	}
	query := string(appwriteGreaterThanEqualQuery(field, cutoff.UTC().Format(time.RFC3339Nano)))
	docs, total, err := s.db.ListDocumentsPage(ctx, mediaCollection, query, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	readCount := len(docs)
	out := make([]MediaRecord, 0, len(docs))
	for _, raw := range docs {
		var id struct {
			ID string `json:"$id"`
		}
		if err = json.Unmarshal(raw, &id); err != nil || !validDocID(id.ID) {
			return nil, 0, 0, errors.New("invalid media object ID in retention scan")
		}
		media, err := decodeRetentionMediaRecord(id.ID, raw)
		if err != nil {
			return nil, 0, 0, err
		}
		if field == "created_at" && (media.Status != "ready" || (media.RetentionClass != "permanent" && media.RetentionClass != "story")) {
			continue
		}
		if field == "deleted_at" && (media.Status != "user_deleted" || media.RetentionClass != "user_deleted" || media.DeletedAt.IsZero()) {
			continue
		}
		if (media.Scope != "post_media" && media.Scope != "story_media") || media.Bytes < 1 {
			continue
		}
		out = append(out, media)
	}
	return out, total, readCount, nil
}

func decodeRetentionMediaRecord(objectID string, raw []byte) (MediaRecord, error) {
	var wire struct {
		OwnerID        string `json:"owner_id"`
		ObjectKey      string `json:"object_key"`
		Bytes          string `json:"bytes_decimal"`
		ContentType    string `json:"mime_type"`
		SHA256         string `json:"sha256"`
		Status         string `json:"status"`
		Scope          string `json:"scope"`
		Class          string `json:"retention_class"`
		CreatedAt      string `json:"created_at"`
		DeletedAt      string `json:"deleted_at"`
		StoryExpiry    string `json:"story_expires_at"`
		LifecycleState string `json:"lifecycle_state"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return MediaRecord{}, errors.New("invalid retention media document")
	}
	bytes, err := strconv.ParseInt(wire.Bytes, 10, 64)
	if err != nil || bytes < 1 || !validOwnerID(wire.OwnerID) || !validObjectKey(wire.ObjectKey) || !validSHA256(wire.SHA256) || wire.ContentType == "" {
		return MediaRecord{}, errors.New("retention media document has invalid canonical metadata")
	}
	parse := func(value string) (time.Time, error) {
		if value == "" {
			return time.Time{}, nil
		}
		return time.Parse(time.RFC3339Nano, value)
	}
	created, err := parse(wire.CreatedAt)
	if err != nil {
		return MediaRecord{}, errors.New("invalid media created_at")
	}
	deleted, err := parse(wire.DeletedAt)
	if err != nil {
		return MediaRecord{}, errors.New("invalid media deleted_at")
	}
	expiry, err := parse(wire.StoryExpiry)
	if err != nil {
		return MediaRecord{}, errors.New("invalid story_expires_at")
	}
	return MediaRecord{ObjectID: objectID, OwnerID: wire.OwnerID, ObjectKey: wire.ObjectKey, Bytes: bytes, ContentType: wire.ContentType, SHA256: wire.SHA256, Status: wire.Status, Scope: wire.Scope, RetentionClass: wire.Class, CreatedAt: created, DeletedAt: deleted, StoryExpiry: expiry, LifecycleState: wire.LifecycleState}, nil
}
