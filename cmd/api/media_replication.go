package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mediaReplicationJob struct {
	ID int64
	ObjectKey string
	Operation string
	OwnerID string
	Namespace string
	ContentType string
	SizeBytes int64
	Checksum string
	LeaseToken string
	Attempts int
}

func newMediaReplicaProvider(prefix, defaultRegion string) (*virtualstorage.S3Provider, error) {
	endpoint := strings.TrimSpace(os.Getenv(prefix + "_ENDPOINT"))
	accessKey := strings.TrimSpace(os.Getenv(prefix + "_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv(prefix + "_SECRET_ACCESS_KEY"))
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("%s endpoint, access key and secret key are required", prefix)
	}
	region := strings.TrimSpace(os.Getenv(prefix + "_REGION"))
	if region == "" { region = defaultRegion }
	provider, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{
		Endpoint: endpoint, Region: region, AccessKey: accessKey, SecretKey: secretKey,
		Client: &http.Client{Timeout: 8 * time.Second},
	})
	if err != nil { return nil, fmt.Errorf("%s provider: %w", prefix, err) }
	return provider, nil
}

func replicaBucket(prefix, visibility, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(prefix + "_" + strings.ToUpper(visibility) + "_BUCKET")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(fallback))
}

func configureMediaReplicator(primary *virtualstorage.S3Provider) (*virtualstorage.Replicator, error) {
	if primary == nil { return nil, errors.New("Instabox VPS primary provider is not configured") }
	r2, err := newMediaReplicaProvider("CLOUDFLARE_R2", "auto")
	if err != nil { return nil, err }
	b2, err := newMediaReplicaProvider("B2_S3", "us-east-005")
	if err != nil { return nil, err }
	primaryBuckets := map[string]string{
		"media-private": env("MEDIA_PRIVATE_BUCKET", env("MINIO_BUCKET", "testagram-private-media")),
		"media-public": env("MEDIA_PUBLIC_BUCKET", env("PUBLIC_MEDIA_BUCKET", "testagram-public-media")),
	}
	r2Buckets := map[string]string{
		"media-private": replicaBucket("CLOUDFLARE_R2", "private", "CLOUDFLARE_R2_BUCKET"),
		"media-public": replicaBucket("CLOUDFLARE_R2", "public", "CLOUDFLARE_R2_BUCKET"),
	}
	b2Buckets := map[string]string{
		"media-private": replicaBucket("B2_S3", "private", "B2_S3_BUCKET"),
		"media-public": replicaBucket("B2_S3", "public", "B2_S3_BUCKET"),
	}
	for _, buckets := range []map[string]string{primaryBuckets,r2Buckets,b2Buckets} {
		if buckets["media-private"] == "" || buckets["media-public"] == "" {
			return nil, errors.New("private and public buckets must be configured for all three media providers")
		}
	}
	return virtualstorage.NewReplicator("instabox-vps", primary, primaryBuckets, []virtualstorage.ReplicaTarget{
		{Name:"cloudflare-r2", Provider:r2, Buckets:r2Buckets},
		{Name:"backblaze-b2", Provider:b2, Buckets:b2Buckets},
	})
}

func claimMediaReplicationJobs(ctx context.Context, db *pgxpool.Pool, limit int) ([]mediaReplicationJob, error) {
	rows, err := db.Query(ctx, `
		WITH picked AS (
			SELECT id FROM testagram_media.media_replication_outbox
			WHERE (status='pending' AND available_at <= now())
			   OR (status='processing' AND lease_until < now())
			ORDER BY available_at, created_at, id
			FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE testagram_media.media_replication_outbox AS q
		SET status='processing', attempts=q.attempts+1,
		    lease_until=now()+interval '2 minutes', lease_token=gen_random_uuid(), updated_at=now()
		FROM picked WHERE q.id=picked.id
		RETURNING q.id,q.object_key,q.operation,q.owner_id,q.storage_namespace,q.content_type,
		          q.size_bytes,q.checksum_sha256,q.lease_token::text,q.attempts
	`, limit)
	if err != nil { return nil, fmt.Errorf("claim media replication jobs: %w",err) }
	defer rows.Close()
	jobs:=make([]mediaReplicationJob,0,limit)
	for rows.Next() {
		var j mediaReplicationJob
		if err:=rows.Scan(&j.ID,&j.ObjectKey,&j.Operation,&j.OwnerID,&j.Namespace,&j.ContentType,&j.SizeBytes,&j.Checksum,&j.LeaseToken,&j.Attempts);err!=nil{return nil,err}
		jobs=append(jobs,j)
	}
	return jobs,rows.Err()
}

func completeMediaReplicationJob(ctx context.Context, db *pgxpool.Pool, job mediaReplicationJob, results []virtualstorage.ReplicaResult) error {
	tx,err:=db.Begin(ctx);if err!=nil{return err};defer func(){_ = tx.Rollback(context.Background())}()
	if job.Operation == "delete" {
		if _,err=tx.Exec(ctx,`DELETE FROM testagram_media.storage_replicas WHERE object_key=$1`,job.ObjectKey);err!=nil{return err}
	} else {
	for _,result:=range results {
		_,err=tx.Exec(ctx,`
			INSERT INTO testagram_media.storage_replicas
			  (object_key,provider,bucket,object_size_bytes,checksum_sha256,state,verified_at,last_error_code,attempts,updated_at)
			VALUES ($1,$2,$3,$4,$5,'verified',now(),'',$6,now())
			ON CONFLICT (object_key,provider) DO UPDATE SET
			  bucket=EXCLUDED.bucket,object_size_bytes=EXCLUDED.object_size_bytes,
			  checksum_sha256=EXCLUDED.checksum_sha256,state='verified',verified_at=now(),
			  last_error_code='',attempts=testagram_media.storage_replicas.attempts+1,updated_at=now()
		`,job.ObjectKey,result.Provider,result.Bucket,result.Size,result.SHA256,job.Attempts)
		if err!=nil{return fmt.Errorf("write verified replica placement: %w",err)}
	}
	}
	tag,err:=tx.Exec(ctx,`UPDATE testagram_media.media_replication_outbox
		SET status='complete',completed_at=now(),lease_until=NULL,lease_token=NULL,last_error_code='',updated_at=now()
		WHERE id=$1 AND status='processing' AND lease_token=$2::uuid AND lease_until>now()`,job.ID,job.LeaseToken)
	if err!=nil{return err};if tag.RowsAffected()!=1{return errors.New("media replication lease was lost before completion")}
	return tx.Commit(ctx)
}

func retryMediaReplicationJob(ctx context.Context, db *pgxpool.Pool, job mediaReplicationJob, code string) error {
	if code=="" { code="replication_failed" }
	_,err:=db.Exec(ctx,`
		UPDATE testagram_media.media_replication_outbox
		SET status=CASE WHEN attempts>=20 THEN 'dead_letter' ELSE 'pending' END,
		    available_at=now()+LEAST(3600,(5*power(2,LEAST(attempts,10)))::integer)*interval '1 second',
		    failed_at=CASE WHEN attempts>=20 THEN now() ELSE NULL END,
		    lease_until=NULL,lease_token=NULL,last_error_code=$3,updated_at=now()
		WHERE id=$1 AND status='processing' AND lease_token=$2::uuid
	`,job.ID,job.LeaseToken,code)
	return err
}

func mediaReplicationErrorCode(err error) string {
	switch {
	case errors.Is(err,virtualstorage.ErrChecksumMismatch): return "primary_checksum_mismatch"
	case errors.Is(err,virtualstorage.ErrObjectMismatch): return "primary_object_mismatch"
	case errors.Is(err,virtualstorage.ErrNotFound): return "primary_object_missing"
	default: return "replica_provider_unavailable"
	}
}

func runMediaReplicationWorker(ctx context.Context, db *pgxpool.Pool, replicator *virtualstorage.Replicator) {
	if db==nil || replicator==nil { return }
	ticker:=time.NewTicker(3*time.Second);defer ticker.Stop()
	for {
		select { case <-ctx.Done(): return; case <-ticker.C: }
		claimCtx,cancel:=context.WithTimeout(ctx,10*time.Second)
		jobs,err:=claimMediaReplicationJobs(claimCtx,db,10);cancel()
		if err!=nil { log.Printf("media replication claim failed: %v",err);continue }
		for _,job:=range jobs {
			workCtx,workCancel:=context.WithTimeout(ctx,90*time.Second)
			var results []virtualstorage.ReplicaResult
			var copyErr error
			if job.Operation == "delete" { copyErr=replicator.Delete(workCtx,job.OwnerID,job.Namespace,job.ObjectKey) } else { results,copyErr=replicator.Replicate(workCtx,job.OwnerID,job.Namespace,job.ObjectKey,job.SizeBytes,job.ContentType,job.Checksum) }
			if copyErr==nil {
				copyErr=completeMediaReplicationJob(workCtx,db,job,results)
			}
			if copyErr!=nil {
				code:=mediaReplicationErrorCode(copyErr)
				if err:=retryMediaReplicationJob(workCtx,db,job,code);err!=nil && !errors.Is(err,pgx.ErrTxClosed) {
					log.Printf("media replication retry state failed job=%d: %v",job.ID,err)
				}
				log.Printf("media replication attempt failed job=%d attempt=%d code=%s",job.ID,job.Attempts,code)
			}
			workCancel()
		}
	}
}
