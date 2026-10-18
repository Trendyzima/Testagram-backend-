# Virtual media upload integrity contract

For virtual-storage uploads, the completion request now requires:

```json
{
  "key": "the-upload-intent-key",
  "visibility": "private",
  "content_type": "image/jpeg",
  "checksum_sha256": "64 lowercase-or-uppercase hexadecimal characters"
}
```

The client must compute SHA-256 over the exact file bytes it uploaded and send the digest as `checksum_sha256` when completing the upload. The API first validates provider-observed object metadata, then streams the stored object through a bounded server-side GET and computes SHA-256 over the actual bytes. It does not treat S3 ETag as a checksum. A mismatch returns `422 MEDIA_CHECKSUM_MISMATCH` and queues cleanup; an absent checksum returns `400 MEDIA_CHECKSUM_REQUIRED`.

This change intentionally makes the virtual-storage completion contract stricter. Client applications must add checksum calculation and include the field before virtual storage is enabled for users. Legacy-media completion retains its existing contract until that path is migrated.

## Important remaining limitation

This checksum check happens at completion, after the direct-to-object-store upload. The existing presigned PUT contract still does not enforce the maximum byte count while bytes are in transit. Provider-observed size is checked at completion and the checksum GET is bounded, but this is not a substitute for transfer-time enforcement. Before production enablement, use a storage-provider POST policy with a signed `content-length-range` where supported, or a bounded upload gateway that aborts as soon as the limit is exceeded. Test the chosen approach against the actual client, reverse proxy and S3-compatible provider.
