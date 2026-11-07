package freshstorage

import (
	"context"
	"errors"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

type archiveCopyFake struct {
	calls                                                      int
	err                                                        error
	sourceBucket, destinationBucket, sourceKey, destinationKey string
	size                                                       int64
	checksum                                                   string
}

func (f *archiveCopyFake) CopyFrom(_ context.Context, _ *virtualstorage.S3Provider, sb, sk, db, dk string, size int64, _ string, hash string) error {
	f.calls++
	f.sourceBucket = sb
	f.destinationBucket = db
	f.sourceKey = sk
	f.destinationKey = dk
	f.size = size
	f.checksum = hash
	return f.err
}

type archiveLedgerFake struct {
	media   MediaRecord
	records int
	err     error
}

func (f *archiveLedgerFake) GetMediaRecord(context.Context, string) (MediaRecord, error) {
	return f.media, nil
}
func (f *archiveLedgerFake) RecordArchiveReplica(context.Context, MediaRecord, string, string, string) error {
	f.records++
	return f.err
}

func TestArchiveWorkerDryRunDoesNotCopyOrWriteLedger(t *testing.T) {
	copier := &archiveCopyFake{}
	ledger := &archiveLedgerFake{media: MediaRecord{ObjectID: "med_1", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 10, ContentType: "image/jpeg", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "ready", RetentionClass: "permanent", Scope: "post_media"}}
	worker, err := NewArchiveWorker(&virtualstorage.S3Provider{}, copier, ledger, "active-b2", "cold-archive")
	if err != nil {
		t.Fatal(err)
	}
	err = worker.Run(context.Background(), RetentionJob{Action: RetentionArchive, ObjectID: "med_1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if copier.calls != 0 || ledger.records != 0 {
		t.Fatalf("dry-run had side effects: copy=%d ledger=%d", copier.calls, ledger.records)
	}
}

func TestArchiveWorkerRecordsOnlyAfterVerifiedCopy(t *testing.T) {
	media := MediaRecord{ObjectID: "med_1", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 10, ContentType: "image/jpeg", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "ready", RetentionClass: "permanent", Scope: "post_media"}
	copier := &archiveCopyFake{}
	ledger := &archiveLedgerFake{media: media}
	worker, err := NewArchiveWorker(&virtualstorage.S3Provider{}, copier, ledger, "active-b2", "cold-archive")
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), RetentionJob{Action: RetentionArchive, ObjectID: "med_1"}, false); err != nil {
		t.Fatal(err)
	}
	if copier.calls != 1 || ledger.records != 1 || copier.sourceBucket != "active-b2" || copier.destinationBucket != "cold-archive" || copier.destinationKey != "archive/"+media.ObjectKey || copier.checksum != media.SHA256 {
		t.Fatalf("archive copy/ledger mismatch: %#v %#v", copier, ledger)
	}
}

func TestArchiveWorkerNeverRecordsFailedCopy(t *testing.T) {
	media := MediaRecord{ObjectID: "med_1", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 10, ContentType: "image/jpeg", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "ready", RetentionClass: "permanent", Scope: "post_media"}
	copier := &archiveCopyFake{err: errors.New("checksum mismatch")}
	ledger := &archiveLedgerFake{media: media}
	worker, _ := NewArchiveWorker(&virtualstorage.S3Provider{}, copier, ledger, "active-b2", "cold-archive")
	if err := worker.Run(context.Background(), RetentionJob{Action: RetentionArchive, ObjectID: "med_1"}, false); err == nil {
		t.Fatal("failed archive verification accepted")
	}
	if ledger.records != 0 {
		t.Fatal("unverified archive was recorded")
	}
}
