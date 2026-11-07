package freshapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/account":
			if r.Header.Get("X-Appwrite-JWT") != "valid-session" {
				http.Error(w, "unauthorized", 401)
				return
			}
			_, _ = w.Write([]byte(`{"$id":"new_user_1","status":true}`))
		case strings.HasSuffix(r.URL.Path, "/documents"):
			if r.Method == http.MethodPost {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
				}
				data := body["data"].(map[string]any)
				if data["user_id"] != "new_user_1" {
					t.Errorf("unexpected owner: %v", data["user_id"])
				}
				w.WriteHeader(201)
				_, _ = w.Write([]byte(`{"$id":"new_user_1","display_name":"Tester"}`))
				return
			}
		case strings.Contains(r.URL.Path, "/documents/"):
			if r.Method == http.MethodGet {
				http.Error(w, "not found", 404)
				return
			}
			if r.Method == http.MethodPatch {
				_, _ = w.Write([]byte(`{"$id":"new_user_1","display_name":"Updated"}`))
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	db, err := appwritedb.New(appwritedb.Config{Endpoint: mock.URL + "/v1", ProjectID: "test-project", DatabaseID: "test-db", APIKey: "server-only-secret"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(db, mock.URL+"/v1", "test-project", mock.Client())
	if err != nil {
		t.Fatal(err)
	}
	return api, mock
}

func TestProfileRequiresAppwriteSession(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	req := httptest.NewRequest(http.MethodPut, "/v1/me/profile", strings.NewReader(`{"display_name":"Tester","handle":"tester","bio":"hello"}`))
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProfileUsesVerifiedAppwriteIdentity(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	req := httptest.NewRequest(http.MethodPut, "/v1/me/profile", strings.NewReader(`{"display_name":"Tester","handle":"tester","bio":"hello"}`))
	req.Header.Set("Authorization", "Bearer valid-session")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "new_user_1") {
		t.Fatalf("identity not used: %s", rec.Body.String())
	}
}

func TestProfileRejectsInvalidHandle(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	req := httptest.NewRequest(http.MethodPut, "/v1/me/profile", strings.NewReader(`{"display_name":"Tester","handle":"bad handle","bio":""}`))
	req.Header.Set("Authorization", "Bearer valid-session")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type fakeMediaService struct {
	reserveInput              freshstorage.ReserveInput
	completeOwner, completeID string
	downloadOwner, downloadID string
	restoreOwner, restoreID   string
}

func (f *fakeMediaService) ReserveUpload(_ context.Context, in freshstorage.ReserveInput) (freshstorage.UploadReservation, error) {
	f.reserveInput = in
	return freshstorage.UploadReservation{ReservationID: "res_123", ObjectID: "med_123", UploadURL: "https://storage.example/upload", UploadMethod: "PUT", RequiredHeaders: map[string]string{"Content-Type": in.ContentType}}, nil
}
func (f *fakeMediaService) CompleteUpload(_ context.Context, owner, id string) error {
	f.completeOwner, f.completeID = owner, id
	return nil
}
func (f *fakeMediaService) DownloadURL(_ context.Context, owner, id string) (string, error) {
	f.downloadOwner, f.downloadID = owner, id
	return "https://storage.example/private", nil
}
func (f *fakeMediaService) RestoreArchive(_ context.Context, owner, id string) error {
	f.restoreOwner, f.restoreID = owner, id
	return nil
}

func TestFreshMediaEndpointsUseAuthenticatedAppwriteOwner(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	media := &fakeMediaService{}
	api.WithStorage(media)
	reserve := httptest.NewRequest(http.MethodPost, "/v1/media/uploads/reservations", strings.NewReader(`{"bytes":1024,"content_type":"image/jpeg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	reserve.Header.Set("Authorization", "Bearer valid-session")
	reserve.Header.Set("Idempotency-Key", "upload-key-123")
	reserveRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(reserveRec, reserve)
	if reserveRec.Code != http.StatusCreated {
		t.Fatalf("reserve status=%d body=%s", reserveRec.Code, reserveRec.Body.String())
	}
	if media.reserveInput.OwnerID != "new_user_1" || media.reserveInput.IdempotencyKey != "upload-key-123" {
		t.Fatalf("owner/idempotency not bound to verified session: %#v", media.reserveInput)
	}
	complete := httptest.NewRequest(http.MethodPost, "/v1/media/uploads/res_123/complete", nil)
	complete.Header.Set("Authorization", "Bearer valid-session")
	completeRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(completeRec, complete)
	if completeRec.Code != http.StatusOK || media.completeOwner != "new_user_1" || media.completeID != "res_123" {
		t.Fatalf("complete response=%d %s owner=%s id=%s", completeRec.Code, completeRec.Body.String(), media.completeOwner, media.completeID)
	}
	download := httptest.NewRequest(http.MethodGet, "/v1/media/med_123/download", nil)
	download.Header.Set("Authorization", "Bearer valid-session")
	downloadRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(downloadRec, download)
	if downloadRec.Code != http.StatusOK || media.downloadOwner != "new_user_1" || media.downloadID != "med_123" {
		t.Fatalf("download response=%d %s", downloadRec.Code, downloadRec.Body.String())
	}
	restore := httptest.NewRequest(http.MethodPost, "/v1/media/med_123/restore", nil)
	restore.Header.Set("Authorization", "Bearer valid-session")
	restoreRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(restoreRec, restore)
	if restoreRec.Code != http.StatusOK || media.restoreOwner != "new_user_1" || media.restoreID != "med_123" {
		t.Fatalf("restore response=%d %s owner=%s id=%s", restoreRec.Code, restoreRec.Body.String(), media.restoreOwner, media.restoreID)
	}
}

func TestFreshMediaEndpointsRequireAppwriteSession(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	api.WithStorage(&fakeMediaService{})
	req := httptest.NewRequest(http.MethodPost, "/v1/media/uploads/reservations", strings.NewReader(`{"bytes":1024,"content_type":"image/jpeg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated media reserve status=%d", rec.Code)
	}
}

func TestFreshAPICORSAllowsOnlyConfiguredOriginsAndHeaders(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	if err := api.WithAllowedOrigins([]string{"https://app.testagram.example"}); err != nil {
		t.Fatal(err)
	}
	preflight := httptest.NewRequest(http.MethodOptions, "/v1/media/uploads/reservations", nil)
	preflight.Header.Set("Origin", "https://app.testagram.example")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type,idempotency-key")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.testagram.example" {
		t.Fatalf("preflight rejected: %d %s", rec.Code, rec.Body.String())
	}
	bad := httptest.NewRequest(http.MethodOptions, "/v1/media/uploads/reservations", nil)
	bad.Header.Set("Origin", "https://evil.example")
	bad.Header.Set("Access-Control-Request-Method", "POST")
	badRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("untrusted origin accepted: %d", badRec.Code)
	}
	badHeader := httptest.NewRequest(http.MethodOptions, "/v1/media/uploads/reservations", nil)
	badHeader.Header.Set("Origin", "https://app.testagram.example")
	badHeader.Header.Set("Access-Control-Request-Method", "POST")
	badHeader.Header.Set("Access-Control-Request-Headers", "x-appwrite-key")
	badHeaderRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(badHeaderRec, badHeader)
	if badHeaderRec.Code != http.StatusForbidden {
		t.Fatalf("unapproved request header accepted: %d", badHeaderRec.Code)
	}
}
