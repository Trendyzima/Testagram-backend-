// Package freshstorage implements the clean Appwrite-backed media lifecycle.
// It deliberately does not read, migrate, or mutate legacy Supabase records.
package freshstorage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

const (
	quotaUnitBytes        int64 = 1 << 10
	platformQuotaDocument       = "platform"
	reservationCollection       = "storage_reservations"
	quotaCollection             = "storage_quotas"
	mediaCollection             = "media_objects"
	replicaCollection           = "storage_replicas"
)

var (
	ErrQuotaExceeded       = errors.New("storage quota exceeded")
	ErrStorageConflict     = errors.New("storage transaction conflict; retry the idempotent request")
	ErrReservationConflict = errors.New("upload reservation conflicts with an existing idempotency key")
	ErrReservationExpired  = errors.New("upload reservation expired")
	ErrReservationNotFound = errors.New("upload reservation not found")
	ErrNotOwner            = errors.New("media object does not belong to authenticated user")
	ErrNotReady            = errors.New("media object is not ready")
)

type Reservation struct {
	ID             string    `json:"$id"`
	OwnerID        string    `json:"owner_id"`
	Scope          string    `json:"scope"`
	ObjectID       string    `json:"object_id"`
	ObjectKey      string    `json:"object_key"`
	Bytes          int64     `json:"bytes"`
	ReservedKiB    int64     `json:"reserved_kib"`
	ContentType    string    `json:"content_type"`
	ExpectedSHA256 string    `json:"sha256_expected"`
	PrimaryProvider string    `json:"primary_provider"`
	SHA256         string    `json:"sha256_actual"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
}

type ReserveInput struct {
	OwnerID        string
	IdempotencyKey string
	Scope          string
	Bytes          int64
	ContentType    string
	SHA256         string
	PrimaryProvider string
}

type Store interface {
	Reserve(context.Context, ReserveInput, string, time.Time) (Reservation, error)
	GetReservation(context.Context, string) (Reservation, error)
	ClaimReservation(context.Context, string, string, string, map[string]any) (Reservation, error)
	ReleaseExpiredReservation(context.Context, Reservation) error
	ListExpiredReservations(context.Context, time.Time, int) ([]Reservation, error)
	CommitProtected(context.Context, Reservation, string, string) error
	MediaKey(context.Context, string, string) (string, error)
	GetMediaRecord(context.Context, string) (MediaRecord, error)
	MarkReplicaState(context.Context, string, string, string) error
}

type MediaRecord struct {
	ObjectID       string
	OwnerID        string
	ObjectKey      string
	Bytes          int64
	ContentType    string
	SHA256         string
	Status         string
	Scope          string
	RetentionClass string
	DeletedAt      time.Time
	CreatedAt      time.Time
	StoryExpiry    time.Time
	LifecycleState string
}

type Service struct {
	store            Store
	b2               *virtualstorage.S3Provider
	r2               *virtualstorage.S3Provider
	b2Bucket         string
	r2Bucket         string
	maxObjectBytes   int64
	uploadTTL        time.Duration
	reservationTTL   time.Duration
	cdnBaseURL       string
	cdnSigningSecret string
	archive          *virtualstorage.S3Provider
	archiveBucket    string
	primaryProvider  string
}

func NewService(store Store, providers ProviderSet, maxObjectBytes int64) (*Service, error) {
	if store == nil {
		return nil, errors.New("Appwrite storage store is required")
	}
	if err := providers.ReadyForMedia(); err != nil {
		return nil, err
	}
	b2, ok := providers.B2.(*virtualstorage.S3Provider)
	if !ok {
		return nil, errors.New("Backblaze B2 provider must support verified object replication")
	}
	r2, ok := providers.R2.(*virtualstorage.S3Provider)
	if !ok {
		return nil, errors.New("Cloudflare R2 provider must support verified object replication")
	}
	if maxObjectBytes < 1 || maxObjectBytes > 500<<20 {
		return nil, errors.New("maximum object size must be between 1 byte and 500 MiB")
	}
	if (providers.MediaCDNBaseURL == "") != (providers.MediaCDNSigningSecret == "") {
		return nil, errors.New("Cloudflare media CDN URL and signing secret must be configured together")
	}
	if providers.MediaCDNBaseURL != "" {
		if err := validateMediaCDNBaseURL(providers.MediaCDNBaseURL); err != nil {
			return nil, err
		}
		if len(providers.MediaCDNSigningSecret) < 32 {
			return nil, errors.New("Cloudflare media CDN signing secret must be at least 32 characters")
		}
	}
	var archive *virtualstorage.S3Provider
	if providers.Archive != nil {
		var archiveOK bool
		archive, archiveOK = providers.Archive.(*virtualstorage.S3Provider)
		if !archiveOK || strings.TrimSpace(providers.ArchiveBucket) == "" {
			return nil, errors.New("cold archive provider must support verified object replication and have a bucket")
		}
	}
	primaryProvider := strings.ToLower(strings.TrimSpace(providers.PrimaryProvider))
	if primaryProvider == "" { primaryProvider = "b2" }
	if primaryProvider != "b2" && primaryProvider != "r2" { return nil, errors.New("primary media provider must be b2 or r2") }
	return &Service{store: store, b2: b2, r2: r2, b2Bucket: providers.B2Bucket, r2Bucket: providers.R2Bucket, maxObjectBytes: maxObjectBytes, uploadTTL: time.Hour, reservationTTL: 2 * time.Hour, cdnBaseURL: providers.MediaCDNBaseURL, cdnSigningSecret: providers.MediaCDNSigningSecret, archive: archive, archiveBucket: providers.ArchiveBucket, primaryProvider: primaryProvider}, nil
}

type UploadReservation struct {
	ReservationID   string            `json:"reservation_id"`
	ObjectID        string            `json:"object_id"`
	UploadURL       string            `json:"upload_url"`
	UploadMethod    string            `json:"upload_method"`
	RequiredHeaders map[string]string `json:"required_headers"`
	ExpiresAt       time.Time         `json:"expires_at"`
	ReservedBytes   int64             `json:"reserved_bytes"`
	Status          string            `json:"status"`
}

func (s *Service) ReserveUpload(ctx context.Context, input ReserveInput) (UploadReservation, error) {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ContentType = strings.ToLower(strings.TrimSpace(input.ContentType))
	input.SHA256 = strings.ToLower(strings.TrimSpace(input.SHA256))
	input.Scope = strings.TrimSpace(input.Scope)
	if input.Scope == "" {
		input.Scope = "post_media"
	}
	if !validMediaScope(input.Scope) {
		return UploadReservation{}, errors.New("unsupported media scope")
	}
	if !validOwnerID(input.OwnerID) || len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 128 || strings.ContainsAny(input.IdempotencyKey, "\r\n") {
		return UploadReservation{}, errors.New("authenticated owner and an 8-128 character idempotency key are required")
	}
	if input.Bytes < 1 || input.Bytes > s.maxObjectBytes {
		return UploadReservation{}, virtualstorage.ErrTooLarge
	}
	if !allowedMediaType(input.ContentType) {
		return UploadReservation{}, virtualstorage.ErrForbidden
	}
	if input.SHA256 != "" {
		if len(input.SHA256) != 64 {
			return UploadReservation{}, errors.New("invalid SHA-256 checksum")
		}
		if _, err := hex.DecodeString(input.SHA256); err != nil {
			return UploadReservation{}, errors.New("invalid SHA-256 checksum")
		}
	}
	keyBytes := sha256.Sum256([]byte(input.OwnerID + "\x00" + input.IdempotencyKey))
	reservationID := "res_" + hex.EncodeToString(keyBytes[:])
	expires := time.Now().UTC().Add(s.reservationTTL)
	input.PrimaryProvider = s.primaryProvider
	reservation, err := s.store.Reserve(ctx, input, reservationID, expires)
	if err != nil {
		return UploadReservation{}, err
	}
	if reservation.OwnerID != input.OwnerID || reservation.Bytes != input.Bytes || reservation.ContentType != input.ContentType || reservation.ExpectedSHA256 != input.SHA256 || reservation.Scope != input.Scope {
		return UploadReservation{}, ErrReservationConflict
	}
	// A retry reuses the already reserved opaque key; it never allocates quota twice.
	if reservation.ObjectKey == "" {
		return UploadReservation{}, errors.New("storage reservation has no object key")
	}
	if reservation.Status == "protected" {
		return UploadReservation{ReservationID: reservation.ID, ObjectID: reservation.ObjectID, ExpiresAt: reservation.ExpiresAt, ReservedBytes: reservation.Bytes, Status: "protected"}, nil
	}
	if reservation.Status == "uploaded_primary" {
		return UploadReservation{ReservationID: reservation.ID, ObjectID: reservation.ObjectID, ExpiresAt: reservation.ExpiresAt, ReservedBytes: reservation.Bytes, Status: "uploaded_primary"}, nil
	}
	if reservation.Status != "reserved" {
		return UploadReservation{}, fmt.Errorf("reservation is %s and cannot accept another upload", reservation.Status)
	}
	if !reservation.ExpiresAt.After(time.Now()) {
		return UploadReservation{}, ErrReservationExpired
	}
	primary, primaryBucket, _, _, primaryName, _ := s.providersFor(reservation)
	uploadURL, err := primary.PresignPut(ctx, primaryBucket, reservation.ObjectKey, reservation.ContentType, s.maxObjectBytes, s.uploadTTL)
	if err != nil {
		return UploadReservation{}, fmt.Errorf("create %s upload URL: %w", primaryName, err)
	}
	return UploadReservation{ReservationID: reservation.ID, ObjectID: reservation.ObjectID, UploadURL: uploadURL, UploadMethod: "PUT", RequiredHeaders: map[string]string{"Content-Type": reservation.ContentType}, ExpiresAt: reservation.ExpiresAt, ReservedBytes: reservation.Bytes, Status: "reserved"}, nil
}

func (s *Service) CompleteUpload(ctx context.Context, ownerID, reservationID string) error {
	reservation, err := s.store.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if reservation.OwnerID != ownerID {
		return ErrNotOwner
	}
	if reservation.Status == "protected" {
		return nil
	}
	if reservation.Status == "reserved" {
		if !reservation.ExpiresAt.After(time.Now()) {
			return ErrReservationExpired
		}
		lease := time.Now().UTC().Add(3 * time.Hour)
		reservation, err = s.store.ClaimReservation(ctx, reservation.ID, "reserved", "verifying", map[string]any{"lease_expires_at": lease.Format(time.RFC3339Nano)})
		if err != nil {
			return err
		}
		if reservation.Status != "verifying" {
			return fmt.Errorf("reservation is %s and cannot be verified", reservation.Status)
		}
		primary, primaryBucket, secondary, secondaryBucket, primaryName, secondaryName := s.providersFor(reservation)
		info, headErr := primary.Head(ctx, primaryBucket, reservation.ObjectKey)
		if headErr != nil {
			_, _ = s.store.ClaimReservation(context.Background(), reservation.ID, "verifying", "reserved", map[string]any{})
			return fmt.Errorf("%s upload not available: %w", primaryName, headErr)
		}
		if info.Size != reservation.Bytes || !strings.EqualFold(info.ContentType, reservation.ContentType) {
			_ = primary.Delete(ctx, primaryBucket, reservation.ObjectKey)
			_, _ = s.store.ClaimReservation(context.Background(), reservation.ID, "verifying", "reserved", map[string]any{})
			return virtualstorage.ErrObjectMismatch
		}
		actualHash, hashErr := primary.SHA256(ctx, primaryBucket, reservation.ObjectKey, reservation.Bytes)
		if hashErr != nil {
			_, _ = s.store.ClaimReservation(context.Background(), reservation.ID, "verifying", "reserved", map[string]any{})
			return fmt.Errorf("verify %s object checksum: %w", primaryName, hashErr)
		}
		if reservation.ExpectedSHA256 != "" && actualHash != reservation.ExpectedSHA256 {
			_ = primary.Delete(ctx, primaryBucket, reservation.ObjectKey)
			_, _ = s.store.ClaimReservation(context.Background(), reservation.ID, "verifying", "reserved", map[string]any{})
			return virtualstorage.ErrChecksumMismatch
		}
		reservation, err = s.store.ClaimReservation(ctx, reservation.ID, "verifying", "uploaded_primary", map[string]any{"sha256_actual": actualHash, "primary_provider": primaryName, "primary_verified_at": time.Now().UTC().Format(time.RFC3339Nano), "lease_expires_at": time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339Nano)})
		if err != nil {
			return fmt.Errorf("record verified %s upload: %w", primaryName, err)
		}
	}
	if reservation.Status != "uploaded_primary" {
		if reservation.Status == "verifying" {
			return errors.New("upload verification is already in progress; retry shortly")
		}
		return fmt.Errorf("reservation is %s and cannot be completed", reservation.Status)
	}
	if !validSHA256(reservation.SHA256) {
		return errors.New("verified primary checksum is missing")
	}
	primary, primaryBucket, secondary, secondaryBucket, primaryName, secondaryName := s.providersFor(reservation)
	if err := secondary.CopyFrom(ctx, primary, primaryBucket, reservation.ObjectKey, secondaryBucket, reservation.ObjectKey, reservation.Bytes, reservation.ContentType, reservation.SHA256); err != nil {
		return fmt.Errorf("%s object is verified but %s replica is pending: %w", primaryName, secondaryName, err)
	}
	if err := s.store.CommitProtected(ctx, reservation, s.b2Bucket, s.r2Bucket); err != nil {
		return fmt.Errorf("both cloud copies verified but Appwrite commit is pending: %w", err)
	}
	return nil
}


// providersFor returns the reservation's immutable upload origin and its peer replica.
// Missing provider metadata on legacy reservations defaults to B2.
func (s *Service) providersFor(r Reservation) (*virtualstorage.S3Provider, string, *virtualstorage.S3Provider, string, string, string) {
	origin := strings.ToLower(strings.TrimSpace(r.PrimaryProvider))
	if origin == "" { origin = "b2" }
	if origin == "r2" { return s.r2, s.r2Bucket, s.b2, s.b2Bucket, "r2", "b2" }
	return s.b2, s.b2Bucket, s.r2, s.r2Bucket, "b2", "r2"
}

// ExpireReservation releases quota only after both provider copies are confirmed
// absent. A transaction first claims the reservation so completion cannot race
// with cleanup and delete a newly protected asset.
func (s *Service) ExpireReservation(ctx context.Context, reservationID string) error {
	r, err := s.store.GetReservation(ctx, reservationID)
	if err != nil {
		return err
	}
	if r.Status == "protected" || r.Status == "expired" {
		return nil
	}
	now := time.Now().UTC()
	if r.ExpiresAt.After(now) {
		return errors.New("reservation has not expired")
	}
	switch r.Status {
	case "reserved":
		r, err = s.store.ClaimReservation(ctx, r.ID, r.Status, "expiring", map[string]any{})
	case "uploaded_primary":
		if r.LeaseExpiresAt.After(now) {
			return errors.New("replica completion lease is still active")
		}
		r, err = s.store.ClaimReservation(ctx, r.ID, r.Status, "expiring", map[string]any{})
		if err != nil {
			return err
		}
	case "verifying":
		if r.LeaseExpiresAt.After(now) {
			return errors.New("upload verification lease is still active")
		}
		r, err = s.store.ClaimReservation(ctx, r.ID, "verifying", "expiring", map[string]any{})
		if err != nil {
			return err
		}
	case "expiring":
		// Resume a previous cleanup after a transient provider or Appwrite failure.
	default:
		return fmt.Errorf("reservation is %s and cannot be expired", r.Status)
	}
	for _, target := range []struct {
		p      *virtualstorage.S3Provider
		bucket string
	}{{s.b2, s.b2Bucket}, {s.r2, s.r2Bucket}} {
		info, headErr := target.p.Head(ctx, target.bucket, r.ObjectKey)
		if errors.Is(headErr, virtualstorage.ErrNotFound) {
			continue
		}
		if headErr != nil {
			return fmt.Errorf("check object before expiry cleanup: %w", headErr)
		}
		if info.Key == "" {
			return errors.New("provider returned an invalid object during expiry cleanup")
		}
		if err := target.p.Delete(ctx, target.bucket, r.ObjectKey); err != nil {
			return fmt.Errorf("delete expired media object: %w", err)
		}
		_, verifyErr := target.p.Head(ctx, target.bucket, r.ObjectKey)
		if !errors.Is(verifyErr, virtualstorage.ErrNotFound) {
			if verifyErr != nil {
				return verifyErr
			}
			return errors.New("expired media object still exists after deletion")
		}
	}
	return s.store.ReleaseExpiredReservation(ctx, r)
}

// RepairObject reconciles the two durable cloud copies. It restores one missing
// or corrupt replica from the other only when the surviving source passes both
// metadata and SHA-256 checks. It never deletes the last known-good copy.
func (s *Service) RepairObject(ctx context.Context, objectID string) error {
	record, err := s.store.GetMediaRecord(ctx, objectID)
	if err != nil {
		return err
	}
	if record.Status != "ready" || record.LifecycleState == "purging" || record.Bytes < 1 || record.ObjectKey == "" || len(record.SHA256) != 64 {
		return ErrNotReady
	}
	b2OK := verifyReplica(ctx, s.b2, s.b2Bucket, record)
	r2OK := verifyReplica(ctx, s.r2, s.r2Bucket, record)
	if !b2OK && !r2OK {
		return errors.New("both durable replicas are unavailable or failed integrity verification; manual recovery required")
	}
	if b2OK && !r2OK {
		if err := s.r2.CopyFrom(ctx, s.b2, s.b2Bucket, record.ObjectKey, s.r2Bucket, record.ObjectKey, record.Bytes, record.ContentType, record.SHA256); err != nil {
			return fmt.Errorf("repair R2 replica: %w", err)
		}
	}
	if r2OK && !b2OK {
		if err := s.b2.CopyFrom(ctx, s.r2, s.r2Bucket, record.ObjectKey, s.b2Bucket, record.ObjectKey, record.Bytes, record.ContentType, record.SHA256); err != nil {
			return fmt.Errorf("repair B2 replica: %w", err)
		}
	}
	for _, provider := range []struct {
		name   string
		p      *virtualstorage.S3Provider
		bucket string
	}{{"b2", s.b2, s.b2Bucket}, {"r2", s.r2, s.r2Bucket}} {
		if !verifyReplica(ctx, provider.p, provider.bucket, record) {
			_ = s.store.MarkReplicaState(ctx, objectID, provider.name, "degraded")
			return fmt.Errorf("%s replica still fails verification after repair", provider.name)
		}
		if err := s.store.MarkReplicaState(ctx, objectID, provider.name, "verified"); err != nil {
			return fmt.Errorf("record %s replica verification: %w", provider.name, err)
		}
	}
	return nil
}

func verifyReplica(ctx context.Context, provider *virtualstorage.S3Provider, bucket string, record MediaRecord) bool {
	info, err := provider.Head(ctx, bucket, record.ObjectKey)
	if err != nil || info.Size != record.Bytes || !strings.EqualFold(info.ContentType, record.ContentType) {
		return false
	}
	hash, err := provider.SHA256(ctx, bucket, record.ObjectKey, record.Bytes)
	return err == nil && hash == record.SHA256
}

// DownloadURL returns a one-hour private media URL only after Appwrite confirms ownership
// and the media object is marked ready. All new uploads are private by default.
func (s *Service) DownloadURL(ctx context.Context, ownerID, objectID string) (string, error) {
	key, err := s.store.MediaKey(ctx, ownerID, objectID)
	if err != nil {
		return "", err
	}
	record, err := s.store.GetMediaRecord(ctx, objectID)
	if err != nil {
		return "", err
	}
	if record.OwnerID != ownerID || record.Status != "ready" || record.LifecycleState == "purging" || record.ObjectKey != key || record.Bytes < 1 || !validSHA256(record.SHA256) {
		return "", ErrNotReady
	}
	// Cloudflare R2 is the preferred delivery origin because the private Worker
	// reads its bucket directly. If R2 is temporarily unavailable or its metadata
	// is wrong, serve the independently verified B2 primary instead of breaking
	// every media view. RepairObject will reconcile the degraded R2 replica.
	r2Info, r2Err := s.r2.Head(ctx, s.r2Bucket, key)
	r2Ready := r2Err == nil && r2Info.Size == record.Bytes && strings.EqualFold(r2Info.ContentType, record.ContentType)
	if r2Ready {
		if s.cdnBaseURL != "" && s.cdnSigningSecret != "" {
			return signPrivateMediaURL(s.cdnBaseURL, s.cdnSigningSecret, key, 3600, time.Now().UTC())
		}
		return s.r2.PresignGet(ctx, s.r2Bucket, key, time.Hour)
	}
	b2Info, b2Err := s.b2.Head(ctx, s.b2Bucket, key)
	if b2Err != nil || b2Info.Size != record.Bytes || !strings.EqualFold(b2Info.ContentType, record.ContentType) {
		if s.archive == nil {
			return "", fmt.Errorf("active B2/R2 replicas unavailable and archive restore is not configured: %w", ErrNotReady)
		}
		if restoreErr := s.RestoreArchive(ctx, ownerID, objectID); restoreErr != nil {
			return "", fmt.Errorf("active replicas unavailable and archive restore failed: %w", restoreErr)
		}
		if s.cdnBaseURL != "" && s.cdnSigningSecret != "" {
			return signPrivateMediaURL(s.cdnBaseURL, s.cdnSigningSecret, key, 3600, time.Now().UTC())
		}
		return s.r2.PresignGet(ctx, s.r2Bucket, key, time.Hour)
	}
	return s.b2.PresignGet(ctx, s.b2Bucket, key, time.Hour)
}

func signPrivateMediaURL(baseURL, secret, objectKey string, ttlSeconds int, now time.Time) (string, error) {
	if validateMediaCDNBaseURL(baseURL) != nil || len(secret) < 32 || !validObjectKey(objectKey) || ttlSeconds < 1 || ttlSeconds > 3600 {
		return "", errors.New("invalid private CDN URL signing configuration")
	}
	expires := now.Unix() + int64(ttlSeconds)
	message := fmt.Sprintf("GET\n%s\n%d", objectKey, expires)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	u.Path = "/private/" + objectKey
	query := u.Query()
	query.Set("e", strconv.FormatInt(expires, 10))
	query.Set("s", hex.EncodeToString(mac.Sum(nil)))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func allowedMediaType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/avif", "video/mp4", "video/webm", "audio/mpeg", "audio/mp4", "audio/ogg", "audio/webm":
		return true
	default:
		return false
	}
}
func validOwnerID(v string) bool {
	if v == "" || len(v) > 36 || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
func reserveKiB(bytes int64) int64 {
	if bytes < 1 {
		return 0
	}
	return (bytes + quotaUnitBytes - 1) / quotaUnitBytes
}

// AppwriteStore uses Appwrite document transactions and atomic numeric increments.
// Quota documents must be provisioned before the API accepts uploads:
// storage_quotas/platform and storage_quotas/<Appwrite user ID>.
type AppwriteStore struct {
	db                  *appwritedb.Client
	platformLimitKiB    int64
	defaultUserQuotaKiB int64
}

func NewAppwriteStore(db *appwritedb.Client) (*AppwriteStore, error) {
	return NewAppwriteStoreWithUserQuota(db, 2<<30)
}

func NewAppwriteStoreWithUserQuota(db *appwritedb.Client, defaultUserQuotaBytes int64) (*AppwriteStore, error) {
	if db == nil {
		return nil, errors.New("Appwrite Database client is required")
	}
	if defaultUserQuotaBytes < quotaUnitBytes || defaultUserQuotaBytes > TargetPoolBytes {
		return nil, errors.New("default per-user quota must be between 1 KiB and the 150 GB platform ceiling")
	}
	return &AppwriteStore{db: db, platformLimitKiB: TargetPoolBytes / quotaUnitBytes, defaultUserQuotaKiB: reserveKiB(defaultUserQuotaBytes)}, nil
}

func (s *AppwriteStore) BootstrapPlatformQuota(ctx context.Context) error {
	q, err := s.readQuota(ctx, platformQuotaDocument)
	if err == nil {
		if q.LimitKiB > s.platformLimitKiB {
			return errors.New("existing platform quota exceeds 150 GB")
		}
		return nil
	}
	var apiErr appwritedb.HTTPError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		return err
	}
	_, createErr := s.db.CreateDocument(ctx, quotaCollection, platformQuotaDocument, map[string]any{"limit_kib": s.platformLimitKiB, "used_kib": 0, "reserved_kib": 0}, nil)
	if createErr == nil {
		return nil
	}
	if errors.As(createErr, &apiErr) && apiErr.StatusCode == 409 {
		q, readErr := s.readQuota(ctx, platformQuotaDocument)
		if readErr == nil && q.LimitKiB <= s.platformLimitKiB {
			return nil
		}
	}
	return fmt.Errorf("create platform storage quota: %w", createErr)
}

func (s *AppwriteStore) ensureUserQuota(ctx context.Context, ownerID string) (quota, error) {
	q, err := s.readQuota(ctx, ownerID)
	if err == nil {
		return q, nil
	}
	var apiErr appwritedb.HTTPError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		return quota{}, err
	}
	_, createErr := s.db.CreateDocument(ctx, quotaCollection, ownerID, map[string]any{"limit_kib": s.defaultUserQuotaKiB, "used_kib": 0, "reserved_kib": 0}, nil)
	if createErr == nil {
		return quota{LimitKiB: s.defaultUserQuotaKiB}, nil
	}
	if errors.As(createErr, &apiErr) && apiErr.StatusCode == 409 {
		return s.readQuota(ctx, ownerID)
	}
	return quota{}, fmt.Errorf("create user storage quota: %w", createErr)
}

func (s *AppwriteStore) Reserve(ctx context.Context, input ReserveInput, id string, expires time.Time) (Reservation, error) {
	if !validOwnerID(input.OwnerID) || !validDocID(id) {
		return Reservation{}, errors.New("invalid reservation owner or ID")
	}
	// Idempotency lookup happens before opening a transaction.
	if raw, err := s.db.GetDocument(ctx, reservationCollection, id); err == nil {
		existing, decodeErr := decodeReservation(raw)
		if decodeErr != nil {
			return Reservation{}, decodeErr
		}
		if existing.OwnerID != input.OwnerID || existing.Bytes != input.Bytes || existing.ContentType != input.ContentType || existing.ExpectedSHA256 != input.SHA256 || existing.Scope != input.Scope {
			return Reservation{}, ErrReservationConflict
		}
		return existing, nil
	} else {
		var apiErr appwritedb.HTTPError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
			return Reservation{}, fmt.Errorf("read idempotent reservation: %w", err)
		}
	}
	userQuota, err := s.ensureUserQuota(ctx, input.OwnerID)
	if err != nil {
		return Reservation{}, err
	}
	platformQuota, err := s.readQuota(ctx, platformQuotaDocument)
	if err != nil {
		return Reservation{}, err
	}
	units := reserveKiB(input.Bytes)
	if units < 1 {
		return Reservation{}, errors.New("invalid reservation size")
	}
	if platformQuota.LimitKiB > s.platformLimitKiB {
		return Reservation{}, errors.New("Appwrite platform quota exceeds the configured 150 GB logical ceiling")
	}
	if userQuota.LimitKiB > s.platformLimitKiB {
		return Reservation{}, errors.New("Appwrite per-user quota exceeds the configured 150 GB logical ceiling")
	}
	if userQuota.LimitKiB-userQuota.UsedKiB < units || platformQuota.LimitKiB-platformQuota.UsedKiB < units {
		return Reservation{}, ErrQuotaExceeded
	}
	objectIDHash := sha256.Sum256([]byte(input.OwnerID + "\x00" + id))
	objectID := "med_" + hex.EncodeToString(objectIDHash[:16])
	var objectRandom [16]byte
	if _, err := rand.Read(objectRandom[:]); err != nil {
		return Reservation{}, err
	}
	objectKey := "v1/" + hex.EncodeToString(objectRandom[:])
	tx, err := s.db.CreateTransaction(ctx, 120)
	if err != nil {
		return Reservation{}, err
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	if err = s.db.IncrementDocumentAttribute(ctx, quotaCollection, platformQuotaDocument, "reserved_kib", units, platformQuota.LimitKiB-platformQuota.UsedKiB, tx); err != nil {
		rollback()
		return Reservation{}, quotaError(err)
	}
	if err = s.db.IncrementDocumentAttribute(ctx, quotaCollection, input.OwnerID, "reserved_kib", units, userQuota.LimitKiB-userQuota.UsedKiB, tx); err != nil {
		rollback()
		return Reservation{}, quotaError(err)
	}
	primaryProvider := input.PrimaryProvider
	if primaryProvider == "" { primaryProvider = "b2" }
	if primaryProvider != "b2" && primaryProvider != "r2" { rollback(); return Reservation{}, errors.New("invalid primary media provider") }
	res := Reservation{ID: id, OwnerID: input.OwnerID, Scope: input.Scope, ObjectID: objectID, ObjectKey: objectKey, Bytes: input.Bytes, ReservedKiB: units, ContentType: input.ContentType, ExpectedSHA256: input.SHA256, PrimaryProvider: primaryProvider, Status: "reserved", ExpiresAt: expires}
	data := map[string]any{"owner_id": res.OwnerID, "scope": res.Scope, "object_id": res.ObjectID, "object_key": res.ObjectKey, "bytes_decimal": fmt.Sprint(res.Bytes), "reserved_kib": res.ReservedKiB, "content_type": res.ContentType, "sha256_expected": res.ExpectedSHA256, "sha256_actual": res.SHA256, "primary_provider": res.PrimaryProvider, "status": res.Status, "expires_at": res.ExpiresAt.Format(time.RFC3339Nano)}
	if err = s.db.CreateDocumentInTransaction(ctx, reservationCollection, id, data, tx); err != nil {
		rollback()
		return Reservation{}, err
	}
	if err = s.db.UpdateTransaction(ctx, tx, true); err != nil {
		return Reservation{}, quotaError(err)
	}
	return res, nil
}

type quota struct{ LimitKiB, UsedKiB, ReservedKiB int64 }

func (s *AppwriteStore) readQuota(ctx context.Context, id string) (quota, error) {
	raw, err := s.db.GetDocument(ctx, quotaCollection, id)
	if err != nil {
		return quota{}, fmt.Errorf("required Appwrite storage quota %q is missing or unavailable: %w", id, err)
	}
	var values struct {
		Limit    int64 `json:"limit_kib"`
		Used     int64 `json:"used_kib"`
		Reserved int64 `json:"reserved_kib"`
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return quota{}, errors.New("invalid Appwrite storage quota document")
	}
	if values.Limit < 1 || values.Used < 0 || values.Reserved < 0 || values.Used+values.Reserved > values.Limit {
		return quota{}, errors.New("Appwrite storage quota document violates capacity invariants")
	}
	return quota{values.Limit, values.Used, values.Reserved}, nil
}

func (s *AppwriteStore) GetReservation(ctx context.Context, id string) (Reservation, error) {
	if !validDocID(id) {
		return Reservation{}, ErrReservationNotFound
	}
	raw, err := s.db.GetDocument(ctx, reservationCollection, id)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return Reservation{}, ErrReservationNotFound
		}
		return Reservation{}, err
	}
	return decodeReservation(raw)
}

func decodeReservation(raw []byte) (Reservation, error) {
	var wire struct {
		ID             string `json:"$id"`
		OwnerID        string `json:"owner_id"`
		Scope          string `json:"scope"`
		ObjectID       string `json:"object_id"`
		ObjectKey      string `json:"object_key"`
		Bytes          string `json:"bytes_decimal"`
		ReservedKiB    int64  `json:"reserved_kib"`
		ContentType    string `json:"content_type"`
		ExpectedSHA256 string `json:"sha256_expected"`
		SHA256         string `json:"sha256_actual"`
		PrimaryProvider string `json:"primary_provider"`
		Status         string `json:"status"`
		ExpiresAt      string `json:"expires_at"`
		LeaseExpiresAt string `json:"lease_expires_at"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Reservation{}, errors.New("invalid Appwrite reservation document")
	}
	bytes, parseErr := strconv.ParseInt(wire.Bytes, 10, 64)
	if parseErr != nil || bytes < 1 || strconv.FormatInt(bytes, 10) != wire.Bytes {
		return Reservation{}, errors.New("invalid reserved byte count")
	}
	expires, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil {
		return Reservation{}, errors.New("invalid reservation expiry")
	}
	if !validDocID(wire.ID) || !validOwnerID(wire.OwnerID) || !validMediaScope(wire.Scope) || !validDocID(wire.ObjectID) || !validObjectKey(wire.ObjectKey) || wire.ReservedKiB < 1 || wire.Status == "" || (wire.ExpectedSHA256 != "" && !validSHA256(wire.ExpectedSHA256)) || (wire.SHA256 != "" && !validSHA256(wire.SHA256)) {
		return Reservation{}, errors.New("Appwrite reservation document is incomplete")
	}
	var lease time.Time
	if wire.LeaseExpiresAt != "" {
		lease, err = time.Parse(time.RFC3339Nano, wire.LeaseExpiresAt)
		if err != nil {
			return Reservation{}, errors.New("invalid reservation lease expiry")
		}
	}
	primaryProvider := wire.PrimaryProvider
	if primaryProvider == "" { primaryProvider = "b2" } // Backward compatibility for existing B2-origin reservations.
	if primaryProvider != "b2" && primaryProvider != "r2" { return Reservation{}, errors.New("invalid reservation primary provider") }
	return Reservation{ID: wire.ID, OwnerID: wire.OwnerID, Scope: wire.Scope, ObjectID: wire.ObjectID, ObjectKey: wire.ObjectKey, Bytes: bytes, ReservedKiB: wire.ReservedKiB, ContentType: wire.ContentType, ExpectedSHA256: wire.ExpectedSHA256, PrimaryProvider: primaryProvider, SHA256: wire.SHA256, Status: wire.Status, ExpiresAt: expires, LeaseExpiresAt: lease}, nil
}

func (s *AppwriteStore) ClaimReservation(ctx context.Context, id, expectedStatus, nextStatus string, fields map[string]any) (Reservation, error) {
	if !validDocID(id) || !validReservationTransition(expectedStatus, nextStatus) {
		return Reservation{}, errors.New("invalid reservation state transition")
	}
	tx, err := s.db.CreateTransaction(ctx, 120)
	if err != nil {
		return Reservation{}, err
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	raw, err := s.db.GetDocumentInTransaction(ctx, reservationCollection, id, tx)
	if err != nil {
		rollback()
		return Reservation{}, err
	}
	current, err := decodeReservation(raw)
	if err != nil {
		rollback()
		return Reservation{}, err
	}
	if current.Status != expectedStatus {
		rollback()
		return Reservation{}, fmt.Errorf("reservation state changed from %s to %s", expectedStatus, current.Status)
	}
	update := map[string]any{"status": nextStatus}
	for key, value := range fields {
		update[key] = value
	}
	if err = s.db.UpdateDocumentInTransaction(ctx, reservationCollection, id, update, tx); err != nil {
		rollback()
		return Reservation{}, err
	}
	if err = s.db.UpdateTransaction(ctx, tx, true); err != nil {
		return Reservation{}, err
	}
	current.Status = nextStatus
	if value, ok := fields["sha256_actual"].(string); ok {
		current.SHA256 = value
	}
	if value, ok := fields["primary_provider"].(string); ok { current.PrimaryProvider = value }
	if value, ok := fields["lease_expires_at"].(string); ok {
		current.LeaseExpiresAt, _ = time.Parse(time.RFC3339Nano, value)
	}
	return current, nil
}

func (s *AppwriteStore) ReleaseExpiredReservation(ctx context.Context, r Reservation) error {
	if r.Status != "expiring" {
		return errors.New("reservation must be claimed for expiry before quota release")
	}
	userQuota, err := s.readQuota(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	platformQuota, err := s.readQuota(ctx, platformQuotaDocument)
	if err != nil {
		return err
	}
	tx, err := s.db.CreateTransaction(ctx, 120)
	if err != nil {
		return err
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	currentRaw, readErr := s.db.GetDocumentInTransaction(ctx, reservationCollection, r.ID, tx)
	if readErr != nil {
		rollback()
		return readErr
	}
	currentReservation, decodeErr := decodeReservation(currentRaw)
	if decodeErr != nil {
		rollback()
		return decodeErr
	}
	if currentReservation.Status != "expiring" {
		rollback()
		return fmt.Errorf("reservation changed to %s before quota release", currentReservation.Status)
	}
	for _, q := range []struct {
		id    string
		value quota
	}{{platformQuotaDocument, platformQuota}, {r.OwnerID, userQuota}} {
		if err = s.db.DecrementDocumentAttribute(ctx, quotaCollection, q.id, "reserved_kib", r.ReservedKiB, 0, tx); err != nil {
			rollback()
			return err
		}
	}
	if err = s.db.UpdateDocumentInTransaction(ctx, reservationCollection, r.ID, map[string]any{"status": "expired", "expired_at": time.Now().UTC().Format(time.RFC3339Nano)}, tx); err != nil {
		rollback()
		return err
	}
	return s.db.UpdateTransaction(ctx, tx, true)
}

func (s *AppwriteStore) ListExpiredReservations(ctx context.Context, now time.Time, limit int) ([]Reservation, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("expiry scan limit must be between 1 and 100")
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	queries := [][]byte{
		appwriteAndQuery(appwriteEqualQuery("status", "reserved"), appwriteLessThanEqualQuery("expires_at", stamp)),
		appwriteAndQuery(appwriteEqualQuery("status", "uploaded_primary"), appwriteLessThanEqualQuery("expires_at", stamp), appwriteLessThanEqualQuery("lease_expires_at", stamp)),
		appwriteAndQuery(appwriteEqualQuery("status", "verifying"), appwriteLessThanEqualQuery("expires_at", stamp), appwriteLessThanEqualQuery("lease_expires_at", stamp)),
		appwriteEqualQuery("status", "expiring"),
	}
	out := make([]Reservation, 0)
	for _, query := range queries {
		rawDocs, err := s.db.ListDocuments(ctx, reservationCollection, string(query), limit-len(out))
		if err != nil {
			return nil, err
		}
		for _, raw := range rawDocs {
			r, err := decodeReservation(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func appwriteEqualQuery(column string, value any) []byte {
	encoded, _ := json.Marshal(map[string]any{"method": "equal", "column": column, "values": []any{value}})
	return encoded
}
func appwriteLessThanEqualQuery(column string, value any) []byte {
	encoded, _ := json.Marshal(map[string]any{"method": "lessThanEqual", "column": column, "values": []any{value}})
	return encoded
}
func appwriteGreaterThanEqualQuery(attribute string, value any) []byte {
	encoded, _ := json.Marshal(map[string]any{"method": "greaterThanEqual", "column": attribute, "values": []any{value}})
	return encoded
}
func appwriteAndQuery(queries ...[]byte) []byte {
	items := make([]json.RawMessage, 0, len(queries))
	for _, query := range queries {
		items = append(items, json.RawMessage(query))
	}
	encoded, _ := json.Marshal(map[string]any{"method": "and", "values": items})
	return encoded
}

func validMediaScope(v string) bool {
	switch v {
	case "post_media", "story_media", "direct_message", "profile_media", "avatar":
		return true
	default:
		return false
	}
}

func validReservationStatus(v string) bool {
	switch v {
	case "reserved", "verifying", "uploaded_primary", "protected", "expiring", "expired":
		return true
	default:
		return false
	}
}
func validReservationTransition(from, to string) bool {
	if !validReservationStatus(from) || !validReservationStatus(to) {
		return false
	}
	switch from {
	case "reserved":
		return to == "verifying" || to == "expiring"
	case "verifying":
		return to == "reserved" || to == "uploaded_primary" || to == "expiring"
	case "uploaded_primary":
		return to == "expiring"
	default:
		return false
	}
}

func (s *AppwriteStore) CommitProtected(ctx context.Context, r Reservation, b2Bucket, r2Bucket string) error {
	if r.Status != "uploaded_primary" || !validOwnerID(r.OwnerID) || !validDocID(r.ID) || !validDocID(r.ObjectID) || !validObjectKey(r.ObjectKey) || !validSHA256(r.SHA256) {
		return errors.New("reservation is not eligible for protected commit")
	}
	userQuota, err := s.readQuota(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	platformQuota, err := s.readQuota(ctx, platformQuotaDocument)
	if err != nil {
		return err
	}
	tx, err := s.db.CreateTransaction(ctx, 120)
	if err != nil {
		return err
	}
	rollback := func() { _ = s.db.UpdateTransaction(context.Background(), tx, false) }
	currentRaw, readErr := s.db.GetDocumentInTransaction(ctx, reservationCollection, r.ID, tx)
	if readErr != nil {
		rollback()
		return readErr
	}
	currentReservation, decodeErr := decodeReservation(currentRaw)
	if decodeErr != nil {
		rollback()
		return decodeErr
	}
	if currentReservation.Status != "uploaded_primary" {
		rollback()
		return fmt.Errorf("reservation changed to %s before protected commit", currentReservation.Status)
	}
	for _, q := range []struct {
		id    string
		value quota
	}{{platformQuotaDocument, platformQuota}, {r.OwnerID, userQuota}} {
		if err = s.db.DecrementDocumentAttribute(ctx, quotaCollection, q.id, "reserved_kib", r.ReservedKiB, 0, tx); err != nil {
			rollback()
			return err
		}
		if err = s.db.IncrementDocumentAttribute(ctx, quotaCollection, q.id, "used_kib", r.ReservedKiB, q.value.LimitKiB, tx); err != nil {
			rollback()
			return quotaError(err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err = s.db.UpdateDocumentInTransaction(ctx, reservationCollection, r.ID, map[string]any{"status": "protected", "protected_at": now}, tx); err != nil {
		rollback()
		return err
	}
	createdAt := time.Now().UTC()
	mediaData, metadataErr := protectedMediaMetadata(r, createdAt)
	if metadataErr != nil {
		rollback()
		return metadataErr
	}
	if err = s.db.CreateDocumentInTransaction(ctx, mediaCollection, r.ObjectID, mediaData, tx); err != nil {
		rollback()
		return err
	}
	for _, replica := range []struct{ id, provider, bucket string }{{"b2_" + r.ObjectID, "b2", b2Bucket}, {"r2_" + r.ObjectID, "r2", r2Bucket}} {
		data := map[string]any{"object_id": r.ObjectID, "provider": replica.provider, "bucket": replica.bucket, "object_key": r.ObjectKey, "bytes_decimal": fmt.Sprint(r.Bytes), "sha256": r.SHA256, "state": "verified", "verified_at": now}
		if err = s.db.CreateDocumentInTransaction(ctx, replicaCollection, replica.id, data, tx); err != nil {
			rollback()
			return err
		}
	}
	return s.db.UpdateTransaction(ctx, tx, true)
}

func protectedMediaMetadata(r Reservation, createdAt time.Time) (map[string]any, error) {
	if !validMediaScope(r.Scope) || !validOwnerID(r.OwnerID) || !validDocID(r.ObjectID) || !validObjectKey(r.ObjectKey) || r.Bytes < 1 || !validSHA256(r.SHA256) {
		return nil, errors.New("reservation has incomplete protected media metadata")
	}
	retentionClass := "permanent"
	archiveAfter := createdAt.UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	data := map[string]any{
		"owner_id": r.OwnerID, "object_id": r.ObjectID, "object_key": r.ObjectKey,
		"namespace": "media-private", "bytes_decimal": fmt.Sprint(r.Bytes), "mime_type": r.ContentType,
		"sha256": r.SHA256, "visibility": "private", "status": "ready", "scope": r.Scope,
		"retention_class": retentionClass, "retention_scope": r.Scope, "lifecycle_state": "active",
		"archive_state": "pending", "archive_after": archiveAfter,
		"retention_policy_version": RetentionPolicyVersion, "lifecycle_attempts": 0,
		"created_at": createdAt.UTC().Format(time.RFC3339Nano),
	}
	if r.Scope == "story_media" {
		data["retention_class"] = "story"
		data["story_expires_at"] = createdAt.UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
		delete(data, "archive_after")
	}
	return data, nil
}

func (s *AppwriteStore) GetMediaRecord(ctx context.Context, objectID string) (MediaRecord, error) {
	if !validDocID(objectID) {
		return MediaRecord{}, ErrNotReady
	}
	raw, err := s.db.GetDocument(ctx, mediaCollection, objectID)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return MediaRecord{}, ErrNotReady
		}
		return MediaRecord{}, err
	}
	var wire struct {
		OwnerID        string `json:"owner_id"`
		ObjectKey      string `json:"object_key"`
		Bytes          string `json:"bytes_decimal"`
		ContentType    string `json:"mime_type"`
		SHA256         string `json:"sha256"`
		Status         string `json:"status"`
		Scope          string `json:"scope"`
		RetentionClass string `json:"retention_class"`
		DeletedAt      string `json:"deleted_at"`
		CreatedAt      string `json:"created_at"`
		StoryExpiry    string `json:"story_expires_at"`
		LifecycleState string `json:"lifecycle_state"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return MediaRecord{}, errors.New("invalid Appwrite media object document")
	}
	bytes, err := strconv.ParseInt(wire.Bytes, 10, 64)
	if err != nil || bytes < 1 || strconv.FormatInt(bytes, 10) != wire.Bytes || !validOwnerID(wire.OwnerID) || !validObjectKey(wire.ObjectKey) || wire.ContentType == "" || !validSHA256(wire.SHA256) {
		return MediaRecord{}, errors.New("Appwrite media object metadata is invalid")
	}
	var deletedAt time.Time
	if wire.DeletedAt != "" {
		deletedAt, err = time.Parse(time.RFC3339Nano, wire.DeletedAt)
		if err != nil {
			return MediaRecord{}, errors.New("invalid media deletion timestamp")
		}
	}
	var createdAt, storyExpiry time.Time
	if wire.CreatedAt != "" {
		createdAt, err = time.Parse(time.RFC3339Nano, wire.CreatedAt)
		if err != nil {
			return MediaRecord{}, errors.New("invalid media creation timestamp")
		}
	}
	if wire.StoryExpiry != "" {
		storyExpiry, err = time.Parse(time.RFC3339Nano, wire.StoryExpiry)
		if err != nil {
			return MediaRecord{}, errors.New("invalid story media expiry timestamp")
		}
	}
	return MediaRecord{ObjectID: objectID, OwnerID: wire.OwnerID, ObjectKey: wire.ObjectKey, Bytes: bytes, ContentType: wire.ContentType, SHA256: wire.SHA256, Status: wire.Status, Scope: wire.Scope, RetentionClass: wire.RetentionClass, DeletedAt: deletedAt, CreatedAt: createdAt, StoryExpiry: storyExpiry, LifecycleState: wire.LifecycleState}, nil
}

func (s *AppwriteStore) MarkReplicaState(ctx context.Context, objectID, provider, state string) error {
	if !validDocID(objectID) || (provider != "b2" && provider != "r2") || (state != "verified" && state != "degraded") {
		return errors.New("invalid replica state update")
	}
	data := map[string]any{"state": state}
	if state == "verified" {
		data["verified_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		data["deleted_at"] = ""
	}
	_, err := s.db.UpdateDocument(ctx, replicaCollection, provider+"_"+objectID, data)
	return err
}

func (s *AppwriteStore) MediaKey(ctx context.Context, ownerID, objectID string) (string, error) {
	if !validOwnerID(ownerID) || !validDocID(objectID) {
		return "", ErrNotOwner
	}
	raw, err := s.db.GetDocument(ctx, mediaCollection, objectID)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return "", ErrNotReady
		}
		return "", err
	}
	var item struct {
		OwnerID    string `json:"owner_id"`
		Key        string `json:"object_key"`
		Status     string `json:"status"`
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return "", errors.New("invalid Appwrite media object document")
	}
	if item.OwnerID != ownerID {
		return "", ErrNotOwner
	}
	if item.Status != "ready" || item.Visibility != "private" || item.Key == "" {
		return "", ErrNotReady
	}
	return item.Key, nil
}

func quotaError(err error) error {
	var apiErr appwritedb.HTTPError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 409 {
			return fmt.Errorf("%w: %v", ErrStorageConflict, err)
		}
		message := strings.ToLower(apiErr.Message)
		if apiErr.StatusCode == 400 && (strings.Contains(message, "maximum") || strings.Contains(message, "minimum") || strings.Contains(message, "max") || strings.Contains(message, "min")) {
			return fmt.Errorf("%w: %v", ErrQuotaExceeded, err)
		}
	}
	return err
}
func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validObjectKey(value string) bool {
	if !strings.HasPrefix(value, "v1/") || len(value) != 35 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "v1/"))
	return err == nil
}

func validDocID(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
