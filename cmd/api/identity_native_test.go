package main

import (
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

func TestIdentityEvidenceExtensionAllowlist(t *testing.T) {
	cases := []struct {
		contentType string
		want string
		ok bool
	}{
		{"image/jpeg", ".jpg", true},
		{"image/png", ".png", true},
		{"image/webp", ".webp", true},
		{"application/pdf", ".pdf", true},
		{"image/svg+xml", "", false},
		{"text/html", "", false},
		{"application/zip", "", false},
	}
	for _, tc := range cases {
		got, ok := identityEvidenceExtension(tc.contentType)
		if got != tc.want || ok != tc.ok {
			t.Errorf("identityEvidenceExtension(%q) = (%q,%v), want (%q,%v)", tc.contentType, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIdentityRequestTokenShapes(t *testing.T) {
	if !identityApplicationIDPattern.MatchString("123e4567-e89b-42d3-a456-426614174000") {
		t.Fatal("valid UUID-shaped identifier rejected")
	}
	if identityApplicationIDPattern.MatchString("not-a-uuid") {
		t.Fatal("invalid identifier accepted")
	}
	if !identityIdempotencyKeyPattern.MatchString("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ") {
		t.Fatal("valid URL-safe idempotency key rejected")
	}
	if identityIdempotencyKeyPattern.MatchString("short") {
		t.Fatal("short idempotency key accepted")
	}
}

func TestIdentityEvidenceContentTypeSniffing(t *testing.T) {
	cases := []struct {
		name string
		contentType string
		payload []byte
		want bool
	}{
		{"jpeg", "image/jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), true},
		{"png", "image/png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), true},
		{"webp", "image/webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), true},
		{"pdf", "application/pdf", []byte("%PDF-1.7\n"), true},
		{"html masquerading as jpeg", "image/jpeg", []byte("<html><body>not an image</body></html>"), false},
		{"jpeg masquerading as pdf", "application/pdf", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), false},
		{"empty", "image/png", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityEvidenceMatchesContentType(tc.payload, tc.contentType); got != tc.want {
				t.Fatalf("identityEvidenceMatchesContentType() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNativeIdentityApplicationStatusUsesVerificationSessions(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native identity status integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var applicationID, sessionID string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text, gen_random_uuid()::text").Scan(&applicationID, &sessionID); err != nil {
		t.Fatal(err)
	}
	token := "ci-native-status-token-0123456789-abcdefghijklmnopqrstuvwxyz"
	fingerprint := authTokenHash("native-status-fingerprint:" + applicationID)
	submissionHash := authTokenHash("native-status-submission:" + applicationID)
	sessionTokenHash := authTokenHash("native-status-session:" + sessionID)
	username := "status_" + strings.ReplaceAll(applicationID, "-", "")[:12]
	email := fmt.Sprintf("%s@example.test", username)

	if _, err := pool.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id,email,username,display_name,adult_confirmed_at,evidence_reference,
		   evidence_fingerprint_sha256,terms_version,privacy_version,terms_accepted_at,
		   privacy_accepted_at,submission_key_sha256,status_access_token_sha256,
		   status_access_token_expires_at,status)
		VALUES ($1::uuid,$2,$3,'Native status test',now(),'native-status-reference',
		        $4,$5,$6,now(),now(),$7,$8,now()+interval '7 days','verification_pending')
	`, applicationID, email, username, fingerprint, "terms-v1", "privacy-v1", submissionHash, authTokenHash(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO testagram_identity.verification_sessions
		  (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,
		   evidence_reference,evidence_fingerprint_sha256,assurance_method,assurance_level,
		   expires_at,submitted_at)
		VALUES ($1::uuid,$2::uuid,$3,'age_assurance',18,'manual_review',
		        'native-status-evidence',$4,'manual_review','none',now()+interval '1 day',now())
	`, sessionID, applicationID, sessionTokenHash, fingerprint); err != nil {
		_, _ = pool.Exec(ctx, "DELETE FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM testagram_identity.verification_sessions WHERE id=$1::uuid", sessionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID)
	})

	request := httptest.NewRequest(http.MethodGet, "/api/identity/applications/"+applicationID+"/status", nil)
	request.SetPathValue("application_id", applicationID)
	request.Header.Set("X-Application-Token", token)
	response := httptest.NewRecorder()
	service := &service{db: pool}
	service.identityApplicationStatus(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("identity status returned HTTP %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["verification_session_id"] != sessionID {
		t.Fatalf("verification_session_id=%v, want %s", body["verification_session_id"], sessionID)
	}
	if body["adult_verification_status"] != "manual_review" {
		t.Fatalf("adult_verification_status=%v, want manual_review", body["adult_verification_status"])
	}
	if body["adult_verification_threshold"] != float64(18) {
		t.Fatalf("adult_verification_threshold=%v, want 18", body["adult_verification_threshold"])
	}
}
