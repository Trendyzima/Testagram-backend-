package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEdgeRecoveryReadinessRequiresNativeAuthPostgresSchema(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL recovery-readiness integration")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("r", 48))
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	handler := (&service{db: db, edgeRecoveryReadinessToken: token}).routes()
	probe := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/internal/edge/recovery-ready", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	ready := probe()
	if ready.Code != http.StatusOK {
		t.Fatalf("fully migrated PostgreSQL auth schema should be ready: status=%d body=%s", ready.Code, ready.Body.String())
	}

	// Simulate a PostgreSQL server that accepts connections while the native
	// session relation is missing. Recovery must not tell Flutter to retry yet.
	if _, err := db.Exec(ctx, "ALTER TABLE testagram_auth.sessions RENAME TO sessions_recovery_probe"); err != nil {
		t.Fatalf("hide native session relation for negative readiness probe: %v", err)
	}
	renamed := true
	t.Cleanup(func() {
		if renamed {
			_, _ = db.Exec(context.Background(), "ALTER TABLE testagram_auth.sessions_recovery_probe RENAME TO sessions")
		}
	})
	notReady := probe()
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing native session relation must keep application unready: status=%d body=%s", notReady.Code, notReady.Body.String())
	}

	if _, err := db.Exec(ctx, "ALTER TABLE testagram_auth.sessions_recovery_probe RENAME TO sessions"); err != nil {
		t.Fatalf("restore native session relation: %v", err)
	}
	renamed = false
	readyAgain := probe()
	if readyAgain.Code != http.StatusOK {
		t.Fatalf("application should become ready after native session schema is restored: status=%d body=%s", readyAgain.Code, readyAgain.Body.String())
	}
}
