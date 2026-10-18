package virtualstorage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProvider struct {
	bucket, key, contentType string
	size                     int64
	deleted                  bool
	headInfo                 ObjectInfo
	headErr                  error
	listItems                []ObjectInfo
}

func (f *fakeProvider) PresignPut(_ context.Context, bucket, key, contentType string, size int64, _ time.Duration) (string, error) {
	f.bucket, f.key, f.contentType, f.size = bucket, key, contentType, size
	return "https://storage.example/signed-put", nil
}
func (f *fakeProvider) PresignGet(_ context.Context, bucket, key string, _ time.Duration) (string, error) {
	f.bucket, f.key = bucket, key
	return "https://storage.example/signed-get", nil
}
func (f *fakeProvider) Head(_ context.Context, bucket, key string) (ObjectInfo, error) {
	f.bucket, f.key = bucket, key
	if f.headErr != nil {
		return ObjectInfo{}, f.headErr
	}
	if f.headInfo.Key == "" {
		return ObjectInfo{}, ErrNotFound
	}
	return f.headInfo, nil
}
func (f *fakeProvider) Delete(_ context.Context, bucket, key string) error {
	f.bucket, f.key, f.deleted = bucket, key, true
	return nil
}
func (f *fakeProvider) List(_ context.Context, bucket, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	f.bucket, f.key = bucket, prefix
	return f.listItems, "next-cursor", nil
}

func testManager(t *testing.T, provider Provider) *Manager {
	t.Helper()
	m, err := New(provider, []Namespace{{
		Name: "post-media", PhysicalBucket: "testagram-private-media", MaxObjectBytes: 1024,
		AllowedTypes: map[string]struct{}{"image/jpeg": {}},
		UploadTTL: 10 * time.Minute, DownloadTTL: 5 * time.Minute,
	}})
	if err != nil { t.Fatal(err) }
	return m
}

func TestUploadURLScopesTenantAndEnforcesContentType(t *testing.T) {
	p := &fakeProvider{}
	m := testManager(t, p)
	url, err := m.UploadURL(context.Background(), "user-123", "post-media", "post/image.jpg", "IMAGE/JPEG", 100)
	if err != nil { t.Fatal(err) }
	if url == "" || p.bucket != "testagram-private-media" || p.key != "tenants/user-123/post/image.jpg" || p.contentType != "image/jpeg" {
		t.Fatalf("unexpected provider call: url=%q provider=%+v", url, p)
	}
	if _, err := m.UploadURL(context.Background(), "user-123", "post-media", "image.jpg", "text/html", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("content type error = %v, want ErrForbidden", err)
	}
	if _, err := m.UploadURL(context.Background(), "user-123", "post-media", "image.jpg", "image/jpeg", 2048); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size error = %v, want ErrTooLarge", err)
	}
}

func TestUploadRejectsPathTraversal(t *testing.T) {
	m := testManager(t, &fakeProvider{})
	for _, key := range []string{"../secret", "/etc/passwd", "a/../secret", "bad\x00key"} {
		if _, err := m.UploadURL(context.Background(), "user-123", "post-media", key, "image/jpeg", 10); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q error = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestVerifyUploadChecksProviderObservedMetadata(t *testing.T) {
	p := &fakeProvider{headInfo: ObjectInfo{
		Key: "tenants/user-123/post/image.jpg", Size: 100, ContentType: "image/jpeg",
	}}
	m := testManager(t, p)
	info, err := m.VerifyUpload(context.Background(), "user-123", "post-media", "post/image.jpg", "image/jpeg", 100)
	if err != nil {
		t.Fatal(err)
	}
	if info.Key != "post/image.jpg" || p.key != "tenants/user-123/post/image.jpg" {
		t.Fatalf("verified object was not tenant-scoped: info=%+v provider=%+v", info, p)
	}
	if _, err := m.VerifyUpload(context.Background(), "user-123", "post-media", "post/image.jpg", "image/jpeg", 99); !errors.Is(err, ErrObjectMismatch) {
		t.Fatalf("size mismatch error = %v, want ErrObjectMismatch", err)
	}
}

func TestListFiltersUnexpectedProviderTenantKeys(t *testing.T) {
	p := &fakeProvider{listItems: []ObjectInfo{
		{Key: "tenants/user-123/post/one.jpg", Size: 10},
		{Key: "tenants/other-user/private.jpg", Size: 20},
	}}
	m := testManager(t, p)
	items, cursor, err := m.List(context.Background(), "user-123", "post-media", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "post/one.jpg" || cursor != "next-cursor" {
		t.Fatalf("unexpected scoped list: items=%+v cursor=%q", items, cursor)
	}
}

func TestPrivateDownloadAndDeleteAreTenantScoped(t *testing.T) {
	p := &fakeProvider{}
	m := testManager(t, p)
	if _, err := m.DownloadURL(context.Background(), "attacker", "owner", "post-media", "post/image.jpg"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant download error = %v, want ErrForbidden", err)
	}
	if err := m.Delete(context.Background(), "attacker", "owner", "post-media", "post/image.jpg"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant delete error = %v, want ErrForbidden", err)
	}
	if p.deleted { t.Fatal("provider delete called for unauthorized tenant") }
}

func TestNewRejectsDuplicateNamespaceAndInvalidPolicy(t *testing.T) {
	p := &fakeProvider{}
	ns := Namespace{Name: "media", PhysicalBucket: "b", MaxObjectBytes: 1, AllowedTypes: map[string]struct{}{"image/jpeg": {}}, UploadTTL: time.Minute, DownloadTTL: time.Minute}
	if _, err := New(p, []Namespace{ns, ns}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("duplicate namespace error = %v, want ErrInvalidNamespace", err)
	}
	ns.Name = "Bad Name"
	if _, err := New(p, []Namespace{ns}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("invalid namespace error = %v, want ErrInvalidNamespace", err)
	}
}
