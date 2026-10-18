// Package virtualstorage provides logical, tenant-scoped storage namespaces over a
// replaceable object-store provider. It owns policy and routing, not object bytes.
package virtualstorage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidNamespace = errors.New("invalid storage namespace")
	ErrInvalidKey       = errors.New("invalid object key")
	ErrForbidden        = errors.New("storage operation forbidden")
	ErrTooLarge         = errors.New("object exceeds namespace size limit")
	ErrNotFound         = errors.New("object not found")
	ErrObjectMismatch    = errors.New("uploaded object does not match declared metadata")
	ErrChecksumMismatch  = errors.New("uploaded object SHA-256 does not match expected checksum")
	ErrChecksumUnavailable = errors.New("storage provider cannot calculate a content SHA-256")
	tenantIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	namespacePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// ObjectInfo contains provider-independent object metadata. ETag is opaque and
// must not be treated as a cryptographic content hash.
type ObjectInfo struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	ETag        string    `json:"etag,omitempty"`
	ModifiedAt  time.Time `json:"modified_at,omitempty"`
}

// Provider is the minimal control-plane contract required from an object store.
// Implementations should use provider-side signed URLs for large data transfers;
// application servers must not proxy large media bodies.
type Provider interface {
	PresignPut(ctx context.Context, bucket, key, contentType string, maxBytes int64, ttl time.Duration) (string, error)
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
	Head(ctx context.Context, bucket, key string) (ObjectInfo, error)
	Delete(ctx context.Context, bucket, key string) error
	List(ctx context.Context, bucket, prefix, cursor string, limit int) ([]ObjectInfo, string, error)
}

// Namespace defines a logical bucket visible to application code. Physical
// bucket names and provider details never need to reach clients.
type Namespace struct {
	Name           string
	PhysicalBucket string
	Public          bool
	MaxObjectBytes int64
	AllowedTypes    map[string]struct{}
	UploadTTL       time.Duration
	DownloadTTL     time.Duration
}

// Manager maps logical namespaces to physical buckets and enforces tenant
// boundaries before any provider call.
type Manager struct {
	provider   Provider
	namespaces map[string]Namespace
}

func New(provider Provider, namespaces []Namespace) (*Manager, error) {
	if provider == nil {
		return nil, errors.New("storage provider is required")
	}
	if len(namespaces) == 0 {
		return nil, errors.New("at least one storage namespace is required")
	}
	m := &Manager{provider: provider, namespaces: make(map[string]Namespace, len(namespaces))}
	physical := make(map[string]struct{}, len(namespaces))
	for _, ns := range namespaces {
		ns.Name = strings.TrimSpace(ns.Name)
		ns.PhysicalBucket = strings.TrimSpace(ns.PhysicalBucket)
		if !namespacePattern.MatchString(ns.Name) || ns.PhysicalBucket == "" || strings.ContainsAny(ns.PhysicalBucket, "/\\?#\r\n") {
			return nil, fmt.Errorf("%w: name or physical bucket is invalid", ErrInvalidNamespace)
		}
		if _, exists := m.namespaces[ns.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate logical name %q", ErrInvalidNamespace, ns.Name)
		}
		// A physical bucket can back multiple logical namespaces only when the
		// caller explicitly chooses that mapping. We still track names for audit.
		physical[ns.PhysicalBucket] = struct{}{}
		if ns.MaxObjectBytes < 1 {
			return nil, fmt.Errorf("%w: max object size must be positive", ErrInvalidNamespace)
		}
		if ns.UploadTTL < time.Minute || ns.UploadTTL > time.Hour {
			return nil, fmt.Errorf("%w: upload TTL must be between 1 minute and 1 hour", ErrInvalidNamespace)
		}
		if ns.DownloadTTL < time.Minute || ns.DownloadTTL > time.Hour {
			return nil, fmt.Errorf("%w: download TTL must be between 1 minute and 1 hour", ErrInvalidNamespace)
		}
		if len(ns.AllowedTypes) == 0 {
			return nil, fmt.Errorf("%w: allowed content types are required", ErrInvalidNamespace)
		}
		copiedTypes := make(map[string]struct{}, len(ns.AllowedTypes))
		for typ := range ns.AllowedTypes {
			typ = strings.ToLower(strings.TrimSpace(typ))
			if typ == "" || !strings.Contains(typ, "/") || strings.ContainsAny(typ, "\r\n") {
				return nil, fmt.Errorf("%w: invalid content type", ErrInvalidNamespace)
			}
			copiedTypes[typ] = struct{}{}
		}
		ns.AllowedTypes = copiedTypes
		m.namespaces[ns.Name] = ns
	}
	_ = physical
	return m, nil
}

// UploadURL issues a short-lived URL for an object under the authenticated
// tenant's reserved prefix. Callers must authenticate tenantID before invoking.
func (m *Manager) UploadURL(ctx context.Context, tenantID, namespace, key, contentType string, size int64) (string, error) {
	ns, err := m.namespace(namespace)
	if err != nil {
		return "", err
	}
	if !tenantIDPattern.MatchString(tenantID) || !validKey(key) {
		return "", ErrInvalidKey
	}
	if size < 1 || size > ns.MaxObjectBytes {
		return "", ErrTooLarge
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if _, ok := ns.AllowedTypes[contentType]; !ok {
		return "", fmt.Errorf("%w: content type not allowed", ErrForbidden)
	}
	scopedKey := tenantPrefix(tenantID) + key
	return m.provider.PresignPut(ctx, ns.PhysicalBucket, scopedKey, contentType, size, ns.UploadTTL)
}

// DownloadURL refuses private namespace access unless the caller is authorized
// for the owning tenant. Public namespaces can still be served by a separate CDN;
// this method always returns a short-lived provider URL.
func (m *Manager) DownloadURL(ctx context.Context, requesterTenant, ownerTenant, namespace, key string) (string, error) {
	ns, err := m.namespace(namespace)
	if err != nil {
		return "", err
	}
	if !tenantIDPattern.MatchString(ownerTenant) || !tenantIDPattern.MatchString(requesterTenant) || !validKey(key) {
		return "", ErrInvalidKey
	}
	if !ns.Public && requesterTenant != ownerTenant {
		return "", ErrForbidden
	}
	return m.provider.PresignGet(ctx, ns.PhysicalBucket, tenantPrefix(ownerTenant)+key, ns.DownloadTTL)
}

// VerifyUpload checks provider-observed metadata before an upload can be marked
// complete. It does not trust the client-reported content type or byte count.
func (m *Manager) VerifyUpload(ctx context.Context, tenantID, namespace, key, expectedContentType string, expectedSize int64) (ObjectInfo, error) {
	ns, err := m.namespace(namespace)
	if err != nil {
		return ObjectInfo{}, err
	}
	if !tenantIDPattern.MatchString(tenantID) || !validKey(key) {
		return ObjectInfo{}, ErrInvalidKey
	}
	if expectedSize < 1 || expectedSize > ns.MaxObjectBytes {
		return ObjectInfo{}, ErrTooLarge
	}
	expectedContentType = strings.ToLower(strings.TrimSpace(expectedContentType))
	if _, ok := ns.AllowedTypes[expectedContentType]; !ok {
		return ObjectInfo{}, ErrForbidden
	}
	info, err := m.provider.Head(ctx, ns.PhysicalBucket, tenantPrefix(tenantID)+key)
	if err != nil {
		return ObjectInfo{}, err
	}
	actualType := strings.ToLower(strings.TrimSpace(info.ContentType))
	if info.Size != expectedSize || info.Size < 1 || info.Size > ns.MaxObjectBytes || actualType != expectedContentType {
		return ObjectInfo{}, ErrObjectMismatch
	}
	info.Key = key
	return info, nil
}

// VerifyUploadChecksum compares a client-declared SHA-256 with the hash of the
// actual stored bytes. The provider must stream with a hard byte bound; ETags
// are not accepted as substitutes for SHA-256.
func (m *Manager) VerifyUploadChecksum(ctx context.Context, tenantID, namespace, key string, maxBytes int64, expectedSHA256 string) error {
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if len(expectedSHA256) != 64 {
		return errors.New("expected SHA-256 must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return errors.New("expected SHA-256 must be 64 hexadecimal characters")
	}
	ns, err := m.namespace(namespace)
	if err != nil {
		return err
	}
	if !tenantIDPattern.MatchString(tenantID) || !validKey(key) {
		return ErrInvalidKey
	}
	if maxBytes < 1 || maxBytes > ns.MaxObjectBytes {
		return ErrTooLarge
	}
	checksummer, ok := m.provider.(interface {
		SHA256(context.Context, string, string, int64) (string, error)
	})
	if !ok {
		return ErrChecksumUnavailable
	}
	actual, err := checksummer.SHA256(ctx, ns.PhysicalBucket, tenantPrefix(tenantID)+key, maxBytes)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(actual), []byte(expectedSHA256)) {
		return ErrChecksumMismatch
	}
	return nil
}

// Delete is deliberately owner-scoped, including for public objects. Public
// readability must never imply public write/delete authority.
func (m *Manager) Delete(ctx context.Context, requesterTenant, ownerTenant, namespace, key string) error {
	ns, err := m.namespace(namespace)
	if err != nil {
		return err
	}
	if !tenantIDPattern.MatchString(ownerTenant) || requesterTenant != ownerTenant || !validKey(key) {
		return ErrForbidden
	}
	return m.provider.Delete(ctx, ns.PhysicalBucket, tenantPrefix(ownerTenant)+key)
}

// List returns only the authenticated tenant's objects. The provider cursor is
// opaque; callers must not manufacture or reinterpret it.
func (m *Manager) List(ctx context.Context, tenantID, namespace, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	ns, err := m.namespace(namespace)
	if err != nil {
		return nil, "", err
	}
	if !tenantIDPattern.MatchString(tenantID) || !validPrefix(prefix) {
		return nil, "", ErrInvalidKey
	}
	if limit < 1 || limit > 1000 {
		return nil, "", errors.New("list limit must be between 1 and 1000")
	}
	items, next, err := m.provider.List(ctx, ns.PhysicalBucket, tenantPrefix(tenantID)+prefix, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	scoped := make([]ObjectInfo, 0, len(items))
	tenantScope := tenantPrefix(tenantID)
	for _, item := range items {
		if !strings.HasPrefix(item.Key, tenantScope) {
			// Never trust a provider adapter to return only the requested prefix.
			continue
		}
		item.Key = strings.TrimPrefix(item.Key, tenantScope)
		scoped = append(scoped, item)
	}
	return scoped, next, nil
}

func (m *Manager) namespace(name string) (Namespace, error) {
	ns, ok := m.namespaces[strings.TrimSpace(name)]
	if !ok {
		return Namespace{}, ErrInvalidNamespace
	}
	return ns, nil
}

func tenantPrefix(tenant string) string { return "tenants/" + tenant + "/" }

func validKey(key string) bool {
	if key == "" || len(key) > 1024 || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00\r\n") {
		return false
	}
	clean := path.Clean(key)
	return clean == key && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validPrefix(prefix string) bool {
	if prefix == "" {
		return true
	}
	return validKey(prefix) && strings.HasSuffix(prefix, "/")
}
