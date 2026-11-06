package freshapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
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
