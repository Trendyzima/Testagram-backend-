package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

func main() {
	policy, err := freshstorage.RetentionPolicyFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	if !policy.Enabled || policy.DryRun {
		log.Fatal("retention worker is fail-closed: set RETENTION_ENABLED=true and DRY_RUN=false only after staging approval")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), "/")
	project := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	database := strings.TrimSpace(os.Getenv("APPWRITE_DATABASE_ID"))
	key := strings.TrimSpace(os.Getenv("APPWRITE_SERVER_API_KEY"))
	if endpoint == "" || project == "" || database == "" || key == "" {
		log.Fatal("Appwrite worker configuration is incomplete")
	}
	jobID := strings.TrimSpace(os.Getenv("TESTAGRAM_RETENTION_JOB_ID"))
	workerID := strings.TrimSpace(os.Getenv("TESTAGRAM_RETENTION_WORKER_ID"))
	if jobID == "" || workerID == "" {
		log.Fatal("TESTAGRAM_RETENTION_JOB_ID and TESTAGRAM_RETENTION_WORKER_ID are required")
	}
	db, err := appwritedb.New(appwritedb.Config{Endpoint: endpoint, ProjectID: project, DatabaseID: database, APIKey: key})
	if err != nil {
		log.Fatal(err)
	}
	jobs, err := freshstorage.NewAppwriteRetentionJobs(db)
	if err != nil {
		log.Fatal(err)
	}
	store, err := freshstorage.NewAppwriteStore(db)
	if err != nil {
		log.Fatal(err)
	}
	providers, err := freshstorage.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	b2, ok := providers.B2.(*virtualstorage.S3Provider)
	if !ok {
		log.Fatal("B2 provider is required")
	}
	r2, ok := providers.R2.(*virtualstorage.S3Provider)
	if !ok {
		log.Fatal("R2 provider is required")
	}
	archive, _ := providers.Archive.(*virtualstorage.S3Provider)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	job, err := jobs.Claim(ctx, jobID, workerID, now, 10*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	var runErr error
	switch job.Action {
	case freshstorage.RetentionArchive:
		if archive == nil || providers.ArchiveBucket == "" {
			runErr = logError("archive provider is not configured")
			break
		}
		worker, e := freshstorage.NewArchiveWorker(b2, archive, store, providers.B2Bucket, providers.ArchiveBucket)
		if e != nil {
			runErr = e
		} else {
			runErr = worker.Run(ctx, job, false)
		}
	case freshstorage.RetentionExpireStory:
		worker, e := freshstorage.NewStoryExpiryWorker(store, freshstorage.ReplicaProviders{B2: b2, R2: r2, Archive: archive})
		if e != nil {
			runErr = e
		} else {
			runErr = worker.Run(ctx, job, now, false)
		}
	case freshstorage.RetentionPurgeDeleted:
		worker, e := freshstorage.NewUserDeletionPurgeWorker(store, freshstorage.ReplicaProviders{B2: b2, R2: r2, Archive: archive}, policy.RecoveryWindow)
		if e != nil {
			runErr = e
		} else {
			runErr = worker.Run(ctx, job, now, false)
		}
	default:
		runErr = logError("unsupported retention job action")
	}
	if runErr != nil {
		if retryErr := jobs.Retry(ctx, job, runErr, time.Now().UTC()); retryErr != nil {
			log.Fatalf("worker failed (%v) and lease retry update failed: %v", runErr, retryErr)
		}
		log.Printf("retention job %s scheduled for retry: %v", job.ID, runErr)
		os.Exit(1)
	}
	if err = jobs.Complete(ctx, job, time.Now().UTC()); err != nil {
		log.Fatalf("complete retention job: %v", err)
	}
	log.Printf("retention job %s (%s) completed", job.ID, job.Action)
}
func logError(message string) error { return &workerError{message: message} }

type workerError struct{ message string }

func (e *workerError) Error() string { return e.message }
