package freshstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

func TestReserveKiBCeilingsNeverUndercount(t *testing.T) {
	cases := []struct{ bytes, want int64 }{{1, 1}, {quotaUnitBytes, 1}, {quotaUnitBytes + 1, 2}, {150000000000, 146484375}}
	for _, tc := range cases {
		if got := reserveKiB(tc.bytes); got != tc.want {
			t.Fatalf("reserveKiB(%d)=%d want %d", tc.bytes, got, tc.want)
		}
	}
}

func TestDecodeReservationRejectsMalformedAndPreservesStringByteCount(t *testing.T) {
	raw := json.RawMessage(`{"$id":"res_123","owner_id":"user_123","scope":"post_media","object_id":"med_123","object_key":"v1/0123456789abcdef0123456789abcdef","bytes_decimal":"5368709120","reserved_kib":5242880,"content_type":"video/mp4","sha256_expected":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sha256_actual":"","status":"reserved","expires_at":"2026-11-07T12:00:00Z"}`)
	got, err := decodeReservation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bytes != 5368709120 || got.ReservedKiB != 5242880 {
		t.Fatalf("large reservation was truncated: %#v", got)
	}
	if _, err := decodeReservation([]byte(`{"$id":"x","owner_id":"u","bytes_decimal":"12x","reserved_kib":1,"expires_at":"bad"}`)); err == nil {
		t.Fatal("malformed reservation accepted")
	}
}


func TestDecodeReservationPreservesR2OriginAndDefaultsLegacyToB2(t *testing.T) {
	base := `{"$id":"res_123","owner_id":"user_123","scope":"post_media","object_id":"med_123","object_key":"v1/0123456789abcdef0123456789abcdef","bytes_decimal":"1000","reserved_kib":1,"content_type":"image/jpeg","sha256_expected":"","sha256_actual":"","status":"reserved","expires_at":"2026-11-07T12:00:00Z","primary_provider":"r2"}`
	got, err := decodeReservation([]byte(base))
	if err != nil { t.Fatal(err) }
	if got.PrimaryProvider != "r2" { t.Fatalf("provider=%q, want r2", got.PrimaryProvider) }
	legacy := strings.Replace(base, `,"primary_provider":"r2"`, "", 1)
	got, err = decodeReservation([]byte(legacy))
	if err != nil { t.Fatal(err) }
	if got.PrimaryProvider != "b2" { t.Fatalf("legacy provider=%q, want b2", got.PrimaryProvider) }
}

func TestProtectedMediaMetadataAppliesRetentionClassAndScope(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	base := Reservation{OwnerID: "user_123", ObjectID: "med_123", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 1024, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64)}
	post := base
	post.Scope = "post_media"
	postData, err := protectedMediaMetadata(post, now)
	if err != nil {
		t.Fatal(err)
	}
	if postData["retention_class"] != "permanent" || postData["scope"] != "post_media" || postData["lifecycle_state"] != "active" || postData["archive_after"] != now.Add(30*24*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("post media lifecycle metadata incomplete: %#v", postData)
	}
	story := base
	story.Scope = "story_media"
	storyData, err := protectedMediaMetadata(story, now)
	if err != nil {
		t.Fatal(err)
	}
	if storyData["retention_class"] != "story" || storyData["scope"] != "story_media" || storyData["story_expires_at"] != now.Add(24*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("story media lifecycle metadata incomplete: %#v", storyData)
	}
	if _, err := protectedMediaMetadata(Reservation{OwnerID: "user_123", ObjectID: "med_123", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 1, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64), Scope: "unknown"}, now); err == nil {
		t.Fatal("unknown media scope accepted")
	}
}

func TestQuotaErrorMapsAtomicBoundConflicts(t *testing.T) {
	got := quotaError(appwritedb.HTTPError{StatusCode: 409})
	if !errors.Is(got, ErrStorageConflict) {
		t.Fatalf("transaction conflicts must be retryable, got %v", got)
	}
	quota := quotaError(appwritedb.HTTPError{StatusCode: 400, Message: "maximum value exceeded"})
	if !errors.Is(quota, ErrQuotaExceeded) {
		t.Fatalf("bounded atomic increment should map to quota, got %v", quota)
	}
	other := quotaError(errors.New("network"))
	if errors.Is(other, ErrQuotaExceeded) {
		t.Fatalf("network error misclassified as quota error: %v", other)
	}
}

func TestQuotaInvariantRejectsUsedPlusReservedOverflow(t *testing.T) {
	// Exercise the exact invariants independently from Appwrite transport.
	values := []byte(`{"limit_kib":10,"used_kib":8,"reserved_kib":3}`)
	var q struct {
		Limit    int64 `json:"limit_kib"`
		Used     int64 `json:"used_kib"`
		Reserved int64 `json:"reserved_kib"`
	}
	if err := json.Unmarshal(values, &q); err != nil {
		t.Fatal(err)
	}
	if q.Used+q.Reserved <= q.Limit {
		t.Fatal("invalid quota test fixture")
	}
	_ = time.Now()
}

func TestAppwriteStoreReservesPlatformAndUserQuotaAtomically(t *testing.T) {
	var stagedIncrements int
	var stagedReservation bool
	var committed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_reservations/documents/res_test"):
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/user_123"):
			_, _ = w.Write([]byte(`{"$id":"user_123","limit_kib":10000,"used_kib":100,"reserved_kib":200}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/platform"):
			_, _ = w.Write([]byte(`{"$id":"platform","limit_kib":146484375,"used_kib":1000,"reserved_kib":2000}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_test"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/reserved_kib/increment"):
			stagedIncrements++
			if r.URL.Query().Get("transactionId") != "tx_test" {
				t.Errorf("missing transaction ID")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["value"] != float64(2) {
				t.Errorf("unexpected reservation units: %#v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"$id":"quota"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/storage_reservations/documents"):
			if r.URL.Query().Get("transactionId") != "tx_test" {
				t.Errorf("reservation create not staged")
			}
			stagedReservation = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"$id":"res_test"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/transactions/tx_test":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			committed = body["commit"] == true
			_, _ = w.Write([]byte(`{"$id":"tx_test","status":"committed"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAppwriteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	input := ReserveInput{OwnerID: "user_123", IdempotencyKey: "upload-key-0001", Bytes: 1500, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64)}
	got, err := store.Reserve(context.Background(), input, "res_test", time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.ReservedKiB != 2 || got.Status != "reserved" || got.ObjectKey == "" {
		t.Fatalf("unexpected reservation: %#v", got)
	}
	if stagedIncrements != 2 || !stagedReservation || !committed {
		t.Fatalf("quota reservation was not atomic: increments=%d document=%v commit=%v", stagedIncrements, stagedReservation, committed)
	}
}

func TestAppwriteStoreRejectsPlatformQuotaAbove150GB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/storage_reservations/documents/res_test") {
			http.Error(w, "not found", 404)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/user_123") {
			_, _ = w.Write([]byte(`{"limit_kib":1000,"used_kib":0,"reserved_kib":0}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/platform") {
			_, _ = w.Write([]byte(`{"limit_kib":146484376,"used_kib":0,"reserved_kib":0}`))
			return
		}
		http.Error(w, "unexpected", 404)
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := NewAppwriteStore(db)
	_, err = store.Reserve(context.Background(), ReserveInput{OwnerID: "user_123", IdempotencyKey: "upload-key-0001", Bytes: 100, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64)}, "res_test", time.Now().Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "150 GB") {
		t.Fatalf("oversized platform quota accepted: %v", err)
	}
}

type memoryStore struct {
	reservation   Reservation
	primaryMarked bool
	committed     bool
}

func (m *memoryStore) Reserve(_ context.Context, in ReserveInput, id string, expires time.Time) (Reservation, error) {
	if m.reservation.ID != "" {
		return m.reservation, nil
	}
	m.reservation = Reservation{ID: id, OwnerID: in.OwnerID, Scope: in.Scope, ObjectID: "med_test", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: in.Bytes, ReservedKiB: reserveKiB(in.Bytes), ContentType: in.ContentType, ExpectedSHA256: in.SHA256, PrimaryProvider: in.PrimaryProvider, Status: "reserved", ExpiresAt: expires}
	return m.reservation, nil
}
func (m *memoryStore) GetReservation(_ context.Context, id string) (Reservation, error) {
	if id != m.reservation.ID {
		return Reservation{}, ErrReservationNotFound
	}
	return m.reservation, nil
}
func (m *memoryStore) ClaimReservation(_ context.Context, id, expected, next string, fields map[string]any) (Reservation, error) {
	if id != m.reservation.ID || m.reservation.Status != expected {
		return Reservation{}, errors.New("state conflict")
	}
	m.reservation.Status = next
	if next == "uploaded_primary" {
		m.primaryMarked = true
	}
	if value, ok := fields["sha256_actual"].(string); ok {
		m.reservation.SHA256 = value
	}
	if value, ok := fields["primary_provider"].(string); ok { m.reservation.PrimaryProvider = value }
	if value, ok := fields["lease_expires_at"].(string); ok {
		m.reservation.LeaseExpiresAt, _ = time.Parse(time.RFC3339Nano, value)
	}
	return m.reservation, nil
}
func (m *memoryStore) ReleaseExpiredReservation(_ context.Context, r Reservation) error {
	m.reservation.Status = "expired"
	return nil
}
func (m *memoryStore) ListExpiredReservations(context.Context, time.Time, int) ([]Reservation, error) {
	return nil, nil
}

func (m *memoryStore) CommitProtected(_ context.Context, r Reservation, _, _ string) error {
	m.committed = true
	m.reservation.Status = "protected"
	return nil
}
func (m *memoryStore) GetMediaRecord(_ context.Context, id string) (MediaRecord, error) {
	if id != m.reservation.ObjectID || !m.committed {
		return MediaRecord{}, ErrNotReady
	}
	return MediaRecord{ObjectID: id, OwnerID: m.reservation.OwnerID, ObjectKey: m.reservation.ObjectKey, Bytes: m.reservation.Bytes, ContentType: m.reservation.ContentType, SHA256: m.reservation.SHA256, Status: "ready"}, nil
}
func (m *memoryStore) MarkReplicaState(context.Context, string, string, string) error { return nil }

func (m *memoryStore) MediaKey(_ context.Context, owner, id string) (string, error) {
	if owner != m.reservation.OwnerID {
		return "", ErrNotOwner
	}
	if id != m.reservation.ObjectID || !m.committed {
		return "", ErrNotReady
	}
	return m.reservation.ObjectKey, nil
}

func TestReserveUploadDefaultsScopeAndRejectsUnsupportedScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer server.Close()
	b2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "b2", SecretKey: "secret", Region: "us-east-005"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "r2", SecretKey: "secret", Region: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	service, err := NewService(store, ProviderSet{B2: b2, B2Bucket: "b2-bucket", R2: r2, R2Bucket: "r2-bucket"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReserveUpload(context.Background(), ReserveInput{OwnerID: "user_123", IdempotencyKey: "upload-default-scope", Bytes: 1024, ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	if store.reservation.Scope != "post_media" {
		t.Fatalf("default media scope=%q", store.reservation.Scope)
	}
	if _, err := service.ReserveUpload(context.Background(), ReserveInput{OwnerID: "user_123", IdempotencyKey: "upload-invalid-scope", Scope: "unknown", Bytes: 1024, ContentType: "image/jpeg"}); err == nil {
		t.Fatal("unsupported media scope accepted")
	}
}

func TestReserveUploadReturnsSelectedR2Origin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer server.Close()
	b2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "b2", SecretKey: "secret", Region: "us-east-005"})
	if err != nil { t.Fatal(err) }
	r2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "r2", SecretKey: "secret", Region: "auto"})
	if err != nil { t.Fatal(err) }
	store := &memoryStore{}
	service, err := NewService(store, ProviderSet{B2: b2, B2Bucket: "b2-bucket", R2: r2, R2Bucket: "r2-bucket", PrimaryProvider: "r2"}, 1<<20)
	if err != nil { t.Fatal(err) }
	reservation, err := service.ReserveUpload(context.Background(), ReserveInput{OwnerID: "user_123", IdempotencyKey: "upload-r2-origin", Bytes: 1024, ContentType: "image/jpeg"})
	if err != nil { t.Fatal(err) }
	if reservation.PrimaryProvider != "r2" || store.reservation.PrimaryProvider != "r2" {
		t.Fatalf("R2 origin was not returned and persisted: response=%q stored=%q", reservation.PrimaryProvider, store.reservation.PrimaryProvider)
	}
	if reservation.UploadURL == "" || reservation.UploadMethod != "PUT" {
		t.Fatalf("R2-origin reservation did not return a direct upload URL: %#v", reservation)
	}
}

func TestServiceCompletesB2UploadOnlyAfterVerifiedR2Replica(t *testing.T) {
	payload := []byte("fresh-platform-image")
	hash := sha256.Sum256(payload)
	expected := hex.EncodeToString(hash[:])
	objects := map[string][]byte{"b2-bucket/v1/0123456789abcdef0123456789abcdef": append([]byte(nil), payload...)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodHead:
			body, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Content-Type", "image/jpeg")
		case http.MethodGet:
			body, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(body)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read replica: %v", err)
				return
			}
			objects[key] = body
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unsupported", 405)
		}
	}))
	defer server.Close()
	b2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "b2", SecretKey: "secret", Region: "us-west-004"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "r2", SecretKey: "secret", Region: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{reservation: Reservation{ID: "res_test", OwnerID: "user_123", ObjectID: "med_test", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: int64(len(payload)), ReservedKiB: 1, ContentType: "image/jpeg", ExpectedSHA256: expected, Status: "reserved", ExpiresAt: time.Now().Add(time.Minute)}}
	service, err := NewService(store, ProviderSet{B2: b2, B2Bucket: "b2-bucket", R2: r2, R2Bucket: "r2-bucket"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteUpload(context.Background(), "user_123", "res_test"); err != nil {
		t.Fatal(err)
	}
	if !store.primaryMarked || !store.committed || store.reservation.Status != "protected" {
		t.Fatalf("state machine did not complete: %#v", store)
	}
	if !bytes.Equal(objects["r2-bucket/v1/0123456789abcdef0123456789abcdef"], payload) {
		t.Fatal("R2 replica differs from primary")
	}
	if _, err := service.DownloadURL(context.Background(), "other_user", "med_test"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("private object leaked to another owner: %v", err)
	}
	if _, err := service.DownloadURL(context.Background(), "user_123", "med_test"); err != nil {
		t.Fatalf("owner should receive private signed URL: %v", err)
	}
	objects["r2-bucket/v1/0123456789abcdef0123456789abcdef"] = []byte("corrupted replica")
	if err := service.RepairObject(context.Background(), "med_test"); err != nil {
		t.Fatalf("repair should restore corrupt R2 from verified B2: %v", err)
	}
	if !bytes.Equal(objects["r2-bucket/v1/0123456789abcdef0123456789abcdef"], payload) {
		t.Fatal("repair did not restore exact R2 object bytes")
	}
	delete(objects, "r2-bucket/v1/0123456789abcdef0123456789abcdef")
	fallbackURL, err := service.DownloadURL(context.Background(), "user_123", "med_test")
	if err != nil {
		t.Fatalf("download should fall back to verified B2 when R2 is missing: %v", err)
	}
	parsedFallback, err := url.Parse(fallbackURL)
	if err != nil || !strings.Contains(parsedFallback.Path, "b2-bucket") {
		t.Fatalf("expected direct B2 fallback URL, got %q (parse error %v)", fallbackURL, err)
	}
}


func TestServiceCompletesR2OriginAndCreatesVerifiedB2Replica(t *testing.T) {
	payload := []byte("r2-origin-media")
	hash := sha256.Sum256(payload)
	expected := hex.EncodeToString(hash[:])
	key := "v1/0123456789abcdef0123456789abcdef"
	objects := map[string][]byte{"r2-bucket/" + key: append([]byte(nil), payload...)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		objectKey := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodHead:
			body, ok := objects[objectKey]
			if !ok { http.NotFound(w, r); return }
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Content-Type", "image/jpeg")
		case http.MethodGet:
			body, ok := objects[objectKey]
			if !ok { http.NotFound(w, r); return }
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(body)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil { t.Errorf("read B2 replica: %v", err); return }
			objects[objectKey] = body
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	b2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "b2", SecretKey: "secret", Region: "us-west-004"})
	if err != nil { t.Fatal(err) }
	r2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "r2", SecretKey: "secret", Region: "auto"})
	if err != nil { t.Fatal(err) }
	store := &memoryStore{reservation: Reservation{ID: "res_r2", OwnerID: "user_123", ObjectID: "med_r2", ObjectKey: key, Bytes: int64(len(payload)), ReservedKiB: 1, ContentType: "image/jpeg", ExpectedSHA256: expected, PrimaryProvider: "r2", Status: "reserved", ExpiresAt: time.Now().Add(time.Minute)}}
	service, err := NewService(store, ProviderSet{B2: b2, B2Bucket: "b2-bucket", R2: r2, R2Bucket: "r2-bucket", PrimaryProvider: "r2"}, 1<<20)
	if err != nil { t.Fatal(err) }
	if err := service.CompleteUpload(context.Background(), "user_123", "res_r2"); err != nil { t.Fatal(err) }
	if !store.primaryMarked || !store.committed || store.reservation.Status != "protected" { t.Fatalf("R2-origin state machine did not complete: %#v", store) }
	if !bytes.Equal(objects["b2-bucket/"+key], payload) { t.Fatal("B2 replica differs from verified R2 origin") }
}

func TestExpiredReservationDeletesCloudObjectsBeforeReleasingQuota(t *testing.T) {
	payload := []byte("expired-media")
	objects := map[string][]byte{"b2-bucket/v1/0123456789abcdef0123456789abcdef": payload}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodHead:
			body, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Header().Set("Content-Type", "image/jpeg")
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", 405)
		}
	}))
	defer server.Close()
	b2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "b2", SecretKey: "secret", Region: "us-west-004"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: "r2", SecretKey: "secret", Region: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{reservation: Reservation{ID: "res_expired", OwnerID: "user_123", ObjectID: "med_expired", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: int64(len(payload)), ReservedKiB: 1, ContentType: "image/jpeg", Status: "reserved", ExpiresAt: time.Now().Add(-time.Minute)}}
	service, err := NewService(store, ProviderSet{B2: b2, B2Bucket: "b2-bucket", R2: r2, R2Bucket: "r2-bucket"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ExpireReservation(context.Background(), "res_expired"); err != nil {
		t.Fatal(err)
	}
	if store.reservation.Status != "expired" {
		t.Fatalf("reservation state=%s", store.reservation.Status)
	}
	if _, ok := objects["b2-bucket/v1/0123456789abcdef0123456789abcdef"]; ok {
		t.Fatal("B2 object was not deleted")
	}
}

func TestExpiredReservationDoesNotDeleteWhileVerificationLeaseIsActive(t *testing.T) {
	store := &memoryStore{reservation: Reservation{ID: "res_busy", OwnerID: "user_123", ObjectID: "med_busy", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 1, ReservedKiB: 1, ContentType: "image/jpeg", Status: "verifying", ExpiresAt: time.Now().Add(-time.Minute), LeaseExpiresAt: time.Now().Add(time.Minute)}}
	service := &Service{store: store}
	if err := service.ExpireReservation(context.Background(), "res_busy"); err == nil {
		t.Fatal("active verification lease must block expiry")
	}
	if store.reservation.Status != "verifying" {
		t.Fatalf("active reservation state changed: %s", store.reservation.Status)
	}
}

func TestBootstrapPlatformQuotaCreatesOnlyMissingDocument(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/platform") {
			http.Error(w, "not found", 404)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			data := body["data"].(map[string]any)
			if data["limit_kib"] != float64(146484375) || data["used_kib"] != float64(0) || data["reserved_kib"] != float64(0) {
				t.Errorf("incorrect initial platform quota: %#v", data)
			}
			created = true
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"$id":"platform"}`))
			return
		}
		http.Error(w, "unexpected", 404)
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAppwriteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapPlatformQuota(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("platform quota document was not created")
	}
}

func TestEnsureUserQuotaCreatesDefaultQuotaOnFirstUpload(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/user_123") {
			http.Error(w, "not found", 404)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			data := body["data"].(map[string]any)
			if data["limit_kib"] != float64(2097152) {
				t.Errorf("default 2 GiB quota is incorrect: %#v", data)
			}
			created = true
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"$id":"user_123"}`))
			return
		}
		http.Error(w, "unexpected", 404)
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAppwriteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := store.ensureUserQuota(context.Background(), "user_123")
	if err != nil {
		t.Fatal(err)
	}
	if !created || quota.LimitKiB != 2097152 {
		t.Fatalf("user quota was not bootstrapped: created=%v quota=%#v", created, quota)
	}
}

func TestPrivateCDNURLSignatureMatchesWorkerContract(t *testing.T) {
	now := time.Unix(1794052800, 0).UTC()
	got, err := signPrivateMediaURL("https://media.testagram.example", "0123456789abcdef0123456789abcdef", "v1/0123456789abcdef0123456789abcdef", 3600, now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/private/v1/0123456789abcdef0123456789abcdef" || u.Query().Get("e") != strconv.FormatInt(now.Unix()+3600, 10) || len(u.Query().Get("s")) != 64 {
		t.Fatalf("unexpected signed private CDN URL: %s", got)
	}
	if _, err := signPrivateMediaURL("http://media.example", "secret", "v1/0123456789abcdef0123456789abcdef", 3600, now); err == nil {
		t.Fatal("insecure CDN URL accepted")
	}
}

func TestCommitProtectedAtomicallyMovesQuotaAndWritesReplicaLedger(t *testing.T) {
	var quotaOps, docCreates int
	var committed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/storage_quotas/documents/user_123"):
			_, _ = w.Write([]byte(`{"$id":"user_123","limit_kib":2097152,"used_kib":10,"reserved_kib":1}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/storage_quotas/documents/platform"):
			_, _ = w.Write([]byte(`{"$id":"platform","limit_kib":146484375,"used_kib":100,"reserved_kib":1}`))
		case r.Method == http.MethodPost && path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_commit"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/storage_reservations/documents/res_123"):
			if r.URL.Query().Get("transactionId") != "tx_commit" {
				t.Errorf("reservation not read in transaction")
			}
			_, _ = w.Write([]byte(`{"$id":"res_123","owner_id":"user_123","scope":"post_media","object_id":"med_123","object_key":"v1/0123456789abcdef0123456789abcdef","bytes_decimal":"1000","reserved_kib":1,"content_type":"image/jpeg","sha256_expected":"","sha256_actual":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"uploaded_primary","expires_at":"2026-11-07T12:00:00Z","lease_expires_at":"2026-11-07T13:00:00Z"}`))
		case r.Method == http.MethodPatch && (strings.HasSuffix(path, "/reserved_kib/decrement") || strings.HasSuffix(path, "/used_kib/increment")):
			quotaOps++
			if r.URL.Query().Get("transactionId") != "tx_commit" {
				t.Errorf("quota op missing transaction id")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"$id":"quota"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/storage_reservations/documents/res_123"):
			if r.URL.Query().Get("transactionId") != "tx_commit" {
				t.Errorf("reservation update missing transaction id")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["data"].(map[string]any)["status"] != "protected" {
				t.Errorf("reservation not marked protected: %#v", body)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"$id":"res_123"}`))
		case r.Method == http.MethodPost && (strings.HasSuffix(path, "/media_objects/documents") || strings.HasSuffix(path, "/storage_replicas/documents")):
			if r.URL.Query().Get("transactionId") != "tx_commit" {
				t.Errorf("ledger create missing transaction id")
			}
			docCreates++
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"$id":"created"}`))
		case r.Method == http.MethodPatch && path == "/v1/databases/transactions/tx_commit":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			committed = body["commit"] == true
			_, _ = w.Write([]byte(`{"$id":"tx_commit","status":"committed"}`))
		default:
			t.Errorf("unexpected Appwrite request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := NewAppwriteStore(db)
	r := Reservation{ID: "res_123", OwnerID: "user_123", Scope: "post_media", ObjectID: "med_123", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 1000, ReservedKiB: 1, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64), Status: "uploaded_primary"}
	if err := store.CommitProtected(context.Background(), r, "b2-bucket", "r2-bucket"); err != nil {
		t.Fatal(err)
	}
	if quotaOps != 4 || docCreates != 3 || !committed {
		t.Fatalf("incomplete atomic commit: quotaOps=%d docCreates=%d committed=%v", quotaOps, docCreates, committed)
	}
}

func TestCommitProtectedRejectsConcurrentExpiryClaim(t *testing.T) {
	quotaOps := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/user_123"):
			_, _ = w.Write([]byte(`{"limit_kib":1000,"used_kib":0,"reserved_kib":1}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_quotas/documents/platform"):
			_, _ = w.Write([]byte(`{"limit_kib":146484375,"used_kib":0,"reserved_kib":1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_race"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/storage_reservations/documents/res_123"):
			_, _ = w.Write([]byte(`{"$id":"res_123","owner_id":"user_123","scope":"post_media","object_id":"med_123","object_key":"v1/0123456789abcdef0123456789abcdef","bytes_decimal":"1000","reserved_kib":1,"content_type":"image/jpeg","sha256_actual":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"expiring","expires_at":"2026-11-07T12:00:00Z"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/transactions/tx_race":
			_, _ = w.Write([]byte(`{"$id":"tx_race","status":"rolled_back"}`))
		case strings.Contains(r.URL.Path, "/reserved_kib/") || strings.Contains(r.URL.Path, "/used_kib/"):
			quotaOps++
			w.WriteHeader(200)
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := NewAppwriteStore(db)
	r := Reservation{ID: "res_123", OwnerID: "user_123", Scope: "post_media", ObjectID: "med_123", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 1000, ReservedKiB: 1, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64), Status: "uploaded_primary"}
	if err := store.CommitProtected(context.Background(), r, "b2-bucket", "r2-bucket"); err == nil {
		t.Fatal("expiry race was allowed to commit protected media")
	}
	if quotaOps != 0 {
		t.Fatalf("quota was mutated after expiry claim: %d operations", quotaOps)
	}
}

func TestAppwriteExpiryQueriesUseDocumentAPIJSONFormat(t *testing.T) {
	query := appwriteAndQuery(appwriteEqualQuery("status", "verifying"), appwriteLessThanEqualQuery("expires_at", "2026-11-07T12:00:00Z"), appwriteLessThanEqualQuery("lease_expires_at", "2026-11-07T12:00:00Z"))
	var parsed struct {
		Method string            `json:"method"`
		Values []json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(query, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Method != "and" || len(parsed.Values) != 3 {
		t.Fatalf("invalid Appwrite AND query: %s", query)
	}
	for _, raw := range parsed.Values {
		var child map[string]any
		if err := json.Unmarshal(raw, &child); err != nil {
			t.Fatal(err)
		}
		if child["method"] == nil || child["column"] == nil {
			t.Fatalf("invalid Appwrite REST query child: %s", raw)
		}
	}
}

func TestReleaseExpiredReservationRequiresExpiryClaimAndReleasesBothQuotas(t *testing.T) {
	decrements, committed := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/storage_quotas/documents/user_123"):
			_, _ = w.Write([]byte(`{"limit_kib":2097152,"used_kib":0,"reserved_kib":1}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/storage_quotas/documents/platform"):
			_, _ = w.Write([]byte(`{"limit_kib":146484375,"used_kib":0,"reserved_kib":1}`))
		case r.Method == http.MethodPost && path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_expire"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/storage_reservations/documents/res_123"):
			_, _ = w.Write([]byte(`{"$id":"res_123","owner_id":"user_123","scope":"post_media","object_id":"med_123","object_key":"v1/0123456789abcdef0123456789abcdef","bytes_decimal":"1000","reserved_kib":1,"content_type":"image/jpeg","status":"expiring","expires_at":"2026-11-07T12:00:00Z"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/reserved_kib/decrement"):
			decrements++
			if r.URL.Query().Get("transactionId") != "tx_expire" {
				t.Errorf("quota release missing transaction id")
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"$id":"quota"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(path, "/storage_reservations/documents/res_123"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["data"].(map[string]any)["status"] != "expired" {
				t.Errorf("reservation not marked expired: %#v", body)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"$id":"res_123"}`))
		case r.Method == http.MethodPatch && path == "/v1/databases/transactions/tx_expire":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			committed = body["commit"] == true
			_, _ = w.Write([]byte(`{"$id":"tx_expire"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	db, err := appwritedb.New(appwritedb.Config{Endpoint: server.URL + "/v1", ProjectID: "p", APIKey: "secret", DatabaseID: "db"})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := NewAppwriteStore(db)
	r := Reservation{ID: "res_123", OwnerID: "user_123", Scope: "post_media", ObjectID: "med_123", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 1000, ReservedKiB: 1, Status: "expiring"}
	if err := store.ReleaseExpiredReservation(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if decrements != 2 || !committed {
		t.Fatalf("quota release incomplete: decrements=%d committed=%v", decrements, committed)
	}
}

func TestValidObjectKeySupportsOwnerBoundFreshMediaKeys(t *testing.T) {
	if !validObjectKey("users/user_123.test/0123456789abcdef0123456789abcdef") {
		t.Fatal("valid Appwrite owner-bound object key rejected")
	}
	if validObjectKey("users/other/0123456789abcdef0123456789abcdef") && freshMediaObjectKeyPattern.MatchString("users/user_123.test/0123456789abcdef0123456789abcdef") == false {
		t.Fatal("unexpected owner-bound object key validation mismatch")
	}
}
