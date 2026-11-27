package main

import (
	"context"
	"log"
	"time"
)

type identityEvidenceRetentionItem struct {
	id            string
	sessionID     string
	applicationID string
	objectKey     string
	status        string
}

// runIdentityEvidenceRetentionWorker removes expired identity evidence from
// the dedicated private Backblaze B2 identity-evidence bucket only. Quarantine is committed first so
// expired evidence immediately disappears from reviewer queues; failed deletes
// remain quarantined and are retried against B2 on the next pass.
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
	if s.db == nil || !s.identityEvidenceStorageConfigured() {
		return nil
	}
	batchCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	tx, err := s.db.Begin(batchCtx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	rows, err := tx.Query(batchCtx, `SELECT e.id::text,e.session_id::text,v.application_id::text,e.object_key,e.status
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
		if err := rows.Scan(&item.id, &item.sessionID, &item.applicationID, &item.objectKey, &item.status); err != nil {
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
		if item.status != "quarantined" {
			_, err = tx.Exec(batchCtx, `INSERT INTO testagram_identity.verification_events
				(session_id,event_type,actor_type,actor_reference,request_id,metadata)
				VALUES ($1::uuid,'identity_evidence_quarantined','system',
				        'identity-evidence-retention-worker',$2,
				        jsonb_build_object('evidence_id',$3::text,'retention_action','quarantine'))`,
				item.sessionID, "retention-quarantine:"+item.id, item.id)
			if err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(batchCtx); err != nil {
		return err
	}

	for _, item := range items {
		deleteCtx, deleteCancel := context.WithTimeout(batchCtx, 12*time.Second)
		deleteErr := s.identityEvidenceDelete(deleteCtx, item.applicationID, item.objectKey)
		deleteCancel()
		if deleteErr != nil {
			// Do not log object keys or evidence content metadata. Quarantined rows
			// remain eligible for retry, and provider DELETE is idempotent.
			log.Printf("identity evidence retention delete will retry for evidence %s", item.id)
			continue
		}

		deleteTx, err := s.db.Begin(batchCtx)
		if err != nil {
			return err
		}
		tag, err := deleteTx.Exec(batchCtx, `UPDATE testagram_identity.verification_evidence
			SET status='deleted',verified_at=NULL,uploaded_at=COALESCE(uploaded_at,now())
			WHERE id=$1::uuid AND status='quarantined'`, item.id)
		if err != nil {
			_ = deleteTx.Rollback(context.Background())
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err = deleteTx.Exec(batchCtx, `INSERT INTO testagram_identity.verification_events
				(session_id,event_type,actor_type,actor_reference,request_id,metadata)
				VALUES ($1::uuid,'identity_evidence_deleted','system',
				        'identity-evidence-retention-worker',$2,
				        jsonb_build_object('evidence_id',$3::text,'retention_action','provider_delete_confirmed'))`,
				item.sessionID, "retention-delete:"+item.id, item.id)
			if err != nil {
				_ = deleteTx.Rollback(context.Background())
				return err
			}
		}
		if err := deleteTx.Commit(batchCtx); err != nil {
			return err
		}
	}
	return nil
}
