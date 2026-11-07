package freshstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

type restoreStoreFake struct {
	*memoryStore
	archive MediaReplica
	marked  []string
}

func (f *restoreStoreFake) GetArchiveReplica(context.Context, string) (MediaReplica, error) {
	return f.archive, nil
}
func (f *restoreStoreFake) MarkReplicaState(_ context.Context, _ string, provider, state string) error {
	f.marked = append(f.marked, provider+":"+state)
	return nil
}

func TestRestoreArchiveVerifiesAndRestoresBothActiveProviders(t *testing.T) {
	payload := []byte("archived-testagram-image")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	key := "v1/0123456789abcdef0123456789abcdef"
	objects := map[string][]byte{"archive-bucket/archive/" + key: append([]byte(nil), payload...)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodHead:
			b, ok := objects[k]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.Header().Set("Content-Type", "image/jpeg")
		case http.MethodGet:
			b, ok := objects[k]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(b)
		case http.MethodPut:
			b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				t.Errorf("read upload: %v", err)
				return
			}
			objects[k] = b
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unsupported", 405)
		}
	}))
	defer server.Close()
	makeProvider := func(name string) *virtualstorage.S3Provider {
		p, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: server.URL, AccessKey: name, SecretKey: "secret", Region: "us-east-1"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	b2, r2, archive := makeProvider("b2"), makeProvider("r2"), makeProvider("archive")
	base := &memoryStore{reservation: Reservation{ID: "res_1", OwnerID: "user_123", ObjectID: "med_test", ObjectKey: key, Bytes: int64(len(payload)), ContentType: "image/jpeg", SHA256: hash, Status: "protected"}, committed: true}
	ledger := &restoreStoreFake{memoryStore: base, archive: MediaReplica{ID: "archive_med_test", ObjectID: "med_test", Provider: "archive", Bucket: "archive-bucket", Key: "archive/" + key, Bytes: int64(len(payload)), SHA256: hash, State: "verified"}}
	service, err := NewService(ledger, ProviderSet{B2: b2, B2Bucket: "b2-bucket", R2: r2, R2Bucket: "r2-bucket", Archive: archive, ArchiveBucket: "archive-bucket"}, 500<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.RestoreArchive(context.Background(), "user_123", "med_test"); err != nil {
		t.Fatal(err)
	}
	if string(objects["b2-bucket/"+key]) != string(payload) || string(objects["r2-bucket/"+key]) != string(payload) {
		t.Fatal("restored active replicas do not match archive bytes")
	}
	if len(ledger.marked) != 2 || ledger.marked[0] != "b2:verified" || ledger.marked[1] != "r2:verified" {
		t.Fatalf("active replica ledger not restored: %v", ledger.marked)
	}
}

func TestRestoreArchiveRejectsNonOwnerBeforeProviderMutation(t *testing.T) {
	base := &memoryStore{reservation: Reservation{ID: "res_1", OwnerID: "user_123", ObjectID: "med_test", ObjectKey: "v1/0123456789abcdef0123456789abcdef", Bytes: 10, ContentType: "image/jpeg", SHA256: strings.Repeat("a", 64), Status: "protected"}, committed: true}
	ledger := &restoreStoreFake{memoryStore: base}
	service := &Service{store: ledger, archive: &virtualstorage.S3Provider{}}
	if err := service.RestoreArchive(context.Background(), "other-user", "med_test"); err != ErrNotOwner {
		t.Fatalf("non-owner restore accepted: %v", err)
	}
}
