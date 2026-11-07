package freshstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

// ArchiveCopier is implemented by the S3 provider. Implementations must stream
// and independently read back the destination before returning success.
type ArchiveCopier interface {
	CopyFrom(context.Context, *virtualstorage.S3Provider, string, string, string, string, int64, string, string) error
}

type ArchiveLedger interface {
	GetMediaRecord(context.Context, string) (MediaRecord, error)
	RecordArchiveReplica(context.Context, MediaRecord, string, string, string) error
}

type ArchiveWorker struct {
	source                      *virtualstorage.S3Provider
	archive                     ArchiveCopier
	ledger                      ArchiveLedger
	sourceBucket, archiveBucket string
}

func NewArchiveWorker(source *virtualstorage.S3Provider, archive ArchiveCopier, ledger ArchiveLedger, sourceBucket, archiveBucket string) (*ArchiveWorker, error) {
	if source == nil || archive == nil || ledger == nil || strings.TrimSpace(sourceBucket) == "" || strings.TrimSpace(archiveBucket) == "" {
		return nil, errors.New("B2 source, independent archive provider, ledger and buckets are required")
	}
	if sourceBucket == archiveBucket {
		return nil, errors.New("archive bucket must be distinct from active B2 bucket")
	}
	return &ArchiveWorker{source: source, archive: archive, ledger: ledger, sourceBucket: sourceBucket, archiveBucket: archiveBucket}, nil
}

func (w *ArchiveWorker) Run(ctx context.Context, job RetentionJob, dryRun bool) error {
	if job.Action != RetentionArchive || job.ObjectID == "" {
		return errors.New("archive worker received a non-archive job")
	}
	media, err := w.ledger.GetMediaRecord(ctx, job.ObjectID)
	if err != nil {
		return err
	}
	if media.Status != "ready" && media.Status != "archived" {
		return fmt.Errorf("media state %q is not archivable", media.Status)
	}
	if media.RetentionClass != "permanent" || media.Scope != "post_media" {
		return errors.New("only permanent post media is eligible for archive")
	}
	if dryRun {
		return nil
	}
	archiveKey := "archive/" + media.ObjectKey
	if err = w.archive.CopyFrom(ctx, w.source, w.sourceBucket, media.ObjectKey, w.archiveBucket, archiveKey, media.Bytes, media.ContentType, media.SHA256); err != nil {
		return fmt.Errorf("copy and independently verify archive replica: %w", err)
	}
	// Only a verified archive copy is recorded. Active B2/R2 replicas are not
	// deleted here; a separate replica-reclamation phase must verify every copy.
	if err = w.ledger.RecordArchiveReplica(ctx, media, "archive", w.archiveBucket, archiveKey); err != nil {
		return fmt.Errorf("record verified archive replica: %w", err)
	}
	return nil
}

func (s *AppwriteStore) RecordArchiveReplica(ctx context.Context, media MediaRecord, provider, bucket, key string) error {
	if provider != "archive" || !validDocID(media.ObjectID) || bucket == "" || key == "" || !validSHA256(media.SHA256) || media.Bytes < 1 {
		return errors.New("invalid verified archive replica metadata")
	}
	id := "archive_" + media.ObjectID
	data := map[string]any{"object_id": media.ObjectID, "provider": "archive", "bucket": bucket, "object_key": key, "bytes_decimal": fmt.Sprint(media.Bytes), "sha256": media.SHA256, "state": "verified", "verified_at": time.Now().UTC().Format(time.RFC3339Nano)}
	_, err := s.db.CreateDocument(ctx, replicaCollection, id, data, nil)
	if err == nil {
		return s.markArchiveVerified(ctx, media.ObjectID)
	}
	var apiErr appwritedb.HTTPError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		return err
	}
	raw, readErr := s.db.GetDocument(ctx, replicaCollection, id)
	if readErr != nil {
		return readErr
	}
	var fields map[string]any
	if jsonErr := json.Unmarshal(raw, &fields); jsonErr != nil {
		return jsonErr
	}
	if fields["object_id"] != media.ObjectID || fields["provider"] != "archive" || fields["bucket"] != bucket || fields["object_key"] != key || fields["sha256"] != media.SHA256 || fields["state"] != "verified" {
		return errors.New("existing archive replica ledger entry does not match verified bytes")
	}
	return s.markArchiveVerified(ctx, media.ObjectID)
}

func (s *AppwriteStore) markArchiveVerified(ctx context.Context, objectID string) error {
	_, err := s.db.UpdateDocument(ctx, mediaCollection, objectID, map[string]any{"archive_state": "verified", "archived_at": time.Now().UTC().Format(time.RFC3339Nano)})
	return err
}
