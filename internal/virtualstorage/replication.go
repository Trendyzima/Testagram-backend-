package virtualstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type checksumProvider interface {
	Provider
	SHA256(context.Context, string, string, int64) (string, error)
}

type copyProvider interface {
	Provider
	CopyFromProvider(context.Context, Provider, string, string, string, string, int64, string, string) error
}

type ReplicaTarget struct {
	Name     string
	Provider Provider
	Buckets  map[string]string
}

type ReplicaResult struct {
	Provider string
	Bucket   string
	ObjectKey string
	Size     int64
	SHA256   string
}


// mediaMetadataManifest is a small, deterministic sidecar stored in the private
// metadata bucket on every configured provider. PostgreSQL remains canonical;
// this sidecar is a recovery/inspection copy and contains no credentials or URLs.
type mediaMetadataManifest struct {
	SchemaVersion string `json:"schema_version"`
	OwnerID       string `json:"owner_id"`
	Namespace     string `json:"namespace"`
	ObjectKey     string `json:"object_key"`
	SizeBytes     int64  `json:"size_bytes"`
	ContentType   string `json:"content_type"`
	SHA256        string `json:"sha256"`
}

type verifiedObjectWriter interface {
	PutObjectVerified(context.Context, string, string, []byte, string) error
}

func (r *Replicator) writeMetadataManifest(ctx context.Context, tenantID, namespace, objectKey string, size int64, contentType, checksum string) error {
	manifestBucket := strings.TrimSpace(r.primaryBuckets["media-metadata-private"])
	if manifestBucket == "" {
		return nil // Backward-compatible for isolated unit tests and legacy deployments.
	}
	manifest := mediaMetadataManifest{
		SchemaVersion: "instabox.media-metadata.v1",
		OwnerID: tenantID, Namespace: namespace, ObjectKey: objectKey,
		SizeBytes: size, ContentType: contentType, SHA256: strings.ToLower(checksum),
	}
	payload, err := json.Marshal(manifest)
	if err != nil { return fmt.Errorf("encode media metadata sidecar: %w", err) }
	key := tenantPrefix(tenantID) + objectKey + ".metadata.json"
	type destination struct { name, bucket string; provider Provider }
	destinations := []destination{{name:r.primaryName, bucket:manifestBucket, provider:r.primary}}
	for _, target := range r.targets {
		bucket := strings.TrimSpace(target.buckets["media-metadata-private"])
		if bucket == "" { return fmt.Errorf("%s metadata sidecar bucket is not configured", target.name) }
		destinations = append(destinations, destination{name:target.name, bucket:bucket, provider:target.provider})
	}
	for _, destination := range destinations {
		writer, ok := destination.provider.(verifiedObjectWriter)
		if !ok { return fmt.Errorf("%s provider cannot write verified metadata sidecars", destination.name) }
		if err := writer.PutObjectVerified(ctx, destination.bucket, key, payload, "application/json"); err != nil {
			return fmt.Errorf("write verified metadata sidecar on %s: %w", destination.name, err)
		}
	}
	return nil
}

type ReplicaDownload struct {
	Provider string
	URL      string
}

// ReplicaDownloadURLs returns short-lived server-side download capabilities only
// for replicas whose size, content type and SHA-256 match the authoritative record.
// Callers must proxy the bytes and must never return these URLs to an end user.
func (r *Replicator) ReplicaDownloadURLs(ctx context.Context, tenantID, namespace, objectKey string, size int64, contentType, expectedSHA256 string) ([]ReplicaDownload, error) {
	if r == nil || len(r.targets) != 2 {
		return nil, errors.New("three-provider media replication is not configured")
	}
	namespace = strings.TrimSpace(namespace)
	if !tenantIDPattern.MatchString(tenantID) || !validKey(objectKey) || size < 1 || strings.TrimSpace(contentType) == "" || len(expectedSHA256) != 64 {
		return nil, errors.New("invalid replica download identity or metadata")
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if _, err := hexDecodeSHA256(expectedSHA256); err != nil {
		return nil, err
	}
	scopedKey := tenantPrefix(tenantID) + objectKey
	downloads := make([]ReplicaDownload, 0, len(r.targets))
	for _, target := range r.targets {
		bucket := target.buckets[namespace]
		if bucket == "" {
			continue
		}
		info, err := target.provider.Head(ctx, bucket, scopedKey)
		if err != nil || info.Size != size || !strings.EqualFold(info.ContentType, contentType) {
			continue
		}
		actual, err := target.checksum.SHA256(ctx, bucket, scopedKey, size)
		if err != nil || !strings.EqualFold(actual, expectedSHA256) {
			continue
		}
		url, err := target.provider.PresignGet(ctx, bucket, scopedKey, 5*time.Minute)
		if err != nil || strings.TrimSpace(url) == "" {
			continue
		}
		downloads = append(downloads, ReplicaDownload{Provider: target.name, URL: url})
	}
	if len(downloads) == 0 {
		return nil, errors.New("no checksum-verified media replica is available")
	}
	return downloads, nil
}

// RestorePrimary repairs a missing or checksum-drifted primary object from the
// first replica whose metadata and streamed SHA-256 match the authoritative object.
func (r *Replicator) RestorePrimary(ctx context.Context, tenantID, namespace, objectKey string, size int64, contentType, expectedSHA256 string) error {
	if r == nil || r.primary == nil || len(r.targets) != 2 {
		return errors.New("three-provider media replication is not configured")
	}
	namespace = strings.TrimSpace(namespace)
	primaryBucket := r.primaryBuckets[namespace]
	if primaryBucket == "" || !tenantIDPattern.MatchString(tenantID) || !validKey(objectKey) || size < 1 || strings.TrimSpace(contentType) == "" || len(expectedSHA256) != 64 {
		return errors.New("invalid primary restore identity or metadata")
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if _, err := hexDecodeSHA256(expectedSHA256); err != nil {
		return err
	}
	scopedKey := tenantPrefix(tenantID) + objectKey
	if info, err := r.primary.Head(ctx, primaryBucket, scopedKey); err == nil && info.Size == size && strings.EqualFold(info.ContentType, contentType) {
		if digest, digestErr := r.primary.SHA256(ctx, primaryBucket, scopedKey, size); digestErr == nil && strings.EqualFold(digest, expectedSHA256) {
			return nil
		}
	}
	primaryCopier, ok := r.primary.(copyProvider)
	if !ok {
		return errors.New("primary media provider does not support verified replica restoration")
	}
	var failures []string
	for _, target := range r.targets {
		bucket := target.buckets[namespace]
		if bucket == "" {
			continue
		}
		info, err := target.provider.Head(ctx, bucket, scopedKey)
		if err != nil || info.Size != size || !strings.EqualFold(info.ContentType, contentType) {
			failures = append(failures, target.name+": replica metadata unavailable or mismatched")
			continue
		}
		digest, err := target.checksum.SHA256(ctx, bucket, scopedKey, size)
		if err != nil || !strings.EqualFold(digest, expectedSHA256) {
			failures = append(failures, target.name+": replica checksum mismatch or unavailable")
			continue
		}
		if err := primaryCopier.CopyFromProvider(ctx, target.provider, bucket, scopedKey, primaryBucket, scopedKey, size, contentType, expectedSHA256); err != nil {
			failures = append(failures, target.name+": primary restore copy failed")
			continue
		}
		restored, err := r.primary.Head(ctx, primaryBucket, scopedKey)
		if err != nil || restored.Size != size || !strings.EqualFold(restored.ContentType, contentType) {
			failures = append(failures, target.name+": restored primary metadata verification failed")
			continue
		}
		restoredDigest, err := r.primary.SHA256(ctx, primaryBucket, scopedKey, size)
		if err != nil || !strings.EqualFold(restoredDigest, expectedSHA256) {
			failures = append(failures, target.name+": restored primary checksum verification failed")
			continue
		}
		return nil
	}
	if len(failures) == 0 {
		return errors.New("no media replicas are configured for primary restoration")
	}
	return fmt.Errorf("no checksum-verified replica could restore the primary: %s", strings.Join(failures, "; "))
}

type Replicator struct {
	primaryName string
	primary checksumProvider
	primaryBuckets map[string]string
	targets []struct {
		name string
		provider copyProvider
		checksum checksumProvider
		buckets map[string]string
	}
}

func NewReplicator(primaryName string, primary Provider, primaryBuckets map[string]string, targets []ReplicaTarget) (*Replicator, error) {
	primaryName = strings.TrimSpace(primaryName)
	if primaryName == "" || primary == nil {
		return nil, errors.New("primary media provider name and provider are required")
	}
	checksummer, ok := primary.(checksumProvider)
	if !ok {
		return nil, errors.New("primary media provider must support SHA-256 read-back")
	}
	if len(primaryBuckets) < 2 || strings.TrimSpace(primaryBuckets["media-private"]) == "" || strings.TrimSpace(primaryBuckets["media-public"]) == "" {
		return nil, errors.New("primary private and public media buckets are required")
	}
	normalizedPrimaryBuckets := make(map[string]string, len(primaryBuckets))
	for namespace, bucket := range primaryBuckets {
		namespace = strings.TrimSpace(namespace)
		bucket = strings.TrimSpace(bucket)
		if !namespacePattern.MatchString(namespace) || bucket == "" {
			return nil, errors.New("primary storage namespace and bucket mappings must be valid")
		}
		normalizedPrimaryBuckets[namespace] = bucket
	}
	if len(targets) != 2 {
		return nil, errors.New("exactly two independent media replica targets are required")
	}
	r := &Replicator{
		primaryName: primaryName,
		primary: checksummer,
		primaryBuckets: normalizedPrimaryBuckets,
	}
	seen := map[string]struct{}{strings.ToLower(primaryName): {}}
	for _, target := range targets {
		name := strings.ToLower(strings.TrimSpace(target.Name))
		if name == "" || target.Provider == nil {
			return nil, errors.New("replica provider name and provider are required")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate media provider name %q", name)
		}
		seen[name] = struct{}{}
		copier, ok := target.Provider.(copyProvider)
		if !ok {
			return nil, fmt.Errorf("replica provider %q does not support verified streaming copy", name)
		}
		targetChecksum, ok := target.Provider.(checksumProvider)
		if !ok {
			return nil, fmt.Errorf("replica provider %q does not support SHA-256 reconciliation", name)
		}
		if len(target.Buckets) != len(normalizedPrimaryBuckets) {
			return nil, fmt.Errorf("replica provider %q must map every configured storage namespace", name)
		}
		normalizedTargetBuckets := make(map[string]string, len(target.Buckets))
		for namespace := range normalizedPrimaryBuckets {
			bucket := strings.TrimSpace(target.Buckets[namespace])
			if bucket == "" {
				return nil, fmt.Errorf("replica provider %q is missing storage namespace %q", name, namespace)
			}
			normalizedTargetBuckets[namespace] = bucket
		}
		r.targets = append(r.targets, struct {
			name string
			provider copyProvider
			checksum checksumProvider
			buckets map[string]string
		}{
			name: name,
			provider: copier,
			checksum: targetChecksum,
			buckets: normalizedTargetBuckets,
		})
	}
	return r, nil
}

// Replicate verifies the uploaded primary bytes, then streams the same object to
// each independent replica. A successful result means the destination was read
// back and checksum-verified by its provider implementation.
func (r *Replicator) Replicate(ctx context.Context, tenantID, namespace, objectKey string, size int64, contentType, expectedSHA256 string) ([]ReplicaResult, error) {
	if r == nil || r.primary == nil || len(r.targets) != 2 {
		return nil, errors.New("three-provider media replication is not configured")
	}
	namespace = strings.TrimSpace(namespace)
	primaryBucket := r.primaryBuckets[namespace]
	if primaryBucket == "" {
		return nil, ErrInvalidNamespace
	}
	if !tenantIDPattern.MatchString(tenantID) || !validKey(objectKey) || size < 1 || strings.TrimSpace(contentType) == "" || len(expectedSHA256) != 64 {
		return nil, errors.New("invalid media replication identity or metadata")
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if _, err := hexDecodeSHA256(expectedSHA256); err != nil {
		return nil, err
	}
	scopedKey := tenantPrefix(tenantID) + objectKey
	info, err := r.primary.Head(ctx, primaryBucket, scopedKey)
	if err != nil {
		return nil, fmt.Errorf("verify primary media object: %w", err)
	}
	if info.Size != size || !strings.EqualFold(info.ContentType, contentType) {
		return nil, ErrObjectMismatch
	}
	actual, err := r.primary.SHA256(ctx, primaryBucket, scopedKey, size)
	if err != nil {
		return nil, fmt.Errorf("verify primary media checksum: %w", err)
	}
	if actual != expectedSHA256 {
		return nil, ErrChecksumMismatch
	}
	results := []ReplicaResult{{
		Provider: r.primaryName,
		Bucket: primaryBucket,
		ObjectKey: scopedKey,
		Size: size,
		SHA256: expectedSHA256,
	}}
	for _, target := range r.targets {
		bucket := target.buckets[namespace]
		copyNeeded := false
		destinationInfo, headErr := target.provider.Head(ctx, bucket, scopedKey)
		switch {
		case errors.Is(headErr, ErrNotFound):
			copyNeeded = true
		case headErr != nil:
			return results, fmt.Errorf("inspect media replica on %s: %w", target.name, headErr)
		case destinationInfo.Size != size || !strings.EqualFold(destinationInfo.ContentType, contentType):
			copyNeeded = true
		default:
			destinationDigest, digestErr := target.checksum.SHA256(ctx, bucket, scopedKey, size)
			if digestErr != nil {
				return results, fmt.Errorf("verify media replica checksum on %s: %w", target.name, digestErr)
			}
			copyNeeded = destinationDigest != expectedSHA256
		}
		if copyNeeded {
			if err := target.provider.CopyFromProvider(ctx, r.primary, primaryBucket, scopedKey, bucket, scopedKey, size, contentType, expectedSHA256); err != nil {
				return results, fmt.Errorf("repair media replica on %s: %w", target.name, err)
			}
		}
		results = append(results, ReplicaResult{
			Provider: target.name,
			Bucket: bucket,
			ObjectKey: scopedKey,
			Size: size,
			SHA256: expectedSHA256,
		})
	}
	if err := r.writeMetadataManifest(ctx, tenantID, namespace, objectKey, size, contentType, expectedSHA256); err != nil {
		return results, err
	}
	return results, nil
}

// Delete removes a tenant-scoped object from the primary and every replica.
// Missing objects are treated as already deleted; any other provider error keeps
// the durable delete job retryable.
func (r *Replicator) Delete(ctx context.Context, tenantID, namespace, objectKey string) error {
	if r == nil || r.primary == nil || len(r.targets) != 2 {
		return errors.New("three-provider media replication is not configured")
	}
	namespace = strings.TrimSpace(namespace)
	key := strings.TrimSpace(objectKey)
	if r.primaryBuckets[namespace] == "" || !tenantIDPattern.MatchString(tenantID) || !validKey(key) {
		return errors.New("invalid media deletion identity")
	}
	scopedKey := tenantPrefix(tenantID) + key
	type destination struct {
		name string
		provider Provider
		bucket string
	}
	destinations := []destination{{name:r.primaryName,provider:r.primary,bucket:r.primaryBuckets[namespace]}}
	for _, target := range r.targets {
		destinations = append(destinations,destination{name:target.name,provider:target.provider,bucket:target.buckets[namespace]})
	}
	var failures []string
	for _, target := range destinations {
		err := target.provider.Delete(ctx,target.bucket,scopedKey)
		if err != nil && !errors.Is(err,ErrNotFound) {
			failures = append(failures,fmt.Sprintf("%s: %v",target.name,err))
		}
	}
	metadataKey := scopedKey + ".metadata.json"
	if metadataBucket := strings.TrimSpace(r.primaryBuckets["media-metadata-private"]); metadataBucket != "" {
		metadataDestinations := []destination{{name:r.primaryName,provider:r.primary,bucket:metadataBucket}}
		for _, target := range r.targets {
			metadataDestinations = append(metadataDestinations,destination{name:target.name,provider:target.provider,bucket:target.buckets["media-metadata-private"]})
		}
		for _, target := range metadataDestinations {
			if target.bucket == "" { failures = append(failures,target.name+": metadata bucket is not configured"); continue }
			err := target.provider.Delete(ctx,target.bucket,metadataKey)
			if err != nil && !errors.Is(err,ErrNotFound) { failures = append(failures,fmt.Sprintf("%s metadata: %v",target.name,err)) }
		}
	}
	if len(failures)>0 {
		return fmt.Errorf("media replica deletion incomplete: %s",strings.Join(failures,"; "))
	}
	return nil
}

func hexDecodeSHA256(value string) ([]byte, error) {
	if len(value) != 64 {
		return nil, errors.New("expected SHA-256 must be 64 hexadecimal characters")
	}
	decoded := make([]byte, 32)
	for i := 0; i < len(value); i += 2 {
		var b byte
		for _, c := range value[i:i+2] {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				b |= byte(c-'a') + 10
			default:
				return nil, errors.New("expected SHA-256 must be lowercase hexadecimal")
			}
		}
		decoded[i/2] = b
	}
	return decoded, nil
}
