package freshstorage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

const retentionJobsCollection = "retention_jobs"

var (
	ErrRetentionJobNotFound = errors.New("retention job not found")
	ErrRetentionJobClaimed  = errors.New("retention job is already leased by another worker")
	ErrRetentionLeaseLost   = errors.New("retention job lease was lost")
)

type RetentionJob struct {
	ID          string          `json:"$id"`
	JobKey      string          `json:"job_key"`
	ObjectID    string          `json:"object_id"`
	Action      RetentionAction `json:"action"`
	Policy      string          `json:"policy_version"`
	DueAt       time.Time       `json:"due_at"`
	Status      string          `json:"status"`
	WorkerID    string          `json:"worker_id,omitempty"`
	LeaseToken  string          `json:"lease_token,omitempty"`
	LeaseUntil  time.Time       `json:"lease_until,omitempty"`
	Attempts    int             `json:"attempts"`
	LastError   string          `json:"last_error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	CompletedAt time.Time       `json:"completed_at,omitempty"`
}

type RetentionJobRepository interface {
	Enqueue(context.Context, RetentionDecision, string) (RetentionJob, error)
	Claim(context.Context, string, string, time.Time, time.Duration) (RetentionJob, error)
	Renew(context.Context, RetentionJob, time.Time, time.Duration) error
	Complete(context.Context, RetentionJob, time.Time) error
	Retry(context.Context, RetentionJob, error, time.Time) error
}

type AppwriteRetentionJobs struct{ db *appwritedb.Client }

func NewAppwriteRetentionJobs(db *appwritedb.Client) (*AppwriteRetentionJobs, error) {
	if db == nil {
		return nil, errors.New("Appwrite Database client is required for retention jobs")
	}
	return &AppwriteRetentionJobs{db: db}, nil
}

func retentionJobDocumentID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "rt_" + hex.EncodeToString(sum[:16])
}

func (r *AppwriteRetentionJobs) Enqueue(ctx context.Context, decision RetentionDecision, objectID string) (RetentionJob, error) {
	if len(decision.JobKey) != 64 || !validDocID(objectID) || decision.DueAt.IsZero() {
		return RetentionJob{}, errors.New("invalid retention job identity or due time")
	}
	if decision.Action != RetentionExpireStory && decision.Action != RetentionArchive && decision.Action != RetentionPurgeDeleted {
		return RetentionJob{}, errors.New("unsupported retention action")
	}
	id := retentionJobDocumentID(decision.JobKey)
	now := time.Now().UTC()
	job := RetentionJob{ID: id, JobKey: decision.JobKey, ObjectID: objectID, Action: decision.Action, Policy: RetentionPolicyVersion, DueAt: decision.DueAt.UTC(), Status: "pending", CreatedAt: now, UpdatedAt: now}
	data := retentionJobData(job)
	_, err := r.db.CreateDocument(ctx, retentionJobsCollection, id, data, nil)
	if err == nil {
		return job, nil
	}
	var apiErr appwritedb.HTTPError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		return RetentionJob{}, fmt.Errorf("enqueue retention job: %w", err)
	}
	raw, readErr := r.db.GetDocument(ctx, retentionJobsCollection, id)
	if readErr != nil {
		return RetentionJob{}, fmt.Errorf("read idempotent retention job: %w", readErr)
	}
	existing, decodeErr := decodeRetentionJob(raw)
	if decodeErr != nil {
		return RetentionJob{}, decodeErr
	}
	if existing.JobKey != decision.JobKey || existing.ObjectID != objectID || existing.Action != decision.Action || existing.Policy != RetentionPolicyVersion {
		return RetentionJob{}, errors.New("retention job idempotency collision")
	}
	return existing, nil
}

func (r *AppwriteRetentionJobs) Claim(ctx context.Context, id, worker string, now time.Time, lease time.Duration) (RetentionJob, error) {
	if !validDocID(id) || !validDocID(worker) || lease < 30*time.Second || lease > 15*time.Minute {
		return RetentionJob{}, errors.New("invalid retention job claim")
	}
	tx, err := r.db.CreateTransaction(ctx, 120)
	if err != nil {
		return RetentionJob{}, err
	}
	rollback := func() { _ = r.db.UpdateTransaction(context.Background(), tx, false) }
	raw, err := r.db.GetDocumentInTransaction(ctx, retentionJobsCollection, id, tx)
	if err != nil {
		rollback()
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return RetentionJob{}, ErrRetentionJobNotFound
		}
		return RetentionJob{}, err
	}
	job, err := decodeRetentionJob(raw)
	if err != nil {
		rollback()
		return RetentionJob{}, err
	}
	if job.Policy != RetentionPolicyVersion {
		rollback()
		return RetentionJob{}, errors.New("retention job policy version is not supported by this worker")
	}
	if job.Status == "completed" {
		rollback()
		return RetentionJob{}, errors.New("retention job is already completed")
	}
	if job.Status == "running" && job.LeaseUntil.After(now.UTC()) {
		rollback()
		return RetentionJob{}, ErrRetentionJobClaimed
	}
	if job.Status != "pending" && job.Status != "retry" && job.Status != "running" {
		rollback()
		return RetentionJob{}, errors.New("retention job has an invalid state")
	}
	if job.DueAt.After(now.UTC()) {
		rollback()
		return RetentionJob{}, errors.New("retention job is not due")
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		rollback()
		return RetentionJob{}, fmt.Errorf("generate retention lease token: %w", err)
	}
	job.Status = "running"
	job.WorkerID = worker
	job.LeaseToken = hex.EncodeToString(tokenBytes)
	job.LeaseUntil = now.UTC().Add(lease)
	job.Attempts++
	job.UpdatedAt = now.UTC()
	job.LastError = ""
	if err = r.db.UpdateDocumentInTransaction(ctx, retentionJobsCollection, id, retentionJobData(job), tx); err != nil {
		rollback()
		return RetentionJob{}, err
	}
	if err = r.db.UpdateTransaction(ctx, tx, true); err != nil {
		return RetentionJob{}, err
	}
	return job, nil
}

func (r *AppwriteRetentionJobs) Renew(ctx context.Context, claimed RetentionJob, now time.Time, lease time.Duration) error {
	if claimed.ID == "" || claimed.LeaseToken == "" || lease < 30*time.Second || lease > 15*time.Minute {
		return ErrRetentionLeaseLost
	}
	tx, err := r.db.CreateTransaction(ctx, 120)
	if err != nil {
		return err
	}
	rollback := func() { _ = r.db.UpdateTransaction(context.Background(), tx, false) }
	raw, err := r.db.GetDocumentInTransaction(ctx, retentionJobsCollection, claimed.ID, tx)
	if err != nil {
		rollback()
		return err
	}
	current, err := decodeRetentionJob(raw)
	if err != nil {
		rollback()
		return err
	}
	if current.Status != "running" || current.LeaseToken != claimed.LeaseToken || current.WorkerID != claimed.WorkerID || !current.LeaseUntil.After(now.UTC()) {
		rollback()
		return ErrRetentionLeaseLost
	}
	until := now.UTC().Add(lease)
	if err = r.db.UpdateDocumentInTransaction(ctx, retentionJobsCollection, claimed.ID, map[string]any{"lease_until": until.Format(time.RFC3339Nano), "updated_at": now.UTC().Format(time.RFC3339Nano)}, tx); err != nil {
		rollback()
		return err
	}
	return r.db.UpdateTransaction(ctx, tx, true)
}

func (r *AppwriteRetentionJobs) Complete(ctx context.Context, claimed RetentionJob, now time.Time) error {
	return r.finish(ctx, claimed, now, map[string]any{"status": "completed", "completed_at": now.UTC().Format(time.RFC3339Nano), "updated_at": now.UTC().Format(time.RFC3339Nano), "lease_token": "", "lease_until": "", "worker_id": ""})
}
func (r *AppwriteRetentionJobs) Retry(ctx context.Context, claimed RetentionJob, cause error, now time.Time) error {
	message := "retry requested"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	return r.finish(ctx, claimed, now, map[string]any{"status": "retry", "last_error": message, "updated_at": now.UTC().Format(time.RFC3339Nano), "lease_token": "", "lease_until": "", "worker_id": ""})
}
func (r *AppwriteRetentionJobs) finish(ctx context.Context, claimed RetentionJob, now time.Time, fields map[string]any) error {
	if claimed.ID == "" || claimed.LeaseToken == "" {
		return ErrRetentionLeaseLost
	}
	tx, err := r.db.CreateTransaction(ctx, 120)
	if err != nil {
		return err
	}
	rollback := func() { _ = r.db.UpdateTransaction(context.Background(), tx, false) }
	raw, err := r.db.GetDocumentInTransaction(ctx, retentionJobsCollection, claimed.ID, tx)
	if err != nil {
		rollback()
		return err
	}
	current, err := decodeRetentionJob(raw)
	if err != nil {
		rollback()
		return err
	}
	if current.Status != "running" || current.LeaseToken != claimed.LeaseToken || current.WorkerID != claimed.WorkerID || !current.LeaseUntil.After(now.UTC()) {
		rollback()
		return ErrRetentionLeaseLost
	}
	if err = r.db.UpdateDocumentInTransaction(ctx, retentionJobsCollection, claimed.ID, fields, tx); err != nil {
		rollback()
		return err
	}
	return r.db.UpdateTransaction(ctx, tx, true)
}

func retentionJobData(j RetentionJob) map[string]any {
	return map[string]any{"job_key": j.JobKey, "object_id": j.ObjectID, "action": string(j.Action), "policy_version": j.Policy, "due_at": j.DueAt.Format(time.RFC3339Nano), "status": j.Status, "worker_id": j.WorkerID, "lease_token": j.LeaseToken, "lease_until": timeString(j.LeaseUntil), "attempts": j.Attempts, "last_error": j.LastError, "created_at": j.CreatedAt.Format(time.RFC3339Nano), "updated_at": j.UpdatedAt.Format(time.RFC3339Nano)}
}
func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func decodeRetentionJob(raw []byte) (RetentionJob, error) {
	var w struct {
		ID, JobKey, ObjectID, Action, Policy, DueAt, Status, WorkerID, LeaseToken, LeaseUntil, LastError, CreatedAt, UpdatedAt string
		Attempts                                                                                                               int `json:"attempts"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return RetentionJob{}, errors.New("invalid Appwrite retention job")
	}
	// Decode explicit wire names; Appwrite document fields are snake_case.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return RetentionJob{}, err
	}
	get := func(k string) string { var v string; _ = json.Unmarshal(m[k], &v); return v }
	var j RetentionJob
	j.ID = get("$id")
	j.JobKey = get("job_key")
	j.ObjectID = get("object_id")
	j.Action = RetentionAction(get("action"))
	j.Policy = get("policy_version")
	j.Status = get("status")
	j.WorkerID = get("worker_id")
	j.LeaseToken = get("lease_token")
	j.LastError = get("last_error")
	_ = json.Unmarshal(m["attempts"], &j.Attempts)
	var err error
	for _, v := range []struct {
		key string
		dst *time.Time
	}{{"due_at", &j.DueAt}, {"lease_until", &j.LeaseUntil}, {"created_at", &j.CreatedAt}, {"updated_at", &j.UpdatedAt}, {"completed_at", &j.CompletedAt}} {
		rawValue := get(v.key)
		if rawValue != "" {
			*v.dst, err = time.Parse(time.RFC3339Nano, rawValue)
			if err != nil {
				return RetentionJob{}, fmt.Errorf("invalid retention job %s", v.key)
			}
		}
	}
	if !validDocID(j.ID) || len(j.JobKey) != 64 || !validDocID(j.ObjectID) || j.Status == "" || j.DueAt.IsZero() || j.Policy == "" || !strings.Contains("|pending|running|retry|completed|", "|"+j.Status+"|") {
		return RetentionJob{}, errors.New("incomplete Appwrite retention job")
	}
	return j, nil
}
