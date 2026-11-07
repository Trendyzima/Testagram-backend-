# Fresh Appwrite Database schema (v1)

Create these collections in a new Appwrite Database for the fresh Testagram project. Use a separate database per environment. The Go Backend service key should have only the scopes required to manage documents in this database. Never ship it to the frontend.

All collections use document security enabled with no broad collection-level public read/write permissions. The Backend validates the Appwrite JWT through the Account endpoint, then performs privileged operations using the server key. Browser/mobile clients do not query these collections directly in v1.

## Collections

| Collection | Initial attributes | Required indexes |
|---|---|---|
| profiles | user_id string(36), handle string(30), display_name string(80), bio string(280), avatar_object_id string(128), created_at datetime, updated_at datetime | unique user_id; unique handle |
| account_settings | user_id string(36), profile_visibility enum (public, private), message_privacy enum, created_at datetime, updated_at datetime | unique user_id |
| follows | follower_id string(36), followed_id string(36), state enum (pending, accepted, rejected), created_at datetime | unique follower_id+followed_id; followed_id+state; follower_id+state; follower_id+state+created_at; follower_id+followed_id+state |
| blocks | blocker_id string(36), blocked_id string(36), created_at datetime | unique blocker_id+blocked_id; blocked_id; blocker_id+created_at; blocked_id+created_at; blocked_id+blocker_id |
| posts | author_id string(36), body string(10000), visibility enum (public, followers, private), status enum (draft, published, deleted), sensitive boolean, created_at datetime, updated_at datetime | author_id+created_at; status+created_at; visibility+status+created_at |
| stories | author_id string(36), body string(10000), visibility enum, status enum (active, expired, deleted), expires_at datetime, created_at datetime, updated_at datetime, expired_at datetime | status+expires_at; author_id+created_at |
| post_media | post_id string(36), owner_id string(36), object_id string(128), media_type enum (image, video, audio), position integer, created_at datetime | post_id+position; unique object_id+created_at |
| likes | post_id string(36), user_id string(36), created_at datetime | unique post_id+user_id; user_id+created_at |
| comments | post_id string(36), author_id string(36), parent_id string(36), body string(5000), status enum, created_at datetime | post_id+created_at; author_id+created_at |
| reposts | post_id string(36), user_id string(36), created_at datetime | unique post_id+user_id; user_id+created_at |
| bookmarks | post_id string(36), user_id string(36), created_at datetime | unique post_id+user_id; user_id+created_at |
| notifications | recipient_id string(36), actor_id string(36), event_type string(64), target_id string(128), read_at datetime, created_at datetime | recipient_id+created_at; recipient_id+read_at |
| conversations | kind enum, created_by string(36), created_at datetime, updated_at datetime | updated_at |
| conversation_members | conversation_id string(36), user_id string(36), joined_at datetime, last_read_at datetime | unique conversation_id+user_id; user_id+joined_at |
| messages | conversation_id string(36), sender_id string(36), body string(10000), media_object_id string(128), created_at datetime, deleted_at datetime | conversation_id+created_at; sender_id+created_at |
| media_objects | owner_id string(36), object_id string(128), object_key string(512), namespace string(32), bytes_decimal string(20), mime_type string(128), sha256 string(64), visibility enum, status enum, scope enum (post_media, story_media, direct_message, profile_media, avatar), retention_class enum (permanent, story, user_deleted), retention_scope enum, lifecycle_state enum (active, recoverable, purging, expired, purged, archived), deleted_at datetime, story_expires_at datetime, archive_state enum, archived_at datetime, archive_after datetime, delete_after datetime, retention_policy_version string(32), lifecycle_lease_until datetime, lifecycle_attempts integer, last_accessed_at datetime, created_at datetime | unique object_id; owner_id+created_at; status+created_at; retention_class+created_at; created_at; deleted_at; lifecycle_state+archive_after; lifecycle_state+delete_after |
| storage_reservations | owner_id string(36), object_id string(128), object_key string(128), bytes_decimal string(20), reserved_kib integer, content_type string(128), sha256_expected string(64), sha256_actual string(64), status enum (reserved, verifying, uploaded_primary, protected, expiring, expired), expires_at datetime, lease_expires_at datetime, primary_verified_at datetime, protected_at datetime, expired_at datetime | unique object_id; status+expires_at; status+expires_at+lease_expires_at; owner_id+status |
| storage_quotas | limit_kib integer, used_kib integer, reserved_kib integer, updated_at datetime | no indexes required; deterministic document IDs: platform and each Appwrite user ID |
| storage_nodes | node_id string(128), node_type enum, storage_only boolean, storage_enabled boolean, compute_enabled boolean, volume_identity string(128), total_bytes_decimal string(20), free_bytes_decimal string(20), reserved_bytes_decimal string(20), verified_bytes_decimal string(20), status enum, last_heartbeat datetime | unique node_id; status+last_heartbeat; storage_enabled+status |
| storage_replicas | object_id string(128), provider enum (b2, r2, archive, node), bucket string(63), object_key string(512), bytes_decimal string(20), sha256 string(64), state enum (verified, degraded, deleting, deleted), verified_at datetime, deleted_at datetime | object_id+state; provider+state |
| media_lifecycle_audit | job_key string(64), object_id string(128), action string(32), outcome string(32), provider string(32), bytes_decimal string(20), sha256 string(64), details string(1000), created_at datetime | object_id+created_at; job_key+created_at |
| story_media | story_id string(36), object_id string(128), position integer, created_at datetime | story_id+position; object_id+created_at |
| retention_jobs | job_key string(64), object_id string(128), action enum (expire_story, archive, purge_deleted), policy_version string(32), due_at datetime, status enum (pending, running, retry, completed), worker_id string(128), lease_token string(64), lease_until datetime, attempts integer, last_error string(1000), created_at datetime, updated_at datetime, completed_at datetime | unique job_key; status+due_at; status+lease_until; object_id+action |

The feed readiness probe checks the collections and composite indexes used by its queries; Backend /readyz remains unavailable until these indexes are usable. The feed reader also requires a composite `posts(status, created_at DESC)` index for the published-post scan. The Backend separately loads candidate-scoped follow and block queries and fails closed if those relationship collections cannot be read.

## Implementation constraints

- Appwrite attribute and index creation is asynchronous; wait for each operation to become available before creating dependent indexes or enabling API traffic.
- Enforce Appwrite's per-attribute size and index-key limits when provisioning. If a composite index is too large, use compact normalized fields or a deterministic relationship document ID.
- Use deterministic IDs for relationship records (for example, a stable hash of both IDs) to make retries idempotent.
- Enforce handle uniqueness through a unique index; never rely on a preflight query alone.
- Appwrite DocumentsDB supports transactions for document writes and atomic numeric increments/decrements. The fresh storage lifecycle uses a single transaction to reserve quota on both the platform and user quota documents and create the reservation; it also uses a transaction to move reserved quota to used quota and commit the media/replica ledger. Reservation state changes use transactional reads and writes to prevent completion and expiry cleanup from racing. Handle commit conflicts by retrying the complete idempotent operation, never by assuming partial success.
- Transactions do not make object-store writes atomic with Appwrite. Keep the upload state machine durable (reserved → uploaded_primary → protected) and reconcile provider objects after timeouts or process restarts. Never mark an object protected before B2 and R2 have both passed read-back SHA-256 verification.
- Store media bytes only in object storage, never in Appwrite documents.
- Appwrite documents are not the object-storage placement ledger unless all provider/node state transitions are persisted and reconciled by the Backend.
- Do not provision public permissions on messages, notifications, account settings, storage records or private profiles.

## Capacity representation

Store exact object byte counts as base-10 decimal strings (bytes_decimal). For atomic quota accounting, store rounded-up KiB units in Appwrite integer attributes (limit_kib, used_kib, reserved_kib). The 150,000,000,000-byte pool is exactly 146,484,375 KiB, which fits a signed 32-bit integer. Round every reservation up to the next KiB so accounting never undercounts. The platform quota document must have ID platform, limit_kib=146484375, used_kib=0, and reserved_kib=0 at initial provisioning. The fresh API bootstraps the platform quota document at startup when media is enabled and creates a per-user quota document on first upload using TESTAGRAM_STORAGE_PER_USER_BYTES (default 2 GiB). Existing quota documents are never reset or overwritten by bootstrap. Never allow used_kib + reserved_kib to exceed limit_kib. The storage_quotas collection and its integer attributes must exist before media is enabled.


## Retention job and archive safeguards

Retention jobs are deterministic (`rt_` plus a stable hash of the full policy job key) and idempotently enqueued in `retention_jobs`. Workers claim a job transactionally with a cryptographically random 256-bit lease token and expiry; completion/retry must present the same unexpired token. Expired `running` leases can be reclaimed. The trusted dispatcher is `cmd/fresh-retention-worker`; it processes one scheduler-supplied job ID and refuses to run while retention is disabled or dry-run is true. Stories are first marked `expired` in the `stories` collection, and media is only eligible for deletion after all live `post_media` and `story_media` references are checked. Configure Appwrite indexes and attributes exactly as listed before enabling any worker.

The independent cold archive uses `ARCHIVE_S3_*` credentials and a bucket distinct from the active B2 bucket. The archive worker streams B2 to the archive provider, verifies destination size/content type and independently reads back SHA-256 before recording `storage_replicas` with provider `archive` and marking `media_objects.archive_state=verified`. This worker does not delete B2/R2 copies or reclaim quota. The authenticated `POST /v1/media/{objectID}/restore` endpoint restores B2 then R2 from a verified archive replica and checks each destination before updating the ledger. Active-replica reclamation remains separate.

Retention must stay disabled and dry-run by default. Never run archive/story expiry against pre-rollout records; never purge user-deleted media before the 30-day recovery deadline. Direct messages, avatars, profile media, text-only posts, and legacy Supabase records are out of scope.

The story and user-deletion workers set `media_objects.lifecycle_state=purging` before replica deletion. Attachment writers must transactionally reject new references to purging media; that attachment-writer guard is a required pre-production integration gate. The purge worker only reclaims quota after all listed replica documents are `deleted` and are re-read in the quota-release transaction. A node replica currently blocks the purge because node transport/deletion is not yet implemented.
