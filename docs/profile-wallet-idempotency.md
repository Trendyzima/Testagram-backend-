# Testagram profile resolution and M-Pesa idempotency

## Profile username resolution

The authenticated `testagram.social.profile.resolve` capability accepts a public Testagram username. It resolves the canonical account UUID only when the viewer is allowed to discover the profile:
- the account is active;
- the profile is the viewer's own account, discoverable by username, or visible through an accepted follow relationship; and
- neither account has blocked the other.

The result is passed through the canonical profile-read handler. This capability does not grant access to private posts and does not bypass the profile endpoint's authorization rules.

## M-Pesa top-up idempotency

A top-up idempotency key is scoped to the authenticated owner and bound to the exact normalized phone, whole-KES amount and currency. Reusing the key with a different payload returns `IDEMPOTENCY_CONFLICT`; it never reuses another payload's checkout request.

The `testagram.wallet.mpesa.status` capability accepts either the provider checkout request ID or the owner's original idempotency key. This lets a client recover a request when the network response was lost before the checkout ID reached the device.

A repeated request with the same key returns the existing request state. A pending request without a recoverable checkout ID is not submitted to Safaricom again; it remains in reconciliation. Only a provider-verified callback/query followed by a committed PostgreSQL settlement transaction credits the wallet.

The Flutter client must persist the request key and exact payload before the network call, reuse the same key for an ambiguous retry, and block a different deposit until the prior attempt is resolved. STK Push acceptance is not settlement. M-Pesa B2C withdrawals are outside this capability and remain unavailable.

## Operational boundary

This document describes application contracts only. It does not authorize production routing changes, M-Pesa payout activation, or a production deployment. Staging acceptance must use isolated provider credentials and a non-production callback endpoint.
