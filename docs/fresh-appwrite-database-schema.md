# Fresh Appwrite Database schema (v1)

Create these collections in a new Appwrite Database for the fresh Testagram project. Use a separate database per environment. The Go Backend service key should have only the scopes required to manage documents in this database. Never ship it to the frontend.

All collections use document security enabled with no broad collection-level public read/write permissions. The Backend validates the Appwrite JWT through the Account endpoint, then performs privileged operations using the server key. Browser/mobile clients do not query these collections directly in v1.

## Collections

| Collection | Initial attributes | Required indexes |
|---|---|---|
| profiles | user_id string(36), handle string(30), display_name string(80), bio string(280), avatar_object_id string(128), created_at datetime, updated_at datetime | unique user_id; unique handle |
| account_settings | user_id string(36), profile_visibility enum, message_privacy enum, created_at datetime, updated_at datetime | unique user_id |
| follows | follower_id string(36), followed_id string(36), state enum, created_at datetime | unique follower_id+followed_id; followed_id+state; follower_id+state |
| blocks | blocker_id string(36), blocked_id string(36), created_at datetime | unique blocker_id+blocked_id; blocked_id |
| posts | author_id string(36), body string(10000), visibility enum, status enum, created_at datetime, updated_at datetime | author_id+created_at; visibility+status+created_at |
| post_media | post_id string(36), owner_id string(36), object_id string(128), media_type enum, position integer, created_at datetime | post_id+position; unique object_id |
| likes | post_id string(36), user_id string(36), created_at datetime | unique post_id+user_id; user_id+created_at |
| comments | post_id string(36), author_id string(36), parent_id string(36), body string(5000), status enum, created_at datetime | post_id+created_at; author_id+created_at |
| reposts | post_id string(36), user_id string(36), created_at datetime | unique post_id+user_id; user_id+created_at |
| bookmarks | post_id string(36), user_id string(36), created_at datetime | unique post_id+user_id; user_id+created_at |
| notifications | recipient_id string(36), actor_id string(36), event_type string(64), target_id string(128), read_at datetime, created_at datetime | recipient_id+created_at; recipient_id+read_at |
| conversations | kind enum, created_by string(36), created_at datetime, updated_at datetime | updated_at |
| conversation_members | conversation_id string(36), user_id string(36), joined_at datetime, last_read_at datetime | unique conversation_id+user_id; user_id+joined_at |
| messages | conversation_id string(36), sender_id string(36), body string(10000), media_object_id string(128), created_at datetime, deleted_at datetime | conversation_id+created_at; sender_id+created_at |
| media_objects | owner_id string(36), object_id string(128), object_key string(512), namespace string(32), bytes_decimal string(20), mime_type string(128), sha256 string(64), visibility enum, status enum, created_at datetime | unique object_id; owner_id+created_at; status+created_at |
| storage_reservations | owner_id string(36), object_id string(128), object_key string(128), bytes_decimal string(20), reserved_kib integer, content_type string(128), sha256_expected string(64), sha256_actual string(64), status enum (reserved, verifying, uploaded_primary, protected, expiring, expired), expires_at datetime, lease_expires_at datetime, primary_verified_at datetime, protected_at datetime, expired_at datetime | unique object_id; status+expires_at; status+expires_at+lease_expires_at; owner_id+status |
| storage_quotas | limit_kib integer, used_kib integer, reserved_kib integer, updated_at datetime | no indexes required; deterministic document IDs: platform and each Appwrite user ID |
| storage_nodes | node_id string(128), node_type enum, storage_only boolean, storage_enabled boolean, compute_enabled boolean, volume_identity string(128), total_bytes_decimal string(20), free_bytes_decimal string(20), reserved_bytes_decimal string(20), verified_bytes_decimal string(20), status enum, last_heartbeat datetime | unique node_id; status+last_heartbeat; storage_enabled+status |
| storage_replicas | object_id string(128), provider string(16), bucket string(63), object_key string(512), bytes_decimal string(20), sha256 string(64), state enum, verified_at datetime | object_id+state; provider+state |

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
