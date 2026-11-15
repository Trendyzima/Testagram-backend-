package main

import (
	"context"
	"errors"
	"log"
	"time"
)

// runStorageCleanupWorker performs a bounded weekly sweep of expired upload
// reservations and orphaned upload objects. It never selects ready media assets,
// posts, profile media, messages, or identity evidence for deletion. The existing
// storage deletion worker drains the durable jobs created by this sweep.
func (s *service) runStorageCleanupWorker(ctx context.Context) {
	ticker := time.NewTicker(7 * 24 * time.Hour)
	defer ticker.Stop()

	if err := s.scheduleExpiredMediaCleanup(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("weekly media cleanup initial pass failed: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.scheduleExpiredMediaCleanup(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("weekly media cleanup pass failed: %v", err)
			}
		}
	}
}

func (s *service) scheduleExpiredMediaCleanup(parent context.Context) error {
	if s.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	// Release reservations whose upload-intent expiry has passed. The database
	// function uses SKIP LOCKED and is safe alongside multiple API replicas.
	if _, err := s.db.Exec(ctx, `SELECT testagram_storage.expire_abandoned_reservations(500)`); err != nil {
		return err
	}

	// Reap stale in-flight claims, then enqueue only expired/cleanup-pending
	// intents that have no corresponding asset row. A deletion is idempotent;
	// provider-specific routing remains in performStorageDeletion.
	_, err := s.db.Exec(ctx, `
		UPDATE testagram_media.upload_intents
		   SET status='expired', updated_at=now()
		 WHERE status IN ('issued','uploading')
		   AND expires_at < now() - interval '2 hours';

		INSERT INTO testagram_media.storage_deletion_jobs
		  (object_key, owner_id, visibility, storage_namespace, storage_provider, reason, status)
		SELECT i.object_key, i.owner_id, i.visibility, i.storage_namespace, i.storage_provider,
		       'expired_upload', 'pending'
		  FROM testagram_media.upload_intents i
		 WHERE i.status IN ('expired','cleanup_pending')
		   AND i.updated_at < now() - interval '15 minutes'
		   AND NOT EXISTS (
		     SELECT 1 FROM testagram_media.assets a WHERE a.object_key=i.object_key
		   )
		   AND NOT EXISTS (
		     SELECT 1 FROM testagram_media.storage_deletion_jobs j
		      WHERE j.object_key=i.object_key AND j.reason='expired_upload'
		        AND j.status IN ('pending','processing','retry')
		   )
		ON CONFLICT (object_key, reason)
		  WHERE status IN ('pending','processing','retry')
		DO NOTHING
	`)
	return err
}
