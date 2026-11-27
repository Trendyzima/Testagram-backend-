package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/datacentreclient"
)

type identityEvidenceMediaClient interface {
	Ready(context.Context) error
	CreateUploadURL(context.Context, string, string, string, string, int64) (datacentreclient.UploadGrant, error)
	Verify(context.Context, string, string, string, string, string, int64) (datacentreclient.Verification, error)
	Status(context.Context, string, string, string) (datacentreclient.MediaStatus, error)
	DownloadURL(context.Context, string, string, string, string) (datacentreclient.DownloadGrant, error)
	Delete(context.Context, string, string, string, string) (string, error)
}

// Identity evidence is owned by the Data Centre media service. There is
// deliberately no Backend-local object-store fallback: missing service
// configuration or readiness keeps public registration fail-closed.
func (s *service) identityEvidenceStorageConfigured() bool {
	return s.identityEvidenceMedia != nil
}

func (s *service) identityEvidenceStorageAvailable(ctx context.Context) error {
	if s.identityEvidenceMedia == nil {
		return errors.New("private Data Centre identity evidence service is not configured")
	}
	return s.identityEvidenceMedia.Ready(ctx)
}

func (s *service) identityEvidenceCreateUploadURL(ctx context.Context, ownerID, objectKey, contentType string, size int64) (string, error) {
	if s.identityEvidenceMedia == nil {
		return "", errors.New("private Data Centre identity evidence service is not configured")
	}
	grant, err := s.identityEvidenceMedia.CreateUploadURL(ctx, ownerID, identityEvidenceNamespace, objectKey, contentType, size)
	if err != nil {
		return "", err
	}
	return grant.UploadURL, nil
}

// Verify the uploaded object in the Data Centre's dedicated Backblaze B2 bucket and wait for its protected state. No cross-provider replica is involved.
func (s *service) identityEvidenceVerifyAndReplicate(ctx context.Context, ownerID, objectKey, contentType string, size int64, sha256Hex string) error {
	if s.identityEvidenceMedia == nil {
		return errors.New("private Data Centre identity evidence service is not configured")
	}
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
			return fmt.Errorf("timed out waiting for Data Centre B2 evidence verification: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *service) identityEvidenceDownload(ctx context.Context, ownerID, objectKey string, size int64, contentType, sha256Hex string) ([]byte, error) {
	if s.identityEvidenceMedia == nil {
		return nil, errors.New("private Data Centre identity evidence service is not configured")
	}
	grant, err := s.identityEvidenceMedia.DownloadURL(ctx, ownerID, ownerID, identityEvidenceNamespace, objectKey)
	if err != nil {
		return nil, err
	}
	return fetchIdentityEvidenceBytes(ctx, grant.URL, size, contentType, sha256Hex)
}

func (s *service) identityEvidenceDelete(ctx context.Context, ownerID, objectKey string) error {
	if s.identityEvidenceMedia == nil {
		return errors.New("private Data Centre identity evidence service is not configured")
	}
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
