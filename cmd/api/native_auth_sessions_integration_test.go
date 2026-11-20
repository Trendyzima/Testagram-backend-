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

func TestNativeAuthSessionRotationReplayAndRevocation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native session lifecycle integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("native-auth-integration-secret-", 2))
	t.Setenv("TESTAGRAM_AUTH_PROVIDER", "native")

	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	email := "native-session-" + suffix + "@example.test"
	username := "ns_" + suffix[len(suffix)-16:]
	password := "NativeSession-Integration-Password-2026!"
	passwordHash, err := authPasswordHash(password)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES($1::uuid,$2,$3,$4,now())
	`, userID, email, username, passwordHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES($1::uuid,$2,'Native Session Integration')
	`, userID, username); err != nil {
		t.Fatal(err)
	}

	router := (&service{db: db}).routes()
	request := func(method, path string, payload any, bearer string) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.RemoteAddr = "192.0.2.181:4567"
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	login := func() nativeSessionTokens {
		t.Helper()
		rec := request(http.MethodPost, "/api/auth/login", map[string]string{
			"email": email, "password": password,
		}, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("native login status=%d body=%s", rec.Code, rec.Body.String())
		}
		var envelope struct {
			OK   bool                `json:"ok"`
			Data nativeSessionTokens `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.OK || envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" ||
			envelope.Data.TokenType != "Bearer" || envelope.Data.ExpiresIn != 900 {
			t.Fatalf("native login returned incomplete session: %#v", envelope)
		}
		return envelope.Data
	}
	refresh := func(token string) *httptest.ResponseRecorder {
		return request(http.MethodPost, "/api/auth/refresh", map[string]string{"refresh_token": token}, "")
	}
	profile := func(access string) *httptest.ResponseRecorder {
		return request(http.MethodGet, "/api/me/profile", nil, access)
	}

	// Rotation invalidates the old access token. Reusing the old refresh token
	// is treated as theft and revokes the entire refresh-token family.
	first := login()
	rotated := refresh(first.RefreshToken)
	if rotated.Code != http.StatusOK {
		t.Fatalf("first refresh rotation status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	var rotatedEnvelope struct {
		OK   bool                `json:"ok"`
		Data nativeSessionTokens `json:"data"`
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedEnvelope); err != nil {
		t.Fatal(err)
	}
	if !rotatedEnvelope.OK || rotatedEnvelope.Data.RefreshToken == "" ||
		rotatedEnvelope.Data.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh token was not rotated: %#v", rotatedEnvelope)
	}
	if rec := profile(first.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old access token survived rotation: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := profile(rotatedEnvelope.Data.AccessToken); rec.Code != http.StatusOK {
		t.Fatalf("rotated access token failed protected profile read: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := refresh(first.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed refresh token was accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := refresh(rotatedEnvelope.Data.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh family was not revoked after replay: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Single-session logout revokes only the selected session.
	logoutSession := login()
	otherSession := login()
	logout := request(http.MethodPost, "/api/auth/logout", map[string]string{}, logoutSession.AccessToken)
	if logout.Code != http.StatusOK {
		t.Fatalf("single-session logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	if rec := refresh(logoutSession.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out refresh token was accepted: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := profile(otherSession.AccessToken); rec.Code != http.StatusOK {
		t.Fatalf("single-session logout revoked another session: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Logout-all increments token_version and revokes every active session.
	allSessionOne := login()
	allSessionTwo := login()
	logoutAll := request(http.MethodPost, "/api/auth/logout-all", map[string]string{}, allSessionOne.AccessToken)
	if logoutAll.Code != http.StatusOK {
		t.Fatalf("logout-all status=%d body=%s", logoutAll.Code, logoutAll.Body.String())
	}
	for label, token := range map[string]string{
		"logout-all caller":          allSessionOne.RefreshToken,
		"other active session":       allSessionTwo.RefreshToken,
		"pre-existing other session": otherSession.RefreshToken,
	} {
		if rec := refresh(token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s survived logout-all: status=%d body=%s", label, rec.Code, rec.Body.String())
		}
	}
	if rec := profile(allSessionTwo.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("logout-all left another access token active: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
