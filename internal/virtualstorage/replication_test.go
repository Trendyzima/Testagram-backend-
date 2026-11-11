package virtualstorage

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type replicaTestObject struct {
	info ObjectInfo
	hash string
}

type replicaTestStore struct {
	objects map[string]replicaTestObject
	failCopy bool
}

func newReplicaTestStore() *replicaTestStore {
	return &replicaTestStore{objects: make(map[string]replicaTestObject)}
}

func replicaTestKey(bucket, key string) string { return bucket + "/" + key }

func (s *replicaTestStore) PresignPut(context.Context, string, string, string, int64, time.Duration) (string, error) {
	return "", errors.New("not used")
}
func (s *replicaTestStore) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "", errors.New("not used")
}
func (s *replicaTestStore) Head(_ context.Context, bucket, key string) (ObjectInfo, error) {
	obj, ok := s.objects[replicaTestKey(bucket, key)]
	if !ok { return ObjectInfo{}, ErrNotFound }
	return obj.info, nil
}
func (s *replicaTestStore) Delete(_ context.Context, bucket, key string) error {
	delete(s.objects, replicaTestKey(bucket, key))
	return nil
}
func (s *replicaTestStore) List(context.Context, string, string, string, int) ([]ObjectInfo, string, error) {
	return nil, "", nil
}
func (s *replicaTestStore) SHA256(_ context.Context, bucket, key string, _ int64) (string, error) {
	obj, ok := s.objects[replicaTestKey(bucket, key)]
	if !ok { return "", ErrNotFound }
	return obj.hash, nil
}
func (s *replicaTestStore) CopyFromProvider(_ context.Context, source Provider, sourceBucket, sourceKey, destinationBucket, destinationKey string, expectedBytes int64, contentType, expectedSHA256 string) error {
	if s.failCopy { return errors.New("simulated provider outage") }
	src, ok := source.(*replicaTestStore)
	if !ok { return errors.New("unexpected source type") }
	obj, ok := src.objects[replicaTestKey(sourceBucket, sourceKey)]
	if !ok { return ErrNotFound }
	if obj.info.Size != expectedBytes || obj.info.ContentType != contentType || obj.hash != expectedSHA256 {
		return ErrObjectMismatch
	}
	s.objects[replicaTestKey(destinationBucket, destinationKey)] = obj
	return nil
}

func TestReplicatorVerifiesAndWritesAllThreeStores(t *testing.T) {
	ctx := context.Background()
	primary := newReplicaTestStore()
	r2 := newReplicaTestStore()
	b2 := newReplicaTestStore()
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	key := tenantPrefix("user-1") + "users/user-1/photo.webp"
	primary.objects[replicaTestKey("vps-private", key)] = replicaTestObject{
		info: ObjectInfo{Key:key, Size:1234, ContentType:"image/webp"},
		hash:digest,
	}
	replicator, err := NewReplicator("instabox-vps", primary,
		map[string]string{"media-private":"vps-private", "media-public":"vps-public"},
		[]ReplicaTarget{
			{Name:"cloudflare-r2", Provider:r2, Buckets:map[string]string{"media-private":"r2-private", "media-public":"r2-public"}},
			{Name:"backblaze-b2", Provider:b2, Buckets:map[string]string{"media-private":"b2-private", "media-public":"b2-public"}},
		})
	if err != nil { t.Fatal(err) }
	results, err := replicator.Replicate(ctx, "user-1", "media-private", "users/user-1/photo.webp", 1234, "image/webp", digest)
	if err != nil { t.Fatal(err) }
	if len(results) != 3 { t.Fatalf("replica count=%d, want 3", len(results)) }
	for _, target := range []struct{ store *replicaTestStore; bucket string }{{r2,"r2-private"},{b2,"b2-private"}} {
		if _, ok := target.store.objects[replicaTestKey(target.bucket,key)]; !ok {
			t.Fatalf("replica missing from bucket %s", target.bucket)
		}
	}
}

func TestReplicatorDoesNotClaimSuccessWhenReplicaFails(t *testing.T) {
	primary := newReplicaTestStore()
	r2 := newReplicaTestStore()
	b2 := newReplicaTestStore()
	const digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	key := tenantPrefix("user-1") + "users/user-1/photo.webp"
	primary.objects[replicaTestKey("vps-private",key)] = replicaTestObject{
		info:ObjectInfo{Key:key,Size:99,ContentType:"image/webp"}, hash:digest,
	}
	b2.failCopy = true
	replicator, err := NewReplicator("instabox-vps", primary,
		map[string]string{"media-private":"vps-private","media-public":"vps-public"},
		[]ReplicaTarget{
			{Name:"cloudflare-r2",Provider:r2,Buckets:map[string]string{"media-private":"r2-private","media-public":"r2-public"}},
			{Name:"backblaze-b2",Provider:b2,Buckets:map[string]string{"media-private":"b2-private","media-public":"b2-public"}},
		})
	if err != nil { t.Fatal(err) }
	results, err := replicator.Replicate(context.Background(),"user-1","media-private","users/user-1/photo.webp",99,"image/webp",digest)
	if err == nil { t.Fatal("replication unexpectedly succeeded") }
	if len(results) != 2 { t.Fatalf("verified copies reported=%d, want primary plus R2",len(results)) }
	if _, ok := b2.objects[replicaTestKey("b2-private",key)]; ok {
		t.Fatal("failed B2 destination unexpectedly contains a verified replica")
	}
}

func TestReplicatorRejectsInvalidProviderTopology(t *testing.T) {
	primary := newReplicaTestStore()
	bad := newReplicaTestStore()
	_, err := NewReplicator("instabox-vps", primary,
		map[string]string{"media-private":"vps-private","media-public":"vps-public"},
		[]ReplicaTarget{{Name:"cloudflare-r2",Provider:bad,Buckets:map[string]string{"media-private":"r2-private","media-public":"r2-public"}}})
	if err == nil || !strings.Contains(err.Error(),"exactly two") {
		t.Fatalf("expected exactly-two-replicas validation, got %v",err)
	}
	_, err = NewReplicator("instabox-vps", primary,
		map[string]string{"media-private":"vps-private","media-public":"vps-public"},
		[]ReplicaTarget{
			{Name:"cloudflare-r2",Provider:bad,Buckets:map[string]string{"media-private":"r2-private","media-public":"r2-public"}},
			{Name:"cloudflare-r2",Provider:bad,Buckets:map[string]string{"media-private":"r2-private","media-public":"r2-public"}},
		})
	if err == nil || !strings.Contains(err.Error(),"duplicate") {
		t.Fatalf("expected duplicate provider validation, got %v",err)
	}
	_ = fmt.Sprint(err)
}
