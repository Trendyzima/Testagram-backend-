package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeIdentityProvisioningTokenAndRedeemIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL identity provisioning integration")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var applicationID string
	if err := db.QueryRow(ctx, "SELECT id::text FROM testagram_identity.identity_applications WHERE email='first@example.test' AND status='approved'").Scan(&applicationID); err != nil {
		t.Fatalf("load approved identity fixture: %v", err)
	}
	applicationToken := strings.Repeat("a", 43)
	if _, err := db.Exec(ctx, "UPDATE testagram_identity.identity_applications SET status_access_token_sha256=$2,status_access_token_expires_at=now()+interval '7 days' WHERE id=$1::uuid", applicationID, authTokenHash(applicationToken)); err != nil {
		t.Fatal(err)
	}

	appwriteUserID := "appwrite-user-ci"
	emailVerified := false
	appwriteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Appwrite-Project") != "identity-ci-project" || r.Header.Get("X-Appwrite-JWT") != "identity-ci-jwt" {
			http.Error(w, "invalid test credentials", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"$id": appwriteUserID, "email": "first@example.test",
			"emailVerification": emailVerified, "status": true,
		})
	}))
	defer appwriteServer.Close()
	t.Setenv("APPWRITE_ENDPOINT", appwriteServer.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", "identity-ci-project")

	svc := &service{db: db, upstream: appwriteServer.Client()}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/provisioning-token", nil)
	tokenRequest.SetPathValue("application_id", applicationID)
	tokenRequest.Header.Set("X-Application-Token", applicationToken)
	tokenResponse := httptest.NewRecorder()
	svc.identityProvisioningToken(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("provisioning token status=%d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var grant map[string]any
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	approvalToken, _ := grant["approval_token"].(string)
	jti, _ := grant["jti"].(string)
	if len(approvalToken) < 32 || len(jti) < 32 {
		t.Fatalf("short-lived approval grant was incomplete: %#v", grant)
	}

	redeem := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/redeem", nil)
		req.SetPathValue("application_id", applicationID)
		req.Header.Set("X-Application-Token", applicationToken)
		req.Header.Set("X-Identity-Approval-Token", approvalToken)
		req.Header.Set("X-Identity-Approval-JTI", jti)
		req.Header.Set("Authorization", "Bearer identity-ci-jwt")
		recorder := httptest.NewRecorder()
		svc.identityProvisioningRedeem(recorder, req)
		return recorder
	}

	first := redeem()
	if first.Code != http.StatusOK {
		t.Fatalf("first redemption status=%d body=%s", first.Code, first.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	canonicalID, _ := result["canonical_user_id"].(string)
	if canonicalID == "" || result["email_verification_required"] != true {
		t.Fatalf("unexpected provisioning result: %#v", result)
	}

	var accountCount, mappingCount, completedCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_local.accounts WHERE user_id=$1::uuid AND username='ci_first'", canonicalID).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1 AND canonical_user_id=$2::uuid AND email_verified=FALSE", appwriteUserID, canonicalID).Scan(&mappingCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.registration_provisioning WHERE application_id=$1::uuid AND status='completed' AND appwrite_user_id=$2 AND canonical_user_id=$3::uuid", applicationID, appwriteUserID, canonicalID).Scan(&completedCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 || mappingCount != 1 || completedCount != 1 {
		t.Fatalf("canonical provisioning invariants: account=%d mapping=%d saga=%d", accountCount, mappingCount, completedCount)
	}

	// Email verification is confirmed by Appwrite at the authenticated API
	// boundary, then synchronized to the canonical identity application.
	emailVerified = true
	authRequest := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	canonicalID, authStatus, authErr := svc.authenticatedAppwriteUser(authRequest, "identity-ci-jwt")
	if authErr != nil || authStatus != http.StatusOK || canonicalID != canonicalUserIDFromResult(t, result) {
		t.Fatalf("verified Appwrite login canonical=%q status=%d err=%v", canonicalID, authStatus, authErr)
	}
	var applicationStatus string
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID).Scan(&applicationStatus); err != nil {
		t.Fatal(err)
	}
	if applicationStatus != "verified" {
		t.Fatalf("application status after verified Appwrite login=%q, want verified", applicationStatus)
	}

	replay := redeem()
	if replay.Code != http.StatusOK {
		t.Fatalf("idempotent redemption status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayResult map[string]any
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult["idempotent_replay"] != true || replayResult["canonical_user_id"] != canonicalID {
		t.Fatalf("redemption retry did not return the same canonical account: %#v", replayResult)
	}

	appwriteUserID = "different-appwrite-user"
	replay = redeem()
	if replay.Code != http.StatusConflict {
		t.Fatalf("different Appwrite identity redemption status=%d, want 409 body=%s", replay.Code, replay.Body.String())
	}
}


func canonicalUserIDFromResult(t *testing.T, result map[string]any) string {
	t.Helper()
	value, _ := result["canonical_user_id"].(string)
	return value
}
