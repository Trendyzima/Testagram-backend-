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

## Single-use intent state machine

The API atomically changes a virtual-storage intent from `issued` to `uploading` **before it reads the request body**. A second or replayed PUT receives `409 MEDIA_INTENT_ALREADY_USED` and cannot overwrite the object while the first transfer is in progress. After the storage provider accepts the complete bounded body, the intent becomes `uploaded`; completion accepts that state and verifies the stored object plus its SHA-256 before marking the intent `completed`.

If a process crashes or a transfer never finishes, an `uploading` claim is considered stale after 2 hours and the media janitor schedules/removes any partial object. Failed uploads do not automatically reopen the same intent: clients should request a new intent. Migration `010_media_upload_claim.sql` adds these states; the operator backup/apply script includes migration 010 and requires an explicit `APPLY-008-010` confirmation.

## Bounded upload gateway

For virtual-storage intents, the API returns a relative upload URL such as `/api/media/upload?key=...`, not the provider's signed object-store URL. The client must send the file bytes using **PUT** to that URL with the exact `Content-Type` declared in the intent. The API authenticates the user, checks ownership and expiry of the intent, and streams the request through a byte-limited reader into a temporary file. It rejects the request as soon as it exceeds the reservation and requires the received byte count to equal the reserved size before forwarding the file to object storage. Provider URLs remain server-side.

Example browser flow for files small enough to fit comfortably in memory:

```js
const bytes = await file.arrayBuffer();
const digest = await crypto.subtle.digest("SHA-256", bytes);
const checksum_sha256 = [...new Uint8Array(digest)]
  .map((b) => b.toString(16).padStart(2, "0")).join("");

const uploaded = await fetch(new URL(intent.url, API_BASE_URL), {
  method: "PUT",
  headers: { "Content-Type": file.type },
  body: file,
  credentials: "include",
});
if (!uploaded.ok) throw new Error("Upload failed");

await fetch(new URL("/api/media/complete", API_BASE_URL), {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  credentials: "include",
  body: JSON.stringify({
    key: intent.key,
    visibility: intent.visibility,
    content_type: file.type,
    checksum_sha256,
  }),
});
```

For large video files, do not use `file.arrayBuffer()` in the main UI thread: it duplicates the entire file in memory. Use an incremental SHA-256 implementation in a Web Worker or native mobile equivalent, and test memory use on lower-end devices. The API currently requires the digest before completion.

The gateway temporarily stores each incoming upload on disk before forwarding it to object storage. Operators must size and monitor the API host's temporary disk, configure reverse-proxy request-body/time limits to permit the intended maximum, and load-test concurrent large uploads. Request context cancellation removes the temporary file on normal handler exit, but crash recovery must also include stale `testagram-upload-*` spool-file cleanup.

## Release gates

- Test oversized fixed-length and chunked transfer requests and confirm the object store never receives them.
- Test exact-size uploads, wrong content type, expired intent, cross-account intent use, interrupted client transfer, storage timeout, and checksum mismatch.
- Verify the client uses the API base URL to resolve the relative intent URL and sends credentials correctly.
- Confirm proxy limits and temporary disk capacity on the actual VPS before enabling virtual storage.
- The legacy-media upload path is unchanged; this gateway is used for virtual-storage intents only.
