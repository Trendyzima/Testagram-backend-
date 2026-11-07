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
| media_objects | owner_id string(36), object_id string(128), provider string(16), bucket string(63), object_key string(512), bytes_decimal string(20), mime_type string(128), sha256 string(64), visibility enum, status enum, created_at datetime | unique object_id; owner_id+created_at; status+created_at |
| storage_reservations | owner_id string(36), object_id string(128), reserved_bytes_decimal string(20), expires_at datetime, state enum | unique object_id; expires_at+state |
| storage_nodes | node_id string(128), node_type enum, storage_only boolean, storage_enabled boolean, compute_enabled boolean, volume_identity string(128), total_bytes_decimal string(20), free_bytes_decimal string(20), reserved_bytes_decimal string(20), verified_bytes_decimal string(20), status enum, last_heartbeat datetime | unique node_id; status+last_heartbeat; storage_enabled+status |
| storage_replicas | object_id string(128), provider string(16), node_id string(128), physical_key string(512), bytes_decimal string(20), sha256 string(64), state enum, verified_at datetime | object_id+state; provider+state |

## Implementation constraints

- Appwrite attribute and index creation is asynchronous; wait for each operation to become available before creating dependent indexes or enabling API traffic.
- Enforce Appwrite's per-attribute size and index-key limits when provisioning. If a composite index is too large, use compact normalized fields or a deterministic relationship document ID.
- Use deterministic IDs for relationship records (for example, a stable hash of both IDs) to make retries idempotent.
- Enforce handle uniqueness through a unique index; never rely on a preflight query alone.
- Appwrite Database does not provide relational joins or multi-document transactions equivalent to PostgreSQL. Design write workflows as idempotent state machines with compensating actions/outbox records where needed. Do not claim cross-collection atomicity.
- Store media bytes only in object storage, never in Appwrite documents.
- Appwrite documents are not the object-storage placement ledger unless all provider/node state transitions are persisted and reconciled by the Backend.
- Do not provision public permissions on messages, notifications, account settings, storage records or private profiles.

## Capacity representation

Store byte counts as base-10 decimal strings in Appwrite documents (for example, bytes_decimal), not Appwrite integer attributes. The 150 GB pool and per-user quotas exceed signed 32-bit integer ranges. Parse and validate these values as int64 in the trusted Go Backend; reject negative, malformed, overflowing or non-canonical values. Keep arithmetic and reservation locking in the storage control plane rather than relying on lexicographic string sorting.
