package freshstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

type ArchiveRestoreLedger interface {
	GetMediaRecord(context.Context, string) (MediaRecord, error)
	GetArchiveReplica(context.Context, string) (MediaReplica, error)
	MarkReplicaState(context.Context, string, string, string) error
}

// RestoreArchive restores the owner-authorized canonical media from its verified
// cold replica into both active origins. It never changes ownership or logical
// quota, and it only marks replicas verified after independent provider read-back.
func (s *Service) RestoreArchive(ctx context.Context, ownerID, objectID string) error {
	if s.archive == nil {
		return errors.New("cold archive provider is not configured")
	}
	ledger, ok := s.store.(ArchiveRestoreLedger)
	if !ok {
		return errors.New("storage ledger does not support archive restoration")
	}
	media, err := ledger.GetMediaRecord(ctx, objectID)
	if err != nil {
		return err
	}
	if media.OwnerID != ownerID {
		return ErrNotOwner
	}
	if media.Status != "ready" || media.LifecycleState == "purging" || media.Bytes < 1 || !validSHA256(media.SHA256) {
		return ErrNotReady
	}
	replica, err := ledger.GetArchiveReplica(ctx, objectID)
	if err != nil {
		return err
	}
	if replica.Provider != "archive" || replica.State != "verified" || replica.ObjectID != objectID || replica.Bytes != media.Bytes || replica.SHA256 != media.SHA256 {
		return errors.New("archive replica ledger is not verified against canonical media metadata")
	}
	archiveInfo, err := s.archive.Head(ctx, replica.Bucket, replica.Key)
	if err != nil {
		return err
	}
	if archiveInfo.Size != media.Bytes {
		return errors.New("archive object size does not match canonical media metadata")
	}
	if err = s.b2.CopyFrom(ctx, s.archive, replica.Bucket, replica.Key, s.b2Bucket, media.ObjectKey, media.Bytes, media.ContentType, media.SHA256); err != nil {
		return fmt.Errorf("restore active B2 replica: %w", err)
	}
	if err = s.r2.CopyFrom(ctx, s.b2, s.b2Bucket, media.ObjectKey, s.r2Bucket, media.ObjectKey, media.Bytes, media.ContentType, media.SHA256); err != nil {
		return fmt.Errorf("restore active R2 replica: %w", err)
	}
	for _, provider := range []string{"b2", "r2"} {
		if err = ledger.MarkReplicaState(ctx, objectID, provider, "verified"); err != nil {
			return fmt.Errorf("record restored %s replica: %w", provider, err)
		}
	}
	return nil
}

func (s *AppwriteStore) GetArchiveReplica(ctx context.Context, objectID string) (MediaReplica, error) {
	if !validDocID(objectID) {
		return MediaReplica{}, ErrNotReady
	}
	id := "archive_" + objectID
	raw, err := s.db.GetDocument(ctx, replicaCollection, id)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return MediaReplica{}, ErrNotReady
		}
		return MediaReplica{}, err
	}
	var wire struct {
		ID       string `json:"$id"`
		ObjectID string `json:"object_id"`
		Provider string `json:"provider"`
		Bucket   string `json:"bucket"`
		Key      string `json:"object_key"`
		Bytes    string `json:"bytes_decimal"`
		SHA256   string `json:"sha256"`
		State    string `json:"state"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return MediaReplica{}, err
	}
	n, err := strconv.ParseInt(wire.Bytes, 10, 64)
	if err != nil || n < 1 || wire.ObjectID != objectID || wire.Provider != "archive" || wire.Bucket == "" || wire.Key == "" || !validSHA256(wire.SHA256) {
		return MediaReplica{}, errors.New("invalid archive replica ledger document")
	}
	return MediaReplica{ID: wire.ID, ObjectID: wire.ObjectID, Provider: wire.Provider, Bucket: wire.Bucket, Key: wire.Key, Bytes: n, SHA256: wire.SHA256, State: wire.State}, nil
}
