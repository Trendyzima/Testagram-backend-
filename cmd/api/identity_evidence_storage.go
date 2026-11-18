package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *service) identityEvidenceStorageConfigured() bool {
	return s.identityEvidenceMedia != nil || (s.virtualStorage != nil && s.mediaReplicator != nil)
}

func (s *service) identityEvidenceStorageAvailable(ctx context.Context) error {
	if s.identityEvidenceMedia != nil {
		return s.identityEvidenceMedia.Ready(ctx)
	}
	if s.virtualStorage == nil || s.mediaReplicator == nil {
		return errors.New("private identity evidence storage is not configured")
	}
	return nil
}

func (s *service) identityEvidenceCreateUploadURL(ctx context.Context, ownerID, objectKey, contentType string, size int64) (string, error) {
	if s.identityEvidenceMedia != nil {
		grant, err := s.identityEvidenceMedia.CreateUploadURL(ctx, ownerID, identityEvidenceNamespace, objectKey, contentType, size)
		if err != nil {
			return "", err
		}
		return grant.UploadURL, nil
	}
	if s.virtualStorage == nil {
		return "", errors.New("private identity evidence storage is not configured")
	}
	return s.virtualStorage.UploadURL(ctx, ownerID, identityEvidenceNamespace, objectKey, contentType, size)
}

func (s *service) identityEvidenceVerifyAndReplicate(ctx context.Context, ownerID, objectKey, contentType string, size int64, sha256Hex string) error {
	if s.identityEvidenceMedia != nil {
		if _, err := s.identityEvidenceMedia.Verify(ctx, ownerID, identityEvidenceNamespace, objectKey, contentType, sha256Hex, size); err != nil {
			return err
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			status, err := s.identityEvidenceMedia.Status(ctx, ownerID, identityEvidenceNamespace, objectKey)
			if err != nil {
				return err
			}
			if status.Status == "protected" {
				if status.SHA256 != sha256Hex || status.SizeBytes != size || status.ContentType != contentType {
					return errors.New("Data Centre protected evidence metadata does not match the submitted object")
				}
				return nil
			}
			if status.Status == "replication_failed" || status.Status == "deleted" || status.Status == "delete_pending" {
				return fmt.Errorf("Data Centre evidence lifecycle did not reach protected state: %s", status.Status)
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("timed out waiting for Data Centre evidence replication: %w", ctx.Err())
			case <-ticker.C:
			}
		}
	}
	if s.virtualStorage == nil || s.mediaReplicator == nil {
		return errors.New("private identity evidence storage and replication are not configured")
	}
	if _, err := s.virtualStorage.VerifyUpload(ctx, ownerID, identityEvidenceNamespace, objectKey, contentType, size); err != nil {
		return err
	}
	if err := s.virtualStorage.VerifyUploadChecksum(ctx, ownerID, identityEvidenceNamespace, objectKey, maxIdentityEvidenceBytes, sha256Hex); err != nil {
		return err
	}
	_, err := s.mediaReplicator.Replicate(ctx, ownerID, identityEvidenceNamespace, objectKey, size, contentType, sha256Hex)
	return err
}

func (s *service) identityEvidenceDownload(ctx context.Context, ownerID, objectKey string, size int64, contentType, sha256Hex string) ([]byte, error) {
	if s.identityEvidenceMedia != nil {
		grant, err := s.identityEvidenceMedia.DownloadURL(ctx, ownerID, ownerID, identityEvidenceNamespace, objectKey)
		if err != nil {
			return nil, err
		}
		return fetchIdentityEvidenceBytes(ctx, grant.URL, size, contentType, sha256Hex)
	}
	if s.virtualStorage == nil || s.mediaReplicator == nil {
		return nil, errors.New("private identity evidence storage and replication are not configured")
	}
	primaryURL, err := s.virtualStorage.DownloadURL(ctx, ownerID, ownerID, identityEvidenceNamespace, objectKey)
	if err == nil {
		if payload, fetchErr := fetchIdentityEvidenceBytes(ctx, primaryURL, size, contentType, sha256Hex); fetchErr == nil {
			return payload, nil
		}
	}
	restoreCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	restoreErr := s.mediaReplicator.RestorePrimary(restoreCtx, ownerID, identityEvidenceNamespace, objectKey, size, contentType, sha256Hex)
	cancel()
	if restoreErr == nil {
		if restoredURL, urlErr := s.virtualStorage.DownloadURL(ctx, ownerID, ownerID, identityEvidenceNamespace, objectKey); urlErr == nil {
			if payload, fetchErr := fetchIdentityEvidenceBytes(ctx, restoredURL, size, contentType, sha256Hex); fetchErr == nil {
				return payload, nil
			}
		}
	}
	replicas, replicaErr := s.mediaReplicator.ReplicaDownloadURLs(ctx, ownerID, identityEvidenceNamespace, objectKey, size, contentType, sha256Hex)
	if replicaErr == nil {
		for _, replica := range replicas {
			if payload, fetchErr := fetchIdentityEvidenceBytes(ctx, replica.URL, size, contentType, sha256Hex); fetchErr == nil {
				return payload, nil
			}
		}
	}
	return nil, errors.New("verified identity evidence could not be retrieved from primary or replicas")
}

func (s *service) identityEvidenceDelete(ctx context.Context, ownerID, objectKey string) error {
	if s.identityEvidenceMedia != nil {
		state, err := s.identityEvidenceMedia.Delete(ctx, ownerID, ownerID, identityEvidenceNamespace, objectKey)
		if err != nil || state == "deleted" {
			return err
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			status, statusErr := s.identityEvidenceMedia.Status(ctx, ownerID, identityEvidenceNamespace, objectKey)
			if statusErr != nil {
				return statusErr
			}
			if status.Status == "deleted" {
				return nil
			}
			if status.Status != "delete_pending" {
				return fmt.Errorf("Data Centre evidence deletion entered unexpected state %q", status.Status)
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("timed out waiting for Data Centre evidence deletion: %w", ctx.Err())
			case <-ticker.C:
			}
		}
	}
	if s.mediaReplicator == nil {
		return errors.New("private identity evidence replication is not configured")
	}
	return s.mediaReplicator.Delete(ctx, ownerID, identityEvidenceNamespace, objectKey)
}
