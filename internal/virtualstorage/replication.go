package virtualstorage

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	if len(primaryBuckets) != 2 || strings.TrimSpace(primaryBuckets["media-private"]) == "" || strings.TrimSpace(primaryBuckets["media-public"]) == "" {
		return nil, errors.New("primary private and public media buckets are required")
	}
	if len(targets) != 2 {
		return nil, errors.New("exactly two independent media replica targets are required")
	}
	r := &Replicator{
		primaryName: primaryName,
		primary: checksummer,
		primaryBuckets: map[string]string{
			"media-private": strings.TrimSpace(primaryBuckets["media-private"]),
			"media-public": strings.TrimSpace(primaryBuckets["media-public"]),
		},
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
		if strings.TrimSpace(target.Buckets["media-private"]) == "" || strings.TrimSpace(target.Buckets["media-public"]) == "" {
			return nil, fmt.Errorf("replica provider %q requires private and public bucket mappings", name)
		}
		r.targets = append(r.targets, struct {
			name string
			provider copyProvider
			buckets map[string]string
		}{
			name: name,
			provider: copier,
			checksum: targetChecksum,
			buckets: map[string]string{
				"media-private": strings.TrimSpace(target.Buckets["media-private"]),
				"media-public": strings.TrimSpace(target.Buckets["media-public"]),
			},
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
