package freshapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshfeed"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

type mockAppwriteState struct {
	authUnavailable          bool
	documents                map[string][]byte
	transactionCommitted     bool
	outboxQueries            []string
	outboxDocuments          []json.RawMessage
	profileFederationEnabled bool
	accountActive            bool
	emailVerified            bool
	profileIdentityStatus    string
	identityFingerprintExists bool
	createdAppwriteUsers int
	profileUpdates           map[string]map[string]any
}

type staticModeration struct {
	result ModerationResult
	err error
}

func (m staticModeration) Analyze(context.Context, string) (ModerationResult, error) {
	return m.result, m.err
}

func testServer(t *testing.T) (*Server, *httptest.Server) {
	api, mock, _ := testServerWithState(t)
	return api, mock
}

func testServerWithState(t *testing.T) (*Server, *httptest.Server, *mockAppwriteState) {
	t.Helper()
	state := &mockAppwriteState{documents: map[string][]byte{}, profileFederationEnabled: true, accountActive: true, emailVerified: true, profileIdentityStatus: "approved", profileUpdates: map[string]map[string]any{}}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/account":
			if state.authUnavailable && r.Header.Get("X-Appwrite-JWT") == "" {
				http.Error(w, "Appwrite Auth unavailable", http.StatusServiceUnavailable)
				return
			}
			if r.Header.Get("X-Appwrite-JWT") != "valid-session" {
				http.Error(w, "unauthorized", 401)
				return
			}
			_, _ = w.Write([]byte(`{"$id":"new_user_1","status":true,"emailVerification":`+strconv.FormatBool(state.emailVerified)+`}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/users":
			if r.Header.Get("X-Appwrite-Key") != "appwrite-test-server-key" {
				http.Error(w, "invalid server key", http.StatusUnauthorized)
				return
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil { t.Errorf("decode Appwrite user: %v", err) }
			if input["emailVerification"] != true || input["email"] != "new@example.com" { t.Errorf("unexpected Appwrite account payload: %#v", input) }
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"$id":"new_user_1","email":"new@example.com","status":true,"emailVerification":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/users":
			if r.Header.Get("X-Appwrite-Key") != "appwrite-test-server-key" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			state.createdAppwriteUsers++
			_, _ = w.Write([]byte(`{"$id":"new_user_1","email":"new@example.com","emailVerification":true,"status":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/users/new_user_1":
			status := state.accountActive
			_, _ = w.Write([]byte(`{"$id":"new_user_1","status":`+strconv.FormatBool(status)+`}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/databases/transactions":
			_, _ = w.Write([]byte(`{"$id":"tx_posts"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/databases/transactions/tx_posts":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			state.transactionCommitted = body["commit"] == true
			_, _ = w.Write([]byte(`{"$id":"tx_posts","status":"committed"}`))
		case strings.HasSuffix(r.URL.Path, "/documents"):
			if r.Method == http.MethodGet {
				if strings.Contains(r.URL.Path, "/collections/profiles/") {
					if strings.Contains(strings.Join(r.URL.Query()["queries[]"], " "), "identity_fingerprint_hash") && state.identityFingerprintExists {
						_, _ = w.Write([]byte(`{"documents":[{"$id":"existing_identity"}],"total":1}`))
						return
					}
					if strings.Contains(strings.Join(r.URL.Query()["queries[]"], " "), "tester") {
						profile := map[string]any{"$id":"new_user_1","user_id":"new_user_1","handle":"tester","display_name":"Test User","bio":"A test profile","federation_enabled":state.profileFederationEnabled,"identity_verification_status":state.profileIdentityStatus}
						encoded, _ := json.Marshal(map[string]any{"documents":[]any{profile},"total":1})
						_, _ = w.Write(encoded)
					} else {
						_, _ = w.Write([]byte(`{"documents":[],"total":0}`))
					}
					return
				}
				if strings.Contains(r.URL.Path, "/collections/backend_outbox/") {
					state.outboxQueries = append(state.outboxQueries, r.URL.Query()["queries[]"]...)
					if len(state.outboxDocuments) > 0 {
						encoded, _ := json.Marshal(map[string]any{"documents": state.outboxDocuments, "total": len(state.outboxDocuments)})
						_, _ = w.Write(encoded)
						return
					}
				}
				_, _ = w.Write([]byte(`{"documents":[],"total":0}`))
				return
			}
			if r.Method == http.MethodPost {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
				}
				data := body["data"].(map[string]any)
				if strings.Contains(r.URL.Path, "/collections/posts/") || strings.Contains(r.URL.Path, "/collections/backend_outbox/") {
					if r.URL.Query().Get("transactionId") != "tx_posts" {
						t.Errorf("transactional document missing transaction ID: %s", r.URL.RawQuery)
					}
					id := body["documentId"].(string)
					if _, exists := state.documents[id]; exists {
						http.Error(w, "document already exists", http.StatusConflict)
						return
					}
					doc := map[string]any{"$id": id}
					for key, value := range data {
						doc[key] = value
					}
					encoded, _ := json.Marshal(doc)
					state.documents[id] = encoded
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write(encoded)
					return
				}
				if data["user_id"] != "new_user_1" {
					t.Errorf("unexpected owner: %v", data["user_id"])
				}
				w.WriteHeader(201)
				_, _ = w.Write([]byte(`{"$id":"new_user_1","display_name":"Tester"}`))
				return
			}
		case strings.Contains(r.URL.Path, "/documents/"):
			if r.Method == http.MethodGet {
				if strings.Contains(r.URL.Path, "/collections/profiles/documents/") {
					_, _ = w.Write([]byte(`{"$id":"new_user_1","user_id":"new_user_1","handle":"tester","federation_enabled":`+strconv.FormatBool(state.profileFederationEnabled)+`,"identity_verification_status":"`+state.profileIdentityStatus+`"}`))
					return
				}
				id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				if saved, ok := state.documents[id]; ok {
					_, _ = w.Write(saved)
					return
				}
				http.Error(w, "not found", 404)
				return
			}
			if r.Method == http.MethodPatch {
				var body struct {
					Data map[string]any `json:"data"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if strings.Contains(r.URL.Path, "/collections/profiles/documents/") {
					state.profileUpdates["new_user_1"] = body.Data
				}
				encoded, _ := json.Marshal(map[string]any{"$id": "new_user_1", "data": body.Data})
				_, _ = w.Write(encoded)
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
	api.WithModeration(staticModeration{result: ModerationResult{OverallScore: 0, Action: "pass"}})
	return api, mock, state
}

func TestFederationReadinessIsPrivateAndIndependentOfAlgorithm(t *testing.T) {
	api, mock, _ := testServerWithState(t)
	defer mock.Close()
	if err := api.WithInternalToken(strings.Repeat("f", 40)); err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	api.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/v1/federation/readyz", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("private readiness without token status=%d", unauthorized.Code)
	}
	ready := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/readyz", nil)
	ready.Header.Set("Authorization", "Bearer "+strings.Repeat("f", 40))
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, ready)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outbox":"ready"`) {
		t.Fatalf("Federation readiness should not depend on Algorithm readiness: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFederationActorRequiresInternalServiceToken(t *testing.T) {
	api, mock, _ := testServerWithState(t)
	defer mock.Close()
	if err := api.WithInternalToken(strings.Repeat("s", 40)); err != nil { t.Fatal(err) }
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/actors/tester", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized { t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String()) }
}

func TestFederationActorRequiresExplicitOptInAndActiveAppwriteAccount(t *testing.T) {
	for _, tc := range []struct{name string; enabled, active bool; want int}{
		{"not opted in", false, true, http.StatusNotFound},
		{"disabled account", true, false, http.StatusNotFound},
		{"opted in active account", true, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, mock, state := testServerWithState(t)
			defer mock.Close()
			state.profileFederationEnabled = tc.enabled
			state.accountActive = tc.active
			if err := api.WithInternalToken(strings.Repeat("s", 40)); err != nil { t.Fatal(err) }
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/actors/tester", nil)
			req.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 40))
			rec := httptest.NewRecorder()
			api.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want { t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String()) }
			if tc.want == http.StatusOK {
				var response struct {
					OK   bool `json:"ok"`
					Data struct {
						UserID            string `json:"user_id"`
						Handle            string `json:"handle"`
						DisplayName       string `json:"display_name"`
						Summary           string `json:"summary"`
						FederationEnabled bool   `json:"federation_enabled"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode Federation actor envelope: %v", err)
				}
				if !response.OK || response.Data.UserID != "new_user_1" || response.Data.Handle != "tester" ||
					!response.Data.FederationEnabled || strings.Contains(rec.Body.String(), "email") {
					t.Fatalf("unexpected actor response: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestUpdateFederationPreferenceRequiresAuthenticatedProfile(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	req := httptest.NewRequest(http.MethodPut, "/v1/me/federation", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Authorization", "Bearer valid-session")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
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

func TestCreatePostUsesVerifiedIdentityAndIdempotency(t *testing.T) {
	api, mock, state := testServerWithState(t)
	defer mock.Close()
	create := func(content string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/posts", strings.NewReader(`{"content":"`+content+`","visibility":"public"}`))
		req.Header.Set("Authorization", "Bearer valid-session")
		req.Header.Set("Idempotency-Key", "post-create-key-001")
		rec := httptest.NewRecorder()
		api.Handler().ServeHTTP(rec, req)
		return rec
	}
	first := create("Hello Testagram")
	if first.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", first.Code, first.Body.String())
	}
	if !state.transactionCommitted || len(state.documents) != 2 {
		t.Fatalf("post and durable outbox event were not committed atomically: committed=%v docs=%d", state.transactionCommitted, len(state.documents))
	}
	var outbox map[string]any
	for _, raw := range state.documents {
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		if doc["event_type"] == "testagram.post.published" {
			outbox = doc
		}
	}
	if outbox == nil || outbox["actor_id"] != "new_user_1" || outbox["federation_eligible"] != true {
		t.Fatalf("public post outbox event missing or invalid: %#v", outbox)
	}
	payload, ok := outbox["payload"].(map[string]any)
	if !ok || payload["id"] != outbox["object_id"] || payload["author_id"] != "new_user_1" || payload["content"] != "Hello Testagram" {
		t.Fatalf("outbox event is missing the authorized post snapshot: %#v", outbox["payload"])
	}
	var firstEnvelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstEnvelope); err != nil {
		t.Fatal(err)
	}
	if firstEnvelope.Data["author_id"] != "new_user_1" || firstEnvelope.Data["body"] != "Hello Testagram" || firstEnvelope.Data["visibility"] != "public" {
		t.Fatalf("post not bound to authenticated identity or payload: %#v", firstEnvelope.Data)
	}
	second := create("Hello Testagram")
	if second.Code != http.StatusOK {
		t.Fatalf("idempotent replay status=%d body=%s", second.Code, second.Body.String())
	}
	var secondEnvelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondEnvelope); err != nil {
		t.Fatal(err)
	}
	if secondEnvelope.Data["$id"] != firstEnvelope.Data["$id"] {
		t.Fatalf("idempotent replay changed post ID: %v != %v", secondEnvelope.Data["$id"], firstEnvelope.Data["$id"])
	}
	conflict := create("Different content")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestCreatePostRejectsMissingIdempotencyAndInvalidVisibility(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	for _, test := range []struct{ name, key, payload string }{
		{"missing key", "", `{"content":"Hello","visibility":"public"}`},
		{"invalid visibility", "post-create-key-002", `{"content":"Hello","visibility":"world"}`},
		{"unknown field", "post-create-key-003", `{"content":"Hello","visibility":"public","author_id":"attacker"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/posts", strings.NewReader(test.payload))
			req.Header.Set("Authorization", "Bearer valid-session")
			if test.key != "" {
				req.Header.Set("Idempotency-Key", test.key)
			}
			rec := httptest.NewRecorder()
			api.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
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
	reserve := httptest.NewRequest(http.MethodPost, "/v1/media/uploads/reservations", strings.NewReader(`{"bytes":1024,"content_type":"image/jpeg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scope":"story_media"}`))
	reserve.Header.Set("Authorization", "Bearer valid-session")
	reserve.Header.Set("Idempotency-Key", "upload-key-123")
	reserveRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(reserveRec, reserve)
	if reserveRec.Code != http.StatusCreated {
		t.Fatalf("reserve status=%d body=%s", reserveRec.Code, reserveRec.Body.String())
	}
	if media.reserveInput.OwnerID != "new_user_1" || media.reserveInput.IdempotencyKey != "upload-key-123" || media.reserveInput.Scope != "story_media" {
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
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type,idempotency-key,x-request-id,x-testagram-api-version")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.testagram.example" {
		t.Fatalf("preflight rejected: %d %s", rec.Code, rec.Body.String())
	}
	allowed := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	exposed := strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers"))
	if !strings.Contains(allowed, "x-request-id") || !strings.Contains(allowed, "x-testagram-api-version") ||
		!strings.Contains(exposed, "x-request-id") || !strings.Contains(exposed, "x-testagram-api-version") {
		t.Fatalf("versioned API headers were not allowed/exposed: allow=%q expose=%q", allowed, exposed)
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

type fakeHomeFeed struct {
	viewer string
	limit  int
	cursor string
	err    error
}

func (f *fakeHomeFeed) Home(_ context.Context, viewer string, limit int, cursor string) (freshfeed.Result, error) {
	f.viewer, f.limit, f.cursor = viewer, limit, cursor
	return freshfeed.Result{Posts: []map[string]any{{"id": "post_1"}}, NextCursor: "next", HasMore: true, RankingMode: "chronological"}, f.err
}

func (f *fakeHomeFeed) Ready(context.Context) error { return f.err }

func TestHomeFeedRequiresVerifiedAppwriteIdentityAndUsesBoundedPagination(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	feed := &fakeHomeFeed{}
	api.WithFeed(feed)

	unauthenticated := httptest.NewRequest(http.MethodGet, "/v1/feed/home", nil)
	unauthenticatedRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(unauthenticatedRec, unauthenticated)
	if unauthenticatedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated feed status=%d", unauthenticatedRec.Code)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/v1/feed/home?limit=51", nil)
	invalid.Header.Set("Authorization", "Bearer valid-session")
	invalidRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d", invalidRec.Code)
	}
	invalidCursor := httptest.NewRequest(http.MethodGet, "/v1/feed/home?before=not-base64", nil)
	invalidCursor.Header.Set("Authorization", "Bearer valid-session")
	invalidCursorRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(invalidCursorRec, invalidCursor)
	if invalidCursorRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d", invalidCursorRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/feed/home?limit=12&before=Y3Vyc29yXzE", nil)
	req.Header.Set("Authorization", "Bearer valid-session")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed status=%d body=%s", rec.Code, rec.Body.String())
	}
	if feed.viewer != "new_user_1" || feed.limit != 12 || feed.cursor != "Y3Vyc29yXzE" {
		t.Fatalf("feed received unverified or incorrect inputs: %#v", feed)
	}
	if !strings.Contains(rec.Body.String(), "post_1") || !strings.Contains(rec.Body.String(), "chronological") {
		t.Fatalf("feed response shape mismatch: %s", rec.Body.String())
	}
}

func TestLegalConsentRequiresAllPoliciesAndAdultBirthDate(t *testing.T) {
	api, mock, state := testServerWithState(t)
	defer mock.Close()
	missing := httptest.NewRequest(http.MethodPut, "/v1/me/legal", strings.NewReader(`{"birth_date":"1990-01-01","policy_version":"2026-09","terms_accepted":true}`))
	missing.Header.Set("Authorization", "Bearer valid-session")
	missingRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(missingRec, missing)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("missing consent flags status=%d body=%s", missingRec.Code, missingRec.Body.String())
	}
	minor := httptest.NewRequest(http.MethodPut, "/v1/me/legal", strings.NewReader(`{"birth_date":"2015-01-01","policy_version":"2026-09","terms_accepted":true,"privacy_accepted":true,"content_policy_accepted":true,"age_confirmed":true}`))
	minor.Header.Set("Authorization", "Bearer valid-session")
	minorRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(minorRec, minor)
	if minorRec.Code != http.StatusForbidden {
		t.Fatalf("minor consent status=%d body=%s", minorRec.Code, minorRec.Body.String())
	}
	if len(state.profileUpdates) != 0 {
		t.Fatalf("invalid consent changed profile: %#v", state.profileUpdates)
	}
}

func TestLegalConsentPersistsVersionedAcceptanceOnAuthenticatedProfile(t *testing.T) {
	api, mock, state := testServerWithState(t)
	defer mock.Close()
	req := httptest.NewRequest(http.MethodPut, "/v1/me/legal", strings.NewReader(`{"birth_date":"1990-01-01","policy_version":"2026-09","terms_accepted":true,"privacy_accepted":true,"content_policy_accepted":true,"age_confirmed":true}`))
	req.Header.Set("Authorization", "Bearer valid-session")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accepted":true`) {
		t.Fatalf("legal consent status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated := state.profileUpdates["new_user_1"]
	for _, key := range []string{"birth_date", "legal_terms_accepted_at", "legal_privacy_accepted_at", "legal_content_policy_accepted_at", "legal_age_confirmed_at", "legal_policy_version"} {
		if updated[key] == nil || updated[key] == "" {
			t.Fatalf("legal consent field %q was not persisted: %#v", key, updated)
		}
	}
	if updated["birth_date"] != "1990-01-01" || updated["legal_policy_version"] != "2026-09" {
		t.Fatalf("unexpected legal consent values: %#v", updated)
	}
}
func TestFederationOutboxRequiresInternalTokenAndReturnsBoundedPage(t *testing.T) {
	api, mock, state := testServerWithState(t)
	defer mock.Close()
	if err := api.WithInternalToken("too-short"); err == nil {
		t.Fatal("short Federation service token accepted")
	}
	if err := api.WithInternalToken(strings.Repeat("t", 32)); err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/outbox", nil)
	unauthorizedRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing service token status=%d", unauthorizedRec.Code)
	}
	invalid := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/outbox?limit=100", nil)
	invalid.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	invalidRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d", invalidRec.Code)
	}
	valid := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/outbox?limit=10", nil)
	valid.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	validRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(validRec, valid)
	if validRec.Code != http.StatusOK || !strings.Contains(validRec.Body.String(), `"events":[]`) {
		t.Fatalf("valid outbox request status=%d body=%s", validRec.Code, validRec.Body.String())
	}
	joinedQueries := strings.Join(state.outboxQueries, " ")
	if !strings.Contains(joinedQueries, `"column":"federation_eligible"`) || !strings.Contains(joinedQueries, `"values":[true]`) || !strings.Contains(joinedQueries, `testagram.post.published`) {
		t.Fatalf("outbox query did not restrict results to eligible public post events: %s", joinedQueries)
	}
}

func TestFederationOutboxIsNotMountedWithoutSecret(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/outbox", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("private route should not exist without token, status=%d", rec.Code)
	}
}

func TestReadyzFailsWhenAppwriteAuthIsUnavailable(t *testing.T) {
	api, mock, state := testServerWithState(t)
	defer mock.Close()
	state.authUnavailable = true
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Appwrite Auth service is not ready") {
		t.Fatalf("login readiness must fail when Appwrite Auth is unreachable; status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReadyzDoesNotDependOnFeedOrNeon(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	api.WithNeon(nil, true)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authentication":"available"`) {
		t.Fatalf("Appwrite login readiness must not depend on feed or Neon; status=%d body=%s", rec.Code, rec.Body.String())
	}

	feed := &fakeHomeFeed{err: errors.New("Algorithm unavailable")}
	api.WithFeed(feed)
	calls := 0
	api.WithNeon(fakeNeonHealth{err: errors.New("Neon unavailable"), calls: &calls}, true)
	second := httptest.NewRecorder()
	api.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if second.Code != http.StatusOK {
		t.Fatalf("login readiness must remain available during feed/Neon outages; status=%d body=%s", second.Code, second.Body.String())
	}
	if calls != 0 {
		t.Fatalf("login readiness unexpectedly probed Neon %d time(s)", calls)
	}
}


type fakeNeonHealth struct {
	err   error
	calls *int
}

func (f fakeNeonHealth) Ping(context.Context) error {
	if f.calls != nil {
		(*f.calls)++
	}
	return f.err
}

func TestPlatformReadyzFailsWhenRequiredNeonIsMissing(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	api.WithNeon(nil, true)
	service := httptest.NewRecorder()
	api.Handler().ServeHTTP(service, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if service.Code != http.StatusOK {
		t.Fatalf("Appwrite login readiness should not require Neon; status=%d body=%s", service.Code, service.Body.String())
	}
	platform := httptest.NewRecorder()
	api.Handler().ServeHTTP(platform, httptest.NewRequest(http.MethodGet, "/readyz/platform", nil))
	if platform.Code != http.StatusServiceUnavailable || !strings.Contains(platform.Body.String(), "Neon is not configured") {
		t.Fatalf("full platform readiness should require Neon; status=%d body=%s", platform.Code, platform.Body.String())
	}
}

func TestPlatformReadyzChecksNeonWithoutBlockingLogin(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	api.WithFeed(&fakeHomeFeed{})
	calls := 0
	api.WithNeon(fakeNeonHealth{err: errors.New("database unavailable"), calls: &calls}, true)
	service := httptest.NewRecorder()
	api.Handler().ServeHTTP(service, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if service.Code != http.StatusOK {
		t.Fatalf("login readiness should not wait for Neon; status=%d body=%s", service.Code, service.Body.String())
	}
	if calls != 0 {
		t.Fatalf("login readiness unexpectedly probed Neon %d time(s)", calls)
	}

	platform := httptest.NewRecorder()
	api.Handler().ServeHTTP(platform, httptest.NewRequest(http.MethodGet, "/readyz/platform", nil))
	if platform.Code != http.StatusServiceUnavailable || !strings.Contains(platform.Body.String(), "Neon PostgreSQL connectivity is not ready") {
		t.Fatalf("platform readiness must report Neon outage; status=%d body=%s", platform.Code, platform.Body.String())
	}
	if calls != 1 {
		t.Fatalf("platform readiness should probe Neon once, got %d", calls)
	}

	api.WithNeon(fakeNeonHealth{calls: &calls}, true)
	platform = httptest.NewRecorder()
	api.Handler().ServeHTTP(platform, httptest.NewRequest(http.MethodGet, "/readyz/platform", nil))
	if platform.Code != http.StatusServiceUnavailable || !strings.Contains(platform.Body.String(), "canonical social repositories are not integrated") {
		t.Fatalf("platform readiness must remain blocked until Neon repositories are integrated; status=%d body=%s", platform.Code, platform.Body.String())
	}
	if calls != 2 {
		t.Fatalf("platform readiness should probe Neon a second time, got %d", calls)
	}
}


func TestReadyzDoesNotClaimPostPublishingWhenModerationIsDisabled(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	api.WithFeed(&fakeHomeFeed{})
	api.WithModeration(nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"moderation":"not_configured"`) ||
		!strings.Contains(rec.Body.String(), `"post_write":"disabled_moderation_unconfigured"`) {
		t.Fatalf("readiness overstated publishing capability: %s", rec.Body.String())
	}
}

func TestFederationOutboxReturnsCursorForFinalPage(t *testing.T) {
	api, mock, state := testServerWithState(t)
	defer mock.Close()
	const token = "federation-internal-token-with-at-least-32-characters"
	if err := api.WithInternalToken(token); err != nil {
		t.Fatal(err)
	}
	const eventID = "evt_12345678901234567890123456789012"
	state.outboxDocuments = []json.RawMessage{json.RawMessage(`{"$id":"` + eventID + `","event_type":"testagram.post.published","actor_id":"user_123","object_id":"post_123","federation_eligible":true,"created_at":"2026-11-07T20:00:00Z","payload":{"id":"post_123"}}`)}
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/outbox?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Events     []json.RawMessage `json:"events"`
			NextCursor string            `json:"next_cursor"`
			HasMore    bool              `json:"has_more"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode outbox response: %v", err)
	}
	wantCursor := base64.RawURLEncoding.EncodeToString([]byte(eventID))
	if !response.OK || len(response.Data.Events) != 1 || response.Data.HasMore || response.Data.NextCursor != wantCursor {
		t.Fatalf("outbox final-page cursor response = %+v, want cursor %q and has_more=false", response, wantCursor)
	}
}



func TestAPIContractHeaders(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "client-request-123")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("X-Request-ID"); got != "client-request-123" {
		t.Fatalf("X-Request-ID = %q, want client-request-123", got)
	}
	if got := response.Header().Get("X-Testagram-API-Version"); got != "1" {
		t.Fatalf("X-Testagram-API-Version = %q, want 1", got)
	}
}

func TestAPIContractHeadersReplaceUnsafeRequestID(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "bad\r\ninjected")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)

	got := response.Header().Get("X-Request-ID")
	if !validRequestID(got) || got == "bad\r\ninjected" {
		t.Fatalf("unsafe request ID was not replaced: %q", got)
	}
}
