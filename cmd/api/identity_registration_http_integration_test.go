package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeIdentityApplicationHTTPJourneyIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native identity HTTP journey integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Setenv("TESTAGRAM_TERMS_VERSION", "terms-ci-v1")
	t.Setenv("TESTAGRAM_PRIVACY_VERSION", "privacy-ci-v1")

	// This fixture exercises the full application path after the WebAuthn release capability is accepted; it does not implement or test the ceremony itself.
	svc := &service{db: db, identityEvidenceMedia: &identityHTTPDataCentreMedia{}, identityReviewWebAuthnReady: true}
	router := svc.routes()
	idempotencyKey, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	username := "u" + suffix[len(suffix)-12:]
	email := "http-journey-" + suffix + "@example.test"
	payload := map[string]any{
		"email": email, "username": username, "display_name": "HTTP Journey Applicant",
		"adult_confirmed": true, "terms_accepted": true, "privacy_accepted": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.RemoteAddr = "192.0.2.41:4567"
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	headers := map[string]string{"Content-Type": "application/json", "Idempotency-Key": idempotencyKey}
	first := call(http.MethodPost, "/api/identity/applications", body, headers)
	if first.Code != http.StatusAccepted {
		t.Fatalf("application create status=%d body=%s", first.Code, first.Body.String())
	}
	var created struct {
		ApplicationID    string `json:"application_id"`
		ApplicationToken string `json:"application_token"`
		SessionID        string `json:"verification_session_id"`
		SessionToken     string `json:"verification_session_token"`
		Status           string `json:"status"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ApplicationID == "" || len(created.ApplicationToken) < 32 || created.SessionID == "" ||
		len(created.SessionToken) < 32 || created.Status != "verification_pending" {
		t.Fatalf("application intake returned incomplete capability/state: %#v", created)
	}

	replay := call(http.MethodPost, "/api/identity/applications", body, headers)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("application recovery status=%d body=%s", replay.Code, replay.Body.String())
	}
	var recovered struct {
		ApplicationID    string `json:"application_id"`
		ApplicationToken string `json:"application_token"`
		SessionID        string `json:"verification_session_id"`
		SessionToken     string `json:"verification_session_token"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.ApplicationID != created.ApplicationID || recovered.ApplicationToken != created.ApplicationToken ||
		recovered.SessionID != created.SessionID || recovered.SessionToken != created.SessionToken {
		t.Fatalf("idempotent recovery changed application/session capabilities: first=%#v replay=%#v", created, recovered)
	}

	changed := map[string]any{
		"email": email, "username": username, "display_name": "Different Applicant",
		"adult_confirmed": true, "terms_accepted": true, "privacy_accepted": true,
	}
	changedBody, _ := json.Marshal(changed)
	conflict := call(http.MethodPost, "/api/identity/applications", changedBody, headers)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("changed-payload idempotency status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	status := call(http.MethodGet, "/api/identity/applications/"+created.ApplicationID, nil,
		map[string]string{"X-Application-Token": created.ApplicationToken})
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "verification_pending") {
		t.Fatalf("application status lookup status=%d body=%s", status.Code, status.Body.String())
	}
	unauthorized := call(http.MethodGet, "/api/identity/applications/"+created.ApplicationID, nil,
		map[string]string{"X-Application-Token": strings.Repeat("x", 43)})
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("application status leaked without capability: %d %s", unauthorized.Code, unauthorized.Body.String())
	}

	var applicationCount, sessionCount, consentCount, nativeIdentityCount, provisioningCount int
	checks := []struct {
		query  string
		args   []any
		target *int
	}{
		{`SELECT count(*) FROM testagram_identity.identity_applications WHERE id=$1::uuid AND submission_key_sha256=$2`, []any{created.ApplicationID, authTokenHash(idempotencyKey)}, &applicationCount},
		{`SELECT count(*) FROM testagram_identity.verification_sessions WHERE id=$1::uuid AND application_id=$2::uuid AND purpose='age_assurance'`, []any{created.SessionID, created.ApplicationID}, &sessionCount},
		{`SELECT count(*) FROM testagram_identity.legal_consent_audit WHERE application_id=$1::uuid`, []any{created.ApplicationID}, &consentCount},
		{`SELECT count(*) FROM testagram_auth.identities WHERE email=$1`, []any{email}, &nativeIdentityCount},
		{`SELECT count(*) FROM testagram_identity.native_registration_provisioning WHERE application_id=$1::uuid`, []any{created.ApplicationID}, &provisioningCount},
	}
	for _, check := range checks {
		if err := db.QueryRow(ctx, check.query, check.args...).Scan(check.target); err != nil {
			t.Fatal(err)
		}
	}
	if applicationCount != 1 || sessionCount != 1 || consentCount != 3 || nativeIdentityCount != 0 || provisioningCount != 0 {
		t.Fatalf("intake invariants applications=%d sessions=%d consents=%d native_identities=%d provisioning=%d",
			applicationCount, sessionCount, consentCount, nativeIdentityCount, provisioningCount)
	}

	// Simulate a committed evidence submission whose HTTP response was lost.
	evidenceUUID := ""
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&evidenceUUID); err != nil {
		t.Fatal(err)
	}
	digest := bytes.Repeat([]byte{0x42}, 32)
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.verification_evidence
		  (id,session_id,object_key,content_type,size_bytes,sha256,status,uploaded_at,verified_at)
		VALUES($1::uuid,$2::uuid,$3,'application/pdf',9,$4,'verified',now(),now())
	`, evidenceUUID, created.SessionID, "identity/"+created.SessionID+"/evidence.pdf", digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_identity.verification_sessions
		SET status='manual_review',evidence_reference=$2,evidence_fingerprint_sha256=$3,
		    submitted_at=now(),updated_at=now()
		WHERE id=$1::uuid
	`, created.SessionID, "identity-evidence:"+evidenceUUID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET status='under_review',evidence_reference=$2,evidence_fingerprint_sha256=$3,updated_at=now()
		WHERE id=$1::uuid
	`, created.ApplicationID, "identity-evidence:"+evidenceUUID, digest); err != nil {
		t.Fatal(err)
	}

	var eventCountBefore int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.verification_events WHERE session_id=$1::uuid", created.SessionID).Scan(&eventCountBefore); err != nil {
		t.Fatal(err)
	}
	submitBody, _ := json.Marshal(map[string]string{"evidence_id": evidenceUUID})
	submitReplay := call(http.MethodPost, "/api/identity/sessions/"+created.SessionID+"/evidence/submit", submitBody,
		map[string]string{"Content-Type": "application/json", "X-Verification-Session-Token": created.SessionToken})
	if submitReplay.Code != http.StatusAccepted || !strings.Contains(submitReplay.Body.String(), `"idempotent_replay":true`) {
		t.Fatalf("evidence submission replay status=%d body=%s", submitReplay.Code, submitReplay.Body.String())
	}
	var eventCountAfter int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.verification_events WHERE session_id=$1::uuid", created.SessionID).Scan(&eventCountAfter); err != nil {
		t.Fatal(err)
	}
	if eventCountAfter != eventCountBefore {
		t.Fatalf("evidence replay duplicated audit events: before=%d after=%d", eventCountBefore, eventCountAfter)
	}
}
