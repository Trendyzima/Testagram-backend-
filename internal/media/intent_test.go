package media

import (
	"regexp"
	"testing"
	"time"
)

func TestIntentFactoryCreatesTenantScopedKeyWithoutProviderURL(t *testing.T) {
	factory, err := NewIntentFactory(IntentConfig{MaxImageBytes: 10 << 20, MaxVideoBytes: 100 << 20, UploadTTL: 5 * time.Minute})
	if err != nil { t.Fatal(err) }
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	intent, err := factory.CreateUploadIntent("user-123", "image/jpeg", "public", 2048, now)
	if err != nil { t.Fatal(err) }
	if !regexp.MustCompile(`^users/user-123/[a-f0-9]{32}$`).MatchString(intent.Key) {
		t.Fatalf("unexpected media key %q", intent.Key)
	}
	if intent.Bucket != "media-public" || intent.URL != "" || intent.MaxUploadBytes != 2048 || intent.Method != "PUT" {
		t.Fatalf("unexpected intent: %+v", intent)
	}
	if !intent.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("unexpected expiry: %s", intent.ExpiresAt)
	}
}

func TestIntentFactoryRejectsInvalidOwnerTypeAndVisibility(t *testing.T) {
	factory, err := NewIntentFactory(IntentConfig{})
	if err != nil { t.Fatal(err) }
	for _, tc := range []struct{ owner, contentType, visibility string; size int64 }{
		{"bad/owner", "image/jpeg", "public", 10},
		{"user-123", "application/pdf", "public", 10},
		{"user-123", "image/jpeg", "friends", 10},
		{"user-123", "image/jpeg", "private", 26 << 20},
	} {
		if _, err := factory.CreateUploadIntent(tc.owner, tc.contentType, tc.visibility, tc.size, time.Now()); err == nil {
			t.Fatalf("invalid intent accepted: %+v", tc)
		}
	}
}
