package freshstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

var ErrLiveMediaReference = errors.New("media still has a live post or story reference")
var ErrUnmanagedReplica = errors.New("media has a replica on an unsupported provider; refusing quota release")

type MediaReplica struct {
	ID, ObjectID, Provider, Bucket, Key, SHA256, State string
	Bytes                                              int64
}
type RetentionLifecycleStore interface {
	RevokeCanonicalMedia(context.Context, MediaRecord) error
	GetMediaRecord(context.Context, string) (MediaRecord, error)
	BeginMediaPurge(context.Context, MediaRecord) error
	ExpireDueStoriesAndCheckReferences(context.Context, string, time.Time) (bool, error)
	HasLiveMediaReferences(context.Context, string) (bool, error)
	ListMediaReplicas(context.Context, string) ([]MediaReplica, error)
	MarkMediaReplicaDeleted(context.Context, MediaReplica, time.Time) error
	FinalizeMediaQuotaRelease(context.Context, MediaRecord, string, time.Time) error
}

type ReplicaObjectStore interface {
	Head(context.Context, string, string) (virtualstorage.ObjectInfo, error)
	Delete(context.Context, string, string) error
}

type ReplicaProviders struct{ B2, R2, Archive ReplicaObjectStore }

func (p ReplicaProviders) get(name string) ReplicaObjectStore {
	switch name {
	case "b2":
		return p.B2
	case "r2":
		return p.R2
	case "archive":
		return p.Archive
	default:
		return nil
	}
}

// StoryExpiryWorker expires story records first, then deletes bytes only if no
// live post/story association remains and every known replica is verifiably gone.
type StoryExpiryWorker struct {
	store     RetentionLifecycleStore
	providers ReplicaProviders
}

func NewStoryExpiryWorker(store RetentionLifecycleStore, providers ReplicaProviders) (*StoryExpiryWorker, error) {
	if store == nil || providers.B2 == nil || providers.R2 == nil {
		return nil, errors.New("story expiry requires Appwrite ledger, B2 and R2")
	}
	return &StoryExpiryWorker{store: store, providers: providers}, nil
}
func (w *StoryExpiryWorker) Run(ctx context.Context, job RetentionJob, now time.Time, dryRun bool) error {
	if job.Action != RetentionExpireStory || job.ObjectID == "" {
		return errors.New("story worker received a non-story job")
	}
	media, err := w.store.GetMediaRecord(ctx, job.ObjectID)
	if err != nil {
		return err
	}
	if media.Status == "expired" {
		return nil
	}
	if media.Scope != "story_media" || media.RetentionClass != "story" {
		return errors.New("story job does not match story media metadata")
	}
	if dryRun {
		return nil
	}
	live, err := w.store.ExpireDueStoriesAndCheckReferences(ctx, job.ObjectID, now.UTC())
	if err != nil {
		return err
	}
	if live {
		return nil
	}
	return purgeReplicasAndRelease(ctx, w.store, w.providers, media, "expired", now.UTC())
}

// UserDeletionPurgeWorker honors the deletion event's own recovery clock. It
// refuses to reclaim quota until all ledgered replicas, including archive, are absent.
type UserDeletionPurgeWorker struct {
	store          RetentionLifecycleStore
	providers      ReplicaProviders
	recoveryWindow time.Duration
}

func NewUserDeletionPurgeWorker(store RetentionLifecycleStore, providers ReplicaProviders, recoveryWindow time.Duration) (*UserDeletionPurgeWorker, error) {
	if store == nil || providers.B2 == nil || providers.R2 == nil || recoveryWindow < 24*time.Hour {
		return nil, errors.New("user deletion purge requires Appwrite ledger, B2/R2 and a recovery window of at least 24 hours")
	}
	return &UserDeletionPurgeWorker{store: store, providers: providers, recoveryWindow: recoveryWindow}, nil
}
func (w *UserDeletionPurgeWorker) Run(ctx context.Context, job RetentionJob, now time.Time, dryRun bool) error {
	if job.Action != RetentionPurgeDeleted || job.ObjectID == "" {
		return errors.New("purge worker received a non-deletion job")
	}
	media, err := w.store.GetMediaRecord(ctx, job.ObjectID)
	if err != nil {
		return err
	}
	if media.Status == "purged" {
		return nil
	}
	if media.Status != "user_deleted" || media.RetentionClass != "user_deleted" || (media.Scope != "post_media" && media.Scope != "story_media") || media.DeletedAt.IsZero() {
		return errors.New("purge job does not match a recoverable user-deletion record")
	}
	if now.UTC().Before(media.DeletedAt.Add(w.recoveryWindow)) {
		return errors.New("user-deleted media is still inside its recovery window")
	}
	if dryRun {
		return nil
	}
	live, err := w.store.HasLiveMediaReferences(ctx, job.ObjectID)
	if err != nil {
		return err
	}
	if live {
		return ErrLiveMediaReference
	}
	return purgeReplicasAndRelease(ctx, w.store, w.providers, media, "purged", now.UTC())
}

func purgeReplicasAndRelease(ctx context.Context, store RetentionLifecycleStore, providers ReplicaProviders, media MediaRecord, terminal string, now time.Time) error {
	if err := store.BeginMediaPurge(ctx, media); err != nil {
		return fmt.Errorf("freeze media lifecycle before replica deletion: %w", err)
	}
	if err := store.RevokeCanonicalMedia(ctx, media); err != nil { return fmt.Errorf("revoke Backend CDN authorization before replica deletion: %w", err) }
	replicas, err := store.ListMediaReplicas(ctx, media.ObjectID)
	if err != nil {
		return err
	}
	if len(replicas) == 0 {
		return errors.New("media has no replica ledger; refusing quota release")
	}
	for _, replica := range replicas {
		provider := providers.get(replica.Provider)
		if provider == nil {
			return fmt.Errorf("%w: %s", ErrUnmanagedReplica, replica.Provider)
		}
		if replica.ObjectID != media.ObjectID || replica.Bytes != media.Bytes || replica.SHA256 != media.SHA256 || replica.Bucket == "" || replica.Key == "" {
			return errors.New("replica ledger metadata does not match canonical media object")
		}
		headErr := error(nil)
		_, headErr = provider.Head(ctx, replica.Bucket, replica.Key)
		if headErr == nil {
			if err = provider.Delete(ctx, replica.Bucket, replica.Key); err != nil {
				return fmt.Errorf("delete %s replica: %w", replica.Provider, err)
			}
			_, headErr = provider.Head(ctx, replica.Bucket, replica.Key)
		}
		if !errors.Is(headErr, virtualstorage.ErrNotFound) {
			if headErr == nil {
				return fmt.Errorf("%s replica still exists after delete", replica.Provider)
			}
			return fmt.Errorf("verify %s replica absence: %w", replica.Provider, headErr)
		}
		if replica.State != "deleted" {
			if err = store.MarkMediaReplicaDeleted(ctx, replica, now); err != nil {
				return fmt.Errorf("record %s replica absence: %w", replica.Provider, err)
			}
		}
	}
	return store.FinalizeMediaQuotaRelease(ctx, media, terminal, now)
}

// Appwrite lifecycle ledger implementation. A 100-document cap is deliberate:
// if a reference or replica scan fills the cap, completeness is unknown and the
// worker fails closed rather than silently missing a shared copy.
func (s *AppwriteStore) RevokeCanonicalMedia(ctx context.Context, media MediaRecord) error {
	if s.backendMedia == nil || !freshMediaObjectKeyPattern.MatchString(media.ObjectKey) { return nil }
	if err := s.backendMedia.Revoke(ctx, media.OwnerID, media.ObjectKey); err != nil {
		return fmt.Errorf("revoke canonical Backend media record: %w", err)
	}
	return nil
}

func (s *AppwriteStore) BeginMediaPurge(ctx context.Context, media MediaRecord) error {
	tx, err := s.db.CreateTransaction(ctx, 120)
	if err != nil {
		return err
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	raw, err := s.db.GetDocumentInTransaction(ctx, mediaCollection, media.ObjectID, tx)
	if err != nil {
		rollback()
		return err
	}
	var current struct {
		OwnerID        string `json:"owner_id"`
		Status         string `json:"status"`
		LifecycleState string `json:"lifecycle_state"`
	}
	if err = json.Unmarshal(raw, &current); err != nil {
		rollback()
		return err
	}
	if current.OwnerID != media.OwnerID || current.Status != media.Status {
		rollback()
		return errors.New("media changed before purge claim")
	}
	if current.LifecycleState == "purging" {
		rollback()
		return nil
	}
	if current.LifecycleState != "" && current.LifecycleState != "active" && current.LifecycleState != "recoverable" && current.LifecycleState != "archived" {
		rollback()
		return errors.New("media lifecycle state cannot enter purge")
	}
	if err = s.db.UpdateDocumentInTransaction(ctx, mediaCollection, media.ObjectID, map[string]any{"lifecycle_state": "purging", "purge_started_at": time.Now().UTC().Format(time.RFC3339Nano)}, tx); err != nil {
		rollback()
		return err
	}
	return s.db.UpdateTransaction(ctx, tx, true)
}

func (s *AppwriteStore) ListMediaReplicas(ctx context.Context, objectID string) ([]MediaReplica, error) {
	raw, err := s.db.ListDocuments(ctx, replicaCollection, string(appwriteEqualQuery("object_id", objectID)), 100)
	if err != nil {
		return nil, err
	}
	if len(raw) >= 100 {
		return nil, errors.New("replica inventory exceeds safe scan bound")
	}
	out := make([]MediaReplica, 0, len(raw))
	for _, item := range raw {
		var w struct {
			ID       string `json:"$id"`
			ObjectID string `json:"object_id"`
			Provider string `json:"provider"`
			Bucket   string `json:"bucket"`
			Key      string `json:"object_key"`
			Bytes    string `json:"bytes_decimal"`
			SHA256   string `json:"sha256"`
			State    string `json:"state"`
		}
		if err = json.Unmarshal(item, &w); err != nil {
			return nil, err
		}
		n, e := strconv.ParseInt(w.Bytes, 10, 64)
		if e != nil || n < 1 || w.ID == "" || w.ObjectID != objectID || w.Provider == "" || w.Bucket == "" || w.Key == "" || !validSHA256(w.SHA256) {
			return nil, errors.New("invalid replica inventory document")
		}
		out = append(out, MediaReplica{ID: w.ID, ObjectID: w.ObjectID, Provider: w.Provider, Bucket: w.Bucket, Key: w.Key, Bytes: n, SHA256: w.SHA256, State: w.State})
	}
	return out, nil
}
func (s *AppwriteStore) MarkMediaReplicaDeleted(ctx context.Context, r MediaReplica, now time.Time) error {
	if !validDocID(r.ID) {
		return errors.New("invalid replica document ID")
	}
	_, err := s.db.UpdateDocument(ctx, replicaCollection, r.ID, map[string]any{"state": "deleted", "deleted_at": now.UTC().Format(time.RFC3339Nano)})
	return err
}
func (s *AppwriteStore) HasLiveMediaReferences(ctx context.Context, objectID string) (bool, error) {
	for _, collection := range []string{"post_media", "story_media"} {
		raw, err := s.db.ListDocuments(ctx, collection, string(appwriteEqualQuery("object_id", objectID)), 100)
		if err != nil {
			return false, err
		}
		if len(raw) >= 100 {
			return false, errors.New("media reference inventory exceeds safe scan bound")
		}
		if len(raw) > 0 {
			return true, nil
		}
	}
	return false, nil
}
func (s *AppwriteStore) ExpireDueStoriesAndCheckReferences(ctx context.Context, objectID string, now time.Time) (bool, error) {
	links, err := s.db.ListDocuments(ctx, "story_media", string(appwriteEqualQuery("object_id", objectID)), 100)
	if err != nil {
		return false, err
	}
	if len(links) >= 100 {
		return false, errors.New("story reference inventory exceeds safe scan bound")
	}
	live := false
	for _, link := range links {
		var item struct {
			StoryID string `json:"story_id"`
		}
		if err = json.Unmarshal(link, &item); err != nil || !validDocID(item.StoryID) {
			return false, errors.New("invalid story-media reference")
		}
		raw, e := s.db.GetDocument(ctx, "stories", item.StoryID)
		if e != nil {
			return false, e
		}
		var story struct {
			Status    string `json:"status"`
			ExpiresAt string `json:"expires_at"`
		}
		if e = json.Unmarshal(raw, &story); e != nil {
			return false, e
		}
		expires, e := time.Parse(time.RFC3339Nano, story.ExpiresAt)
		if e != nil {
			return false, errors.New("invalid story expiry timestamp")
		}
		if story.Status == "active" && !expires.After(now) {
			_, e = s.db.UpdateDocument(ctx, "stories", item.StoryID, map[string]any{"status": "expired", "expired_at": now.UTC().Format(time.RFC3339Nano)})
			if e != nil {
				return false, e
			}
		} else if story.Status == "active" {
			live = true
		} else if story.Status != "expired" && story.Status != "deleted" {
			return false, errors.New("story has an unknown lifecycle state")
		}
	}
	posts, e := s.db.ListDocuments(ctx, "post_media", string(appwriteEqualQuery("object_id", objectID)), 100)
	if e != nil {
		return false, e
	}
	if len(posts) >= 100 {
		return false, errors.New("post reference inventory exceeds safe scan bound")
	}
	if len(posts) > 0 {
		live = true
	}
	return live, nil
}
func (s *AppwriteStore) FinalizeMediaQuotaRelease(ctx context.Context, media MediaRecord, terminal string, now time.Time) error {
	if terminal != "expired" && terminal != "purged" {
		return errors.New("invalid terminal media state")
	}
	if media.Status == terminal {
		return nil
	}
	replicas, err := s.ListMediaReplicas(ctx, media.ObjectID)
	if err != nil {
		return err
	}
	if len(replicas) == 0 {
		return errors.New("cannot release quota without a complete replica inventory")
	}
	for _, replica := range replicas {
		if replica.State != "deleted" {
			return fmt.Errorf("replica %s is not confirmed deleted", replica.ID)
		}
	}
	userQuota, err := s.readQuota(ctx, media.OwnerID)
	if err != nil {
		return err
	}
	platformQuota, err := s.readQuota(ctx, platformQuotaDocument)
	if err != nil {
		return err
	}
	units := reserveKiB(media.Bytes)
	tx, err := s.db.CreateTransaction(ctx, 120)
	if err != nil {
		return err
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	raw, err := s.db.GetDocumentInTransaction(ctx, mediaCollection, media.ObjectID, tx)
	if err != nil {
		rollback()
		return err
	}
	var current struct {
		OwnerID        string `json:"owner_id"`
		Status         string `json:"status"`
		LifecycleState string `json:"lifecycle_state"`
		Bytes          string `json:"bytes_decimal"`
	}
	if err = json.Unmarshal(raw, &current); err != nil {
		rollback()
		return err
	}
	if current.Status == terminal {
		rollback()
		return nil
	}
	if current.OwnerID != media.OwnerID || current.Status != media.Status || current.LifecycleState != "purging" || current.Bytes != strconv.FormatInt(media.Bytes, 10) {
		rollback()
		return errors.New("media changed before quota release")
	}
	for _, replica := range replicas {
		replicaRaw, readErr := s.db.GetDocumentInTransaction(ctx, replicaCollection, replica.ID, tx)
		if readErr != nil {
			rollback()
			return readErr
		}
		var ledger struct {
			State    string `json:"state"`
			ObjectID string `json:"object_id"`
			SHA256   string `json:"sha256"`
		}
		if readErr = json.Unmarshal(replicaRaw, &ledger); readErr != nil {
			rollback()
			return readErr
		}
		if ledger.State != "deleted" || ledger.ObjectID != media.ObjectID || ledger.SHA256 != media.SHA256 {
			rollback()
			return errors.New("replica ledger changed before quota release")
		}
	}
	for _, q := range []struct {
		id string
		v  quota
	}{{platformQuotaDocument, platformQuota}, {media.OwnerID, userQuota}} {
		if err = s.db.DecrementDocumentAttribute(ctx, quotaCollection, q.id, "used_kib", units, 0, tx); err != nil {
			rollback()
			return err
		}
	}
	if err = s.db.UpdateDocumentInTransaction(ctx, mediaCollection, media.ObjectID, map[string]any{"status": terminal, "lifecycle_state": terminal, "purged_at": now.UTC().Format(time.RFC3339Nano)}, tx); err != nil {
		rollback()
		return err
	}
	return s.db.UpdateTransaction(ctx, tx, true)
}
