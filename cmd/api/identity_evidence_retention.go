package main

import (
	"context"
	"log"
	"time"
)

type identityEvidenceRetentionItem struct {
	id            string
	applicationID string
	objectKey     string
}

// runIdentityEvidenceRetentionWorker removes expired identity evidence from
// every configured private storage provider. Quarantine is committed first so
// expired evidence immediately disappears from reviewer queues; failed deletes
// remain quarantined and are retried on the next pass.
func (s *service) runIdentityEvidenceRetentionWorker(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		if err := s.processIdentityEvidenceRetentionBatch(ctx); err != nil && ctx.Err() == nil {
			log.Printf("identity evidence retention pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *service) processIdentityEvidenceRetentionBatch(ctx context.Context) error {
	if s.db == nil || s.virtualStorage == nil || s.mediaReplicator == nil {
		return nil
	}
	batchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	tx, err := s.db.Begin(batchCtx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	rows, err := tx.Query(batchCtx, `SELECT e.id::text,v.application_id::text,e.object_key
		FROM testagram_identity.verification_evidence e
		JOIN testagram_identity.verification_sessions v ON v.id=e.session_id
		WHERE e.retention_until<=now()
		  AND e.status IN ('upload_pending','verified','quarantined')
		ORDER BY e.retention_until,e.id
		FOR UPDATE OF e SKIP LOCKED
		LIMIT 50`)
	if err != nil {
		return err
	}
	items := make([]identityEvidenceRetentionItem, 0, 50)
	for rows.Next() {
		var item identityEvidenceRetentionItem
		if err := rows.Scan(&item.id, &item.applicationID, &item.objectKey); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, item := range items {
		_, err := tx.Exec(batchCtx, `UPDATE testagram_identity.verification_evidence
			SET status='quarantined',uploaded_at=COALESCE(uploaded_at,now()),verified_at=NULL
			WHERE id=$1::uuid AND status IN ('upload_pending','verified','quarantined')`, item.id)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(batchCtx); err != nil {
		return err
	}

	for _, item := range items {
		deleteCtx, deleteCancel := context.WithTimeout(batchCtx, 12*time.Second)
		deleteErr := s.mediaReplicator.Delete(deleteCtx, item.applicationID, identityEvidenceNamespace, item.objectKey)
		deleteCancel()
		if deleteErr != nil {
			// Do not log object keys or evidence content metadata. Quarantined rows
			// remain eligible for retry, and provider DELETE is idempotent.
			log.Printf("identity evidence retention delete will retry for evidence %s", item.id)
			continue
		}

		_, err := s.db.Exec(batchCtx, `UPDATE testagram_identity.verification_evidence
			SET status='deleted',verified_at=NULL,uploaded_at=COALESCE(uploaded_at,now())
			WHERE id=$1::uuid AND status='quarantined'`, item.id)
		if err != nil {
			return err
		}
	}
	return nil
}
