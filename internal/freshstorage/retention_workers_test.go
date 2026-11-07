package freshstorage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

type replicaFake struct {
	present   bool
	deletes   int
	deleteErr error
}

func (f *replicaFake) Head(context.Context, string, string) (virtualstorage.ObjectInfo, error) {
	if !f.present {
		return virtualstorage.ObjectInfo{}, virtualstorage.ErrNotFound
	}
	return virtualstorage.ObjectInfo{Size: 10}, nil
}
func (f *replicaFake) Delete(context.Context, string, string) error {
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.present = false
	return nil
}

type lifecycleStoreFake struct {
	media        MediaRecord
	replicas     []MediaReplica
	live         bool
	expiredLive  bool
	expireCalls  int
	releaseCalls int
	marked       []string
	err          error
}

func (f *lifecycleStoreFake) GetMediaRecord(context.Context, string) (MediaRecord, error) {
	return f.media, f.err
}
func (f *lifecycleStoreFake) BeginMediaPurge(context.Context, MediaRecord) error { return f.err }
func (f *lifecycleStoreFake) ExpireDueStoriesAndCheckReferences(context.Context, string, time.Time) (bool, error) {
	f.expireCalls++
	return f.expiredLive, f.err
}
func (f *lifecycleStoreFake) HasLiveMediaReferences(context.Context, string) (bool, error) {
	return f.live, f.err
}
func (f *lifecycleStoreFake) ListMediaReplicas(context.Context, string) ([]MediaReplica, error) {
	return f.replicas, f.err
}
func (f *lifecycleStoreFake) MarkMediaReplicaDeleted(_ context.Context, r MediaReplica, _ time.Time) error {
	f.marked = append(f.marked, r.Provider)
	return f.err
}
func (f *lifecycleStoreFake) FinalizeMediaQuotaRelease(_ context.Context, _ MediaRecord, state string, _ time.Time) error {
	f.releaseCalls++
	if state == "" {
		return errors.New("missing terminal state")
	}
	return f.err
}

func testMedia(status, class, scope string) MediaRecord {
	return MediaRecord{ObjectID: "med_1", OwnerID: "user_1", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 10, ContentType: "image/jpeg", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: status, RetentionClass: class, Scope: scope, DeletedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
}
func testReplicas() []MediaReplica {
	return []MediaReplica{{ID: "b2_med_1", ObjectID: "med_1", Provider: "b2", Bucket: "active-b2", Key: "key", Bytes: 10, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "verified"}, {ID: "r2_med_1", ObjectID: "med_1", Provider: "r2", Bucket: "active-r2", Key: "key", Bytes: 10, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "verified"}}
}

func TestStoryExpiryMarksStoryButRetainsSharedMedia(t *testing.T) {
	store := &lifecycleStoreFake{media: testMedia("ready", "story", "story_media"), expiredLive: true, replicas: testReplicas()}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, err := NewStoryExpiryWorker(store, ReplicaProviders{B2: b2, R2: r2})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), RetentionJob{Action: RetentionExpireStory, ObjectID: "med_1"}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if store.expireCalls != 1 || store.releaseCalls != 0 || b2.deletes != 0 || r2.deletes != 0 {
		t.Fatalf("shared media was reclaimed: %#v %#v %#v", store, b2, r2)
	}
}

func TestUserDeletionPurgeWaitsForRecoveryDeadline(t *testing.T) {
	store := &lifecycleStoreFake{media: testMedia("user_deleted", "user_deleted", "post_media"), replicas: testReplicas()}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, _ := NewUserDeletionPurgeWorker(store, ReplicaProviders{B2: b2, R2: r2}, 30*24*time.Hour)
	err := worker.Run(context.Background(), RetentionJob{Action: RetentionPurgeDeleted, ObjectID: "med_1"}, store.media.DeletedAt.Add(29*24*time.Hour), false)
	if err == nil || b2.deletes != 0 || r2.deletes != 0 || store.releaseCalls != 0 {
		t.Fatalf("recovery window was bypassed: err=%v", err)
	}
}

func TestUserDeletionPurgeVerifiesAllReplicasBeforeQuotaRelease(t *testing.T) {
	media := testMedia("user_deleted", "user_deleted", "post_media")
	store := &lifecycleStoreFake{media: media, replicas: testReplicas()}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, _ := NewUserDeletionPurgeWorker(store, ReplicaProviders{B2: b2, R2: r2}, 30*24*time.Hour)
	if err := worker.Run(context.Background(), RetentionJob{Action: RetentionPurgeDeleted, ObjectID: "med_1"}, media.DeletedAt.Add(30*24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if b2.deletes != 1 || r2.deletes != 1 || store.releaseCalls != 1 || len(store.marked) != 2 {
		t.Fatalf("replicas/quota not reconciled: b2=%d r2=%d release=%d marked=%v", b2.deletes, r2.deletes, store.releaseCalls, store.marked)
	}
}

func TestUserDeletionPurgeFailsClosedForNodeReplica(t *testing.T) {
	media := testMedia("user_deleted", "user_deleted", "post_media")
	replicas := testReplicas()
	replicas = append(replicas, MediaReplica{ID: "node_1", ObjectID: "med_1", Provider: "node", Bucket: "node", Key: "key", Bytes: 10, SHA256: media.SHA256, State: "verified"})
	store := &lifecycleStoreFake{media: media, replicas: replicas}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, _ := NewUserDeletionPurgeWorker(store, ReplicaProviders{B2: b2, R2: r2}, 30*24*time.Hour)
	err := worker.Run(context.Background(), RetentionJob{Action: RetentionPurgeDeleted, ObjectID: "med_1"}, media.DeletedAt.Add(31*24*time.Hour), false)
	if !errors.Is(err, ErrUnmanagedReplica) || store.releaseCalls != 0 {
		t.Fatalf("unsupported node copy did not block quota release: %v", err)
	}
}

func TestUserDeletionPurgeDoesNotReleaseQuotaWhileReferenced(t *testing.T) {
	media := testMedia("user_deleted", "user_deleted", "post_media")
	store := &lifecycleStoreFake{media: media, replicas: testReplicas(), live: true}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, _ := NewUserDeletionPurgeWorker(store, ReplicaProviders{B2: b2, R2: r2}, 30*24*time.Hour)
	err := worker.Run(context.Background(), RetentionJob{Action: RetentionPurgeDeleted, ObjectID: "med_1"}, media.DeletedAt.Add(31*24*time.Hour), false)
	if !errors.Is(err, ErrLiveMediaReference) || store.releaseCalls != 0 {
		t.Fatalf("live reference did not block purge: %v", err)
	}
}

func TestStoryExpiryDeletesUnsharedMediaAfterExpiryAndVerifiesCopies(t *testing.T) {
	store := &lifecycleStoreFake{media: testMedia("ready", "story", "story_media"), replicas: testReplicas()}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, err := NewStoryExpiryWorker(store, ReplicaProviders{B2: b2, R2: r2})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Run(context.Background(), RetentionJob{Action: RetentionExpireStory, ObjectID: "med_1"}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if store.expireCalls != 1 || store.releaseCalls != 1 || b2.deletes != 1 || r2.deletes != 1 {
		t.Fatalf("story media not safely expired: store=%#v b2=%#v r2=%#v", store, b2, r2)
	}
}

func TestStoryExpiryDryRunDoesNotMutateStoriesOrReplicas(t *testing.T) {
	store := &lifecycleStoreFake{media: testMedia("ready", "story", "story_media"), replicas: testReplicas()}
	b2, r2 := &replicaFake{present: true}, &replicaFake{present: true}
	worker, _ := NewStoryExpiryWorker(store, ReplicaProviders{B2: b2, R2: r2})
	if err := worker.Run(context.Background(), RetentionJob{Action: RetentionExpireStory, ObjectID: "med_1"}, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if store.expireCalls != 0 || store.releaseCalls != 0 || b2.deletes != 0 || r2.deletes != 0 {
		t.Fatal("dry-run mutated story or media state")
	}
}
