package neondb

import (
	"strings"
	"testing"
)

func TestValidateDSNRequiresTLSForRemoteDatabase(t *testing.T) {
	_, err := validateDSN("postgresql://user:pass@ep-example.neon.tech/testagram")
	if err == nil || !strings.Contains(err.Error(), "sslmode") { t.Fatalf("expected TLS requirement, got %v", err) }
}
func TestValidateDSNAcceptsNeonTLSAndPooler(t *testing.T) {
	got, err := validateDSN("postgresql://user:pass@ep-example-pooler.neon.tech/testagram?sslmode=require")
	if err != nil { t.Fatal(err) }
	if !strings.Contains(got, "sslmode=require") || !strings.Contains(got, "connect_timeout=5") { t.Fatalf("expected TLS and bounded timeout: %s", got) }
}
func TestValidateDSNAllowsLocalDevelopment(t *testing.T) {
	if _, err := validateDSN("postgres://user:pass@localhost:5432/testagram"); err != nil { t.Fatal(err) }
}
func TestValidateDSNRejectsInvalidURLs(t *testing.T) {
	for _, dsn := range []string{"postgres://localhost/testagram", "postgres://user:pass@ep-example.neon.tech/testagram?sslmode=require#fragment", "mysql://user:pass@localhost/testagram"} {
		if _, err := validateDSN(dsn); err == nil { t.Fatalf("expected invalid DSN to be rejected: %s", dsn) }
	}
}
