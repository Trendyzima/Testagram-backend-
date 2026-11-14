package virtualstorage

import (
	"context"
	"errors"
	"strings"
	"time"
	"testing"
)

type replicaTestObject struct {
	info ObjectInfo
	hash string
}

type replicaTestStore struct {
	objects map[string]replicaTestObject
	failCopy bool
	copyCalls int
}

func newReplicaTestStore() *replicaTestStore {
	return &replicaTestStore{objects: make(map[string]replicaTestObject)}
}

func replicaTestKey(bucket, key string) string { return bucket + "/" + key }

func (s *replicaTestStore) PresignPut(context.Context, string, string, string, int64, time.Duration) (string, error) {
	return "", errors.New("not used")
}
func (s *replicaTestStore) PresignGet(_ context.Context, bucket, key string, _ time.Duration) (string, error) {
	if _, ok := s.objects[replicaTestKey(bucket, key)]; !ok {
		return "", ErrNotFound
	}
	return "https://replica.example/" + bucket + "/" + key, nil
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
	s.copyCalls++
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
}

func TestReplicatorDeletesPrimaryAndEveryReplica(t *testing.T) {
	primary:=newReplicaTestStore()
	r2:=newReplicaTestStore()
	b2:=newReplicaTestStore()
	key:="users/user-1/photo.webp"
	scoped:=tenantPrefix("user-1")+key
	for _,entry:=range []struct{store *replicaTestStore;bucket string}{{primary,"vps-private"},{r2,"r2-private"},{b2,"b2-private"}} {
		entry.store.objects[replicaTestKey(entry.bucket,scoped)]=replicaTestObject{
			info:ObjectInfo{Key:scoped,Size:10,ContentType:"image/webp"},
			hash:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}
	}
	replicator,err:=NewReplicator("instabox-vps",primary,
		map[string]string{"media-private":"vps-private","media-public":"vps-public"},
		[]ReplicaTarget{
			{Name:"cloudflare-r2",Provider:r2,Buckets:map[string]string{"media-private":"r2-private","media-public":"r2-public"}},
			{Name:"backblaze-b2",Provider:b2,Buckets:map[string]string{"media-private":"b2-private","media-public":"b2-public"}},
		})
	if err!=nil { t.Fatal(err) }
	if err:=replicator.Delete(context.Background(),"user-1","media-private",key);err!=nil { t.Fatal(err) }
	for _,entry:=range []struct{store *replicaTestStore;bucket string}{{primary,"vps-private"},{r2,"r2-private"},{b2,"b2-private"}} {
		if _,ok:=entry.store.objects[replicaTestKey(entry.bucket,scoped)];ok {
			t.Fatalf("object remains in %s after replica deletion",entry.bucket)
		}
	}
}

func TestReplicatorReconcilesOnlyMissingOrDriftedCopies(t *testing.T) {
	primary:=newReplicaTestStore()
	r2:=newReplicaTestStore()
	b2:=newReplicaTestStore()
	const digest="cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	key:="users/user-1/photo.webp"
	scoped:=tenantPrefix("user-1")+key
	for _,entry:=range []struct{store *replicaTestStore;bucket string;hash string}{{primary,"vps-private",digest},{r2,"r2-private",digest},{b2,"b2-private","dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}} {
		entry.store.objects[replicaTestKey(entry.bucket,scoped)]=replicaTestObject{
			info:ObjectInfo{Key:scoped,Size:321,ContentType:"image/webp"},hash:entry.hash,
		}
	}
	replicator,err:=NewReplicator("instabox-vps",primary,
		map[string]string{"media-private":"vps-private","media-public":"vps-public"},
		[]ReplicaTarget{
			{Name:"cloudflare-r2",Provider:r2,Buckets:map[string]string{"media-private":"r2-private","media-public":"r2-public"}},
			{Name:"backblaze-b2",Provider:b2,Buckets:map[string]string{"media-private":"b2-private","media-public":"b2-public"}},
		})
	if err!=nil { t.Fatal(err) }
	results,err:=replicator.Replicate(context.Background(),"user-1","media-private",key,321,"image/webp",digest)
	if err!=nil { t.Fatal(err) }
	if len(results)!=3 { t.Fatalf("verified placements=%d, want 3",len(results)) }
	if r2.copyCalls!=0 { t.Fatalf("healthy R2 object was recopied %d times",r2.copyCalls) }
	if b2.copyCalls!=1 { t.Fatalf("drifted B2 object repair copies=%d, want 1",b2.copyCalls) }
	if got:=b2.objects[replicaTestKey("b2-private",scoped)].hash;got!=digest {
		t.Fatalf("B2 checksum after repair=%s, want %s",got,digest)
	}
}


func TestReplicatorSupportsPrivateIdentityEvidenceNamespace(t *testing.T) {
	primary := newReplicaTestStore()
	r2 := newReplicaTestStore()
	b2 := newReplicaTestStore()
	const digest = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	key := "identity/session-1/evidence.pdf"
	scopedKey := tenantPrefix("application-1") + key
	payloadInfo := ObjectInfo{Key: scopedKey, Size: 64, ContentType: "application/pdf"}
	primary.objects[replicaTestKey("vps-identity", scopedKey)] = replicaTestObject{info: payloadInfo, hash: digest}

	replicator, err := NewReplicator("instabox-vps", primary,
		map[string]string{
			"media-private": "vps-private",
			"media-public": "vps-public",
			"identity-evidence-private": "vps-identity",
		},
		[]ReplicaTarget{
			{Name: "cloudflare-r2", Provider: r2, Buckets: map[string]string{
				"media-private": "r2-private", "media-public": "r2-public",
				"identity-evidence-private": "r2-identity",
			}},
			{Name: "backblaze-b2", Provider: b2, Buckets: map[string]string{
				"media-private": "b2-private", "media-public": "b2-public",
				"identity-evidence-private": "b2-identity",
			}},
		})
	if err != nil {
		t.Fatal(err)
	}
	results, err := replicator.Replicate(context.Background(), "application-1", "identity-evidence-private", key, 64, "application/pdf", digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("verified placements=%d, want 3", len(results))
	}
	if _, ok := r2.objects[replicaTestKey("r2-identity", scopedKey)]; !ok {
		t.Fatal("identity evidence was not replicated to R2")
	}
	if _, ok := b2.objects[replicaTestKey("b2-identity", scopedKey)]; !ok {
		t.Fatal("identity evidence was not replicated to B2")
	}
	if err := replicator.Delete(context.Background(), "application-1", "identity-evidence-private", key); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		store *replicaTestStore
		bucket string
	}{{primary, "vps-identity"}, {r2, "r2-identity"}, {b2, "b2-identity"}} {
		if _, ok := item.store.objects[replicaTestKey(item.bucket, scopedKey)]; ok {
			t.Fatalf("identity evidence remains in %s after deletion", item.bucket)
		}
	}
}

func TestReplicatorRestoresMissingPrimaryFromChecksumVerifiedReplica(t *testing.T) {
	primary := newReplicaTestStore()
	r2 := newReplicaTestStore()
	b2 := newReplicaTestStore()
	const digest = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	tenant := "application-restore"
	key := "identity/evidence.pdf"
	scoped := tenantPrefix(tenant) + key
	info := ObjectInfo{Key: scoped, Size: 64, ContentType: "application/pdf"}
	r2.objects[replicaTestKey("r2-identity", scoped)] = replicaTestObject{info: info, hash: digest}
	b2.objects[replicaTestKey("b2-identity", scoped)] = replicaTestObject{info: info, hash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}

	replicator, err := NewReplicator("instabox-vps", primary,
		map[string]string{
			"media-private": "vps-private", "media-public": "vps-public",
			"identity-evidence-private": "vps-identity",
		},
		[]ReplicaTarget{
			{Name: "cloudflare-r2", Provider: r2, Buckets: map[string]string{
				"media-private": "r2-private", "media-public": "r2-public", "identity-evidence-private": "r2-identity",
			}},
			{Name: "backblaze-b2", Provider: b2, Buckets: map[string]string{
				"media-private": "b2-private", "media-public": "b2-public", "identity-evidence-private": "b2-identity",
			}},
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := replicator.RestorePrimary(context.Background(), tenant, "identity-evidence-private", key, 64, "application/pdf", digest); err != nil {
		t.Fatal(err)
	}
	restored, err := primary.SHA256(context.Background(), "vps-identity", scoped, 64)
	if err != nil || restored != digest {
		t.Fatalf("restored primary digest=%q err=%v, want %s", restored, err, digest)
	}
}

func TestReplicatorReturnsOnlyChecksumVerifiedPrivateReplicaURLs(t *testing.T) {
	primary := newReplicaTestStore()
	r2 := newReplicaTestStore()
	b2 := newReplicaTestStore()
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tenant := "application-review"
	key := "identity/evidence.pdf"
	scoped := tenantPrefix(tenant) + key
	info := ObjectInfo{Key: scoped, Size: 64, ContentType: "application/pdf"}
	r2.objects[replicaTestKey("r2-identity", scoped)] = replicaTestObject{info: info, hash: digest}
	b2.objects[replicaTestKey("b2-identity", scoped)] = replicaTestObject{info: info, hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}

	replicator, err := NewReplicator("instabox-vps", primary,
		map[string]string{
			"media-private": "vps-private", "media-public": "vps-public",
			"identity-evidence-private": "vps-identity",
		},
		[]ReplicaTarget{
			{Name: "cloudflare-r2", Provider: r2, Buckets: map[string]string{
				"media-private": "r2-private", "media-public": "r2-public", "identity-evidence-private": "r2-identity",
			}},
			{Name: "backblaze-b2", Provider: b2, Buckets: map[string]string{
				"media-private": "b2-private", "media-public": "b2-public", "identity-evidence-private": "b2-identity",
			}},
		})
	if err != nil {
		t.Fatal(err)
	}
	urls, err := replicator.ReplicaDownloadURLs(context.Background(), tenant, "identity-evidence-private", key, 64, "application/pdf", digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0].Provider != "cloudflare-r2" {
		t.Fatalf("verified replica downloads=%+v, want only checksum-valid R2", urls)
	}
}
