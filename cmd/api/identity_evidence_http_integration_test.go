package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type identityHTTPStoredObject struct {
	body []byte
	info virtualstorage.ObjectInfo
}

type identityHTTPFakeProvider struct {
	mu         sync.Mutex
	objects    map[string]identityHTTPStoredObject
	uploadBase string
}

func newIdentityHTTPFakeProvider() *identityHTTPFakeProvider {
	return &identityHTTPFakeProvider{objects: make(map[string]identityHTTPStoredObject)}
}

func identityHTTPObjectKey(bucket, key string) string { return bucket + ":" + key }

func (p *identityHTTPFakeProvider) PresignPut(_ context.Context, bucket, key, contentType string, _ int64, _ time.Duration) (string, error) {
	if p.uploadBase == "" {
		return "", errors.New("fake upload endpoint is not configured")
	}
	u, err := url.Parse(p.uploadBase)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("bucket", bucket)
	q.Set("key", key)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (p *identityHTTPFakeProvider) PresignGet(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return p.uploadBase, nil
}
func (p *identityHTTPFakeProvider) Head(_ context.Context, bucket, key string) (virtualstorage.ObjectInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	object, ok := p.objects[identityHTTPObjectKey(bucket, key)]
	if !ok {
		return virtualstorage.ObjectInfo{}, virtualstorage.ErrNotFound
	}
	return object.info, nil
}
func (p *identityHTTPFakeProvider) Delete(_ context.Context, bucket, key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.objects, identityHTTPObjectKey(bucket, key))
	return nil
}
func (p *identityHTTPFakeProvider) List(_ context.Context, _, _, cursor string, _ int) ([]virtualstorage.ObjectInfo, string, error) {
	return nil, cursor, nil
}
func (p *identityHTTPFakeProvider) SHA256(_ context.Context, bucket, key string, maxBytes int64) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	object, ok := p.objects[identityHTTPObjectKey(bucket, key)]
	if !ok {
		return "", virtualstorage.ErrNotFound
	}
	if int64(len(object.body)) > maxBytes {
		return "", virtualstorage.ErrTooLarge
	}
	digest := sha256.Sum256(object.body)
	return hex.EncodeToString(digest[:]), nil
}
func (p *identityHTTPFakeProvider) CopyFromProvider(_ context.Context, source virtualstorage.Provider, sourceBucket, sourceKey, destinationBucket, destinationKey string, expectedBytes int64, contentType, expectedSHA256 string) error {
	from, ok := source.(*identityHTTPFakeProvider)
	if !ok {
		return errors.New("unsupported fake replica source")
	}
	from.mu.Lock()
	object, exists := from.objects[identityHTTPObjectKey(sourceBucket, sourceKey)]
	from.mu.Unlock()
	if !exists {
		return virtualstorage.ErrNotFound
	}
	if int64(len(object.body)) != expectedBytes || object.info.ContentType != contentType {
		return virtualstorage.ErrObjectMismatch
	}
	digest := sha256.Sum256(object.body)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return virtualstorage.ErrChecksumMismatch
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	copied := append([]byte(nil), object.body...)
	p.objects[identityHTTPObjectKey(destinationBucket, destinationKey)] = identityHTTPStoredObject{
		body: copied,
		info: virtualstorage.ObjectInfo{Key: destinationKey, Size: int64(len(copied)), ContentType: contentType, ModifiedAt: time.Now().UTC()},
	}
	return nil
}

func TestNativeIdentityEvidenceHTTPUploadSubmitAndReplayIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native identity evidence HTTP integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	primary := newIdentityHTTPFakeProvider()
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "PUT required", http.StatusMethodNotAllowed)
			return
		}
		bucket, key := r.URL.Query().Get("bucket"), r.URL.Query().Get("key")
		if bucket == "" || key == "" {
			http.Error(w, "missing object identity", http.StatusBadRequest)
			return
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		primary.mu.Lock()
		primary.objects[identityHTTPObjectKey(bucket, key)] = identityHTTPStoredObject{
			body: append([]byte(nil), body...),
			info: virtualstorage.ObjectInfo{Key: key, Size: int64(len(body)), ContentType: r.Header.Get("Content-Type"), ModifiedAt: time.Now().UTC()},
		}
		primary.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer primaryServer.Close()
	primary.uploadBase = primaryServer.URL
	replicaR2 := newIdentityHTTPFakeProvider()
	replicaB2 := newIdentityHTTPFakeProvider()

	buckets := map[string]string{
		"media-private": "primary-private",
		"media-public": "primary-public",
		identityEvidenceNamespace: "primary-identity-evidence",
	}
	replicaBuckets := func(prefix string) map[string]string {
		return map[string]string{
			"media-private": prefix + "-private",
			"media-public": prefix + "-public",
			identityEvidenceNamespace: prefix + "-identity-evidence",
		}
	}
	replicator, err := virtualstorage.NewReplicator("primary", primary, buckets, []virtualstorage.ReplicaTarget{
		{Name: "r2", Provider: replicaR2, Buckets: replicaBuckets("r2")},
		{Name: "b2", Provider: replicaB2, Buckets: replicaBuckets("b2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := virtualstorage.New(primary, []virtualstorage.Namespace{{
		Name: identityEvidenceNamespace,
		PhysicalBucket: buckets[identityEvidenceNamespace],
		Public: false,
		MaxObjectBytes: maxIdentityEvidenceBytes,
		AllowedTypes: map[string]struct{}{"image/jpeg": {}, "image/png": {}, "image/webp": {}, "application/pdf": {}},
		UploadTTL: 5*time.Minute,
		DownloadTTL: 5*time.Minute,
	}})
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("TESTAGRAM_IDENTITY_REGISTRATION_ENABLED", "true")
	t.Setenv("TESTAGRAM_TERMS_VERSION", "terms-http-v1")
	t.Setenv("TESTAGRAM_PRIVACY_VERSION", "privacy-http-v1")
	svc := &service{db: db, virtualStorage: manager, mediaReplicator: replicator}
	router := svc.routes()

	idempotencyKey, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	username := "e" + suffix[len(suffix)-12:]
	email := "evidence-http-" + suffix + "@example.test"
	payload, err := json.Marshal(map[string]any{
		"email": email, "username": username, "display_name": "Evidence HTTP Applicant",
		"adult_confirmed": true, "terms_accepted": true, "privacy_accepted": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.73:4567"
		for key, value := range headers { req.Header.Set(key, value) }
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	createdResponse := call(http.MethodPost, "/api/identity/applications", payload, map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": idempotencyKey,
	})
	if createdResponse.Code != http.StatusAccepted {
		t.Fatalf("application intake status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		ApplicationID string `json:"application_id"`
		SessionID string `json:"verification_session_id"`
		SessionToken string `json:"verification_session_token"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ApplicationID == "" || created.SessionID == "" || len(created.SessionToken) < 32 {
		t.Fatalf("incomplete application/session capabilities: %#v", created)
	}

	evidenceBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	digest := sha256.Sum256(evidenceBytes)
	reservationBody, _ := json.Marshal(map[string]any{
		"content_type": "image/jpeg", "size_bytes": len(evidenceBytes),
		"sha256": hex.EncodeToString(digest[:]),
	})
	sessionPath := "/api/identity/sessions/" + created.SessionID
	sessionHeaders := map[string]string{
		"Content-Type": "application/json",
		"X-Verification-Session-Token": created.SessionToken,
	}
	reservation := call(http.MethodPost, sessionPath+"/evidence/upload-url", reservationBody, sessionHeaders)
	if reservation.Code != http.StatusCreated {
		t.Fatalf("evidence reservation status=%d body=%s", reservation.Code, reservation.Body.String())
	}
	var reserved struct {
		EvidenceID string `json:"evidence_id"`
		UploadURL string `json:"upload_url"`
	}
	if err := json.Unmarshal(reservation.Body.Bytes(), &reserved); err != nil {
		t.Fatal(err)
	}
	if reserved.EvidenceID == "" || !strings.HasPrefix(reserved.UploadURL, sessionPath+"/evidence/") {
		t.Fatalf("invalid evidence reservation: %#v", reserved)
	}
	uploaded := call(http.MethodPut, reserved.UploadURL, evidenceBytes, map[string]string{
		"Content-Type": "image/jpeg",
		"X-Verification-Session-Token": created.SessionToken,
	})
	if uploaded.Code != http.StatusNoContent {
		t.Fatalf("private evidence upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}

	submitBody, _ := json.Marshal(map[string]string{"evidence_id": reserved.EvidenceID})
	submitPath := sessionPath + "/evidence/submit"
	firstSubmit := call(http.MethodPost, submitPath, submitBody, sessionHeaders)
	if firstSubmit.Code != http.StatusAccepted || !strings.Contains(firstSubmit.Body.String(), `"status":"manual_review"`) {
		t.Fatalf("evidence submission status=%d body=%s", firstSubmit.Code, firstSubmit.Body.String())
	}
	for providerName, provider := range map[string]*identityHTTPFakeProvider{"primary": primary, "r2": replicaR2, "b2": replicaB2} {
		provider.mu.Lock()
		count := len(provider.objects)
		provider.mu.Unlock()
		if count != 1 {
			t.Fatalf("%s provider has %d objects after verified replication; want exactly one", providerName, count)
		}
	}

	var eventCountBefore int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.verification_events WHERE session_id=$1::uuid", created.SessionID).Scan(&eventCountBefore); err != nil {
		t.Fatal(err)
	}
	replayedSubmit := call(http.MethodPost, submitPath, submitBody, sessionHeaders)
	if replayedSubmit.Code != http.StatusAccepted || !strings.Contains(replayedSubmit.Body.String(), `"idempotent_replay":true`) {
		t.Fatalf("evidence replay status=%d body=%s", replayedSubmit.Code, replayedSubmit.Body.String())
	}
	var eventCountAfter int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.verification_events WHERE session_id=$1::uuid", created.SessionID).Scan(&eventCountAfter); err != nil {
		t.Fatal(err)
	}
	if eventCountAfter != eventCountBefore {
		t.Fatalf("evidence replay duplicated audit events: before=%d after=%d", eventCountBefore, eventCountAfter)
	}
	var applicationStatus, sessionStatus, evidenceStatus string
	if err := db.QueryRow(ctx, `
		SELECT a.status,v.status,e.status
		FROM testagram_identity.identity_applications a
		JOIN testagram_identity.verification_sessions v ON v.application_id=a.id
		JOIN testagram_identity.verification_evidence e ON e.session_id=v.id
		WHERE a.id=$1::uuid AND v.id=$2::uuid AND e.id=$3::uuid
	`, created.ApplicationID, created.SessionID, reserved.EvidenceID).Scan(&applicationStatus, &sessionStatus, &evidenceStatus); err != nil {
		t.Fatal(err)
	}
	if applicationStatus != "under_review" || sessionStatus != "manual_review" || evidenceStatus != "verified" {
		t.Fatalf("evidence lifecycle state application=%q session=%q evidence=%q", applicationStatus, sessionStatus, evidenceStatus)
	}
}
