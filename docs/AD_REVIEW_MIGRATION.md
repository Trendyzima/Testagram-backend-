# Native advertising review

The ad-review API is owned by Testagram Backend. It does not call Supabase RPCs or depend on Supabase Auth.

## Security model

- Every review and staff-permission action requires an Appwrite JWT in the Bearer header.
- The Backend validates the Appwrite account and then reads the current Appwrite session.
- The current session must report at least one second factor and an `mfaUpdatedAt` timestamp no older than five minutes.
- The reviewer must be the configured platform owner or have an active `platform.ads` permission in `testagram_identity.staff_permissions`.
- Set `TESTAGRAM_PLATFORM_OWNER_USER_ID` to the canonical UUID from `testagram_identity.appwrite_accounts`. This is the bootstrap owner; do not use an Appwrite string ID here.

## Routes

- `GET /api/admin/ads/review-queue?status=pending&limit=100`
- `POST /api/admin/ads/{campaign_id}/status` with `{"status":"active"|"paused"}`
- `POST /api/admin/ads/{campaign_id}/review` with `{"decision":"approved"|"rejected","note":"..."}`
- `POST /api/admin/staff/permissions` with `{"user_id":"...","permission_key":"platform.ads","enabled":true|false}`

The owner must complete Appwrite MFA before granting or revoking a staff permission. Rejections require a reason. Each decision is written to `testagram_local.ad_review_audit` in the same transaction as the campaign update.

## Data migration boundary

Migration `20261122_native_ad_review.sql` creates the Backend-owned advertiser, campaign, creative, permission and audit tables. It does not fabricate or silently discard legacy campaign data. Existing campaigns and creatives must be imported into these tables before the new queue can display them. Do not switch the production admin UI to this API until the legacy data import has been reconciled and verified.
