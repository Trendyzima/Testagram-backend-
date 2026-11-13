package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// IntentConfig contains policy only. It deliberately has no storage endpoint or
// provider credentials; Data Centre signs and owns the actual object upload.
type IntentConfig struct {
	MaxImageBytes int64
	MaxVideoBytes int64
	UploadTTL time.Duration
}

type IntentFactory struct {
	maxImageBytes int64
	maxVideoBytes int64
	uploadTTL time.Duration
}

func NewIntentFactory(cfg IntentConfig) (*IntentFactory, error) {
	if cfg.MaxImageBytes == 0 { cfg.MaxImageBytes = 25 << 20 }
	if cfg.MaxVideoBytes == 0 { cfg.MaxVideoBytes = 500 << 20 }
	if cfg.UploadTTL == 0 { cfg.UploadTTL = 10 * time.Minute }
	if cfg.MaxImageBytes < 1 || cfg.MaxImageBytes > 5<<30 || cfg.MaxVideoBytes < 1 || cfg.MaxVideoBytes > 5<<30 {
		return nil, errors.New("media intent size limits must be between 1 byte and 5 GiB")
	}
	if cfg.UploadTTL < time.Minute || cfg.UploadTTL > time.Hour {
		return nil, errors.New("media intent TTL must be between 1 minute and 1 hour")
	}
	return &IntentFactory{maxImageBytes: cfg.MaxImageBytes, maxVideoBytes: cfg.MaxVideoBytes, uploadTTL: cfg.UploadTTL}, nil
}

func (f *IntentFactory) MaxBytesForContentType(contentType string) (int64, bool) {
	kind, ok := allowedTypes[contentType]
	if !ok { return 0, false }
	if kind == "image" { return f.maxImageBytes, true }
	return f.maxVideoBytes, true
}

func (f *IntentFactory) CreateUploadIntent(userID, contentType, visibility string, size int64, now time.Time) (UploadIntent, error) {
	if f == nil || !userIDPattern.MatchString(userID) {
		return UploadIntent{}, errors.New("invalid media owner")
	}
	maxBytes, ok := f.MaxBytesForContentType(contentType)
	if !ok || size < 1 || size > maxBytes {
		return UploadIntent{}, errors.New("media type or size is not allowed")
	}
	if visibility != "private" && visibility != "public" {
		return UploadIntent{}, errors.New("visibility must be private or public")
	}
	if now.IsZero() { now = time.Now().UTC() }
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return UploadIntent{}, errors.New("could not generate an opaque media object key")
	}
	namespace := "media-private"
	if visibility == "public" { namespace = "media-public" }
	return UploadIntent{
		Bucket: namespace,
		Key: "users/" + userID + "/" + hex.EncodeToString(raw[:]),
		Method: "PUT",
		Fields: map[string]string{},
		ExpiresAt: now.UTC().Add(f.uploadTTL),
		MaxUploadBytes: size,
	}, nil
}
