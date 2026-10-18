package main

import (
	"context"
	"errors"
	"log"
	"math"
	"time"
)

// runStorageDeletionWorker drains durable object-deletion jobs. It is safe to
// run on multiple API instances: jobs are claimed with SKIP LOCKED and provider
// DELETE is expected to be idempotent.
func (s *service) runStorageDeletionWorker(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.processStorageDeletionBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("storage deletion worker pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type storageDeletionJob struct {
	id int64
	key, ownerID, visibility, reason, externalID string
	attempts int
}

// processStorageDeletionBatch claims a bounded batch transactionally, then
// performs network I/O outside the transaction so slow object stores never hold
// row locks. A stale processing lease is recoverable after a process crash.
func (s *service) processStorageDeletionBatch(ctx context.Context) error {
	if s.db == nil || s.media == nil {
		return nil
	}
	batchCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	tx, err := s.db.Begin(batchCtx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(batchCtx, `update testagram_media.storage_deletion_jobs
		set status='retry', locked_at=null, available_at=least(available_at, now()), updated_at=now()
		where status='processing' and locked_at < now() - interval '10 minutes'`); err != nil {
		return err
	}

	rows, err := tx.Query(batchCtx, `with picked as (
		select id from testagram_media.storage_deletion_jobs
		where status in ('pending','retry') and available_at <= now()
		order by available_at, id
		for update skip locked
		limit 20
	)
	update testagram_media.storage_deletion_jobs j
	set status='processing', locked_at=now(), attempts=j.attempts+1, updated_at=now()
	from picked
	where j.id=picked.id
	returning j.id, j.object_key, j.owner_id, j.visibility, j.reason, j.attempts`)
	if err != nil {
		return err
	}
	jobs := make([]storageDeletionJob, 0, 20)
	for rows.Next() {
		var job storageDeletionJob
		if err := rows.Scan(&job.id, &job.key, &job.ownerID, &job.visibility, &job.reason, &job.attempts); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err := tx.Commit(batchCtx); err != nil {
		return err
	}

	for _, job := range jobs {
		jobCtx, jobCancel := context.WithTimeout(batchCtx, 8*time.Second)
		err := s.performStorageDeletion(jobCtx, job)
		jobCancel()
		if err != nil {
			backoff := time.Duration(math.Min(float64(time.Hour), float64(5*time.Second)*math.Pow(2, float64(minInt(job.attempts-1, 10)))))
			status := "retry"
			if job.attempts >= 12 {
				status = "dead_letter"
			}
			_, updateErr := s.db.Exec(batchCtx, `update testagram_media.storage_deletion_jobs
				set status=$2, locked_at=null, available_at=now()+($3 * interval '1 second'),
				    last_error_code=$4, updated_at=now()
				where id=$1 and status='processing'`, job.id, status, int64(backoff/time.Second), "provider_delete_failed")
			if updateErr != nil {
				return updateErr
			}
			continue
		}
		if _, err := s.db.Exec(batchCtx, `update testagram_media.storage_deletion_jobs
			set status='completed', completed_at=now(), locked_at=null, last_error_code='', updated_at=now()
			where id=$1 and status='processing'`, job.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) performStorageDeletion(ctx context.Context, job storageDeletionJob) error {
	if err := s.deleteMediaObject(ctx, job.ownerID, job.key, job.visibility); err != nil {
		return err
	}
	if job.reason == "user_delete" {
		var externalID string
		err := s.db.QueryRow(ctx, `select coalesce(external_media_id::text,'')
			from testagram_media.assets where object_key=$1 and owner_id=$2`, job.key, job.ownerID).Scan(&externalID)
		if err != nil && !errors.Is(err, context.Canceled) {
			// An already-removed asset is safe to treat as completed after object
			// deletion; there is no remaining product-media link to update.
			externalID = ""
		}
		if err := s.deleteProductMedia(ctx, externalID); err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `update testagram_media.assets
			set status='deleted', updated_at=now()
			where object_key=$1 and owner_id=$2 and status='deletion_pending'`, job.key, job.ownerID); err != nil {
			return err
		}
	}
	return nil
}

func minInt(a, b int) int {
	if a < b { return a }
	return b
}
