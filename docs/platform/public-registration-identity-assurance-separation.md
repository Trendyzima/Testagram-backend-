# Public registration and optional identity assurance

## Public account creation

The native `POST /api/auth/signup` endpoint is the ordinary self-service
registration path. It creates a native credential record, queues email
verification, and does not call the identity evidence or reviewer workflows.
The account is not usable for sign-in until the one-time email verification
code or link is successfully consumed.

Identity evidence collection, human review, and two-person approval are a
separate assurance workflow. They are not blanket prerequisites for creating
or email-verifying an ordinary Testagram account.

## Privilege boundaries

Removing identity review as a registration prerequisite does not grant
identity-verified status, staff status, administrator status, or any elevated
permission. Every sensitive endpoint must continue to enforce its own
server-side authorization, step-up authentication, and identity assurance
requirements. The approved-identity provisioning endpoints remain separate
and retain their evidence, independent-review, dual-approval, and replay
protections.

## Regression requirements

- Public signup with malformed JSON reaches normal request validation.
- Public signup with a syntactically valid request reaches password validation
  (or the normal auth-store readiness boundary), never
  `IDENTITY_REVIEW_REQUIRED`.
- Unverified accounts cannot sign in.
- Email verification creates the basic profile and remains single-use.
- Existing identity-dependent sensitive operations remain protected.
- Staging smoke tests probe signup with an intentionally short password and
  expect `400 INVALID_PASSWORD`, which proves the route is open without
  creating an account or queuing email.

This is a platform policy change. Do not restore a blanket signup gate as a
temporary workaround for identity-review service outages.
