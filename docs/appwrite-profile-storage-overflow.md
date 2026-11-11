# Appwrite-first profile image storage with Instabox overflow

## Policy

Profile pictures and avatars use Appwrite Storage first. The target free capacity is 2 GiB, but Testagram must not wait for Appwrite to return quota errors. The default policy reserves 256 MiB and stops assigning new uploads to Appwrite at 1,792 MiB of effective usage.

The quota calculation is:

baseline Appwrite usage + Testagram-tracked Appwrite bytes + active upload reservations + proposed upload size.

The selection is serialized in PostgreSQL so concurrent avatar uploads cannot overbook the Appwrite tier. When the high-water mark would be crossed, the new upload is assigned to Instabox VPS storage. The overflow state is sticky until an operator explicitly resets it after reconciling usage; this avoids oscillation when files are deleted or Appwrite's usage counter is delayed.

## Placement metadata

Every profile image records its provider, bucket, object ID, owner, byte size, content type and SHA-256. Profile records store a stable asset reference, not a provider URL or credential. Appwrite files and Instabox objects remain private to the storage service; the Testagram API/CDN issues viewer-safe delivery URLs.

## Important integration requirements

- Appwrite service key is server-only and restricted to Storage scopes; never ship it in Flutter.
- Read current Appwrite bucket usage during deployment and seed `baseline_used_bytes`; the Testagram ledger alone cannot see files uploaded outside Testagram.
- Appwrite upload and Instabox overflow both pass the same image validation, byte cap, SHA-256 and malware/content checks.
- Profile update is committed only after upload verification; replace old image with a tombstone and asynchronous delete only after the new asset is ready.
- Testagram CDN must fetch Appwrite-backed avatar objects as a private origin as well as Instabox objects, or the API must provide a safe view URL. The public delivery path must still be Testagram CDN.
- Existing avatars remain valid during migration; never rewrite all profile rows until their object locations are verified.
- This policy is for profile images and avatars only. Post/video media uses the separate three-copy Instabox + R2 + B2 durability workflow.

## Rollout gates

1. Configure Appwrite profile bucket and least-privilege server API key.
2. Seed Appwrite baseline usage from provider-side usage, then enable the policy.
3. Test concurrent reservations at the high-water boundary.
4. Test Appwrite quota/provider outage and automatic Instabox fallback.
5. Test avatar replacement, deletion, checksum mismatch, and CDN delivery for both providers.
6. Confirm storage dashboard reflects actual provider bytes, not only Testagram's ledger.

No production Appwrite bucket, API key, storage setting or live avatar is changed by this branch.
