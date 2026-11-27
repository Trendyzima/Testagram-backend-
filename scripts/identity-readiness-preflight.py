#!/usr/bin/env python3
"""Staging-only, no-application-write preflight for native identity registration.

The request deliberately contains invalid applicant data. The Backend checks
WebAuthn/Admin Console, PostgreSQL, private evidence storage and policy
configuration before validating the body. A 400 INVALID_REGISTRATION_APPLICATION
therefore proves the registration prerequisites passed without creating an
application, evidence object, account or email.
"""
from __future__ import annotations

import base64
import json
import os
import secrets
import sys
import urllib.error
import urllib.parse
import urllib.request


PRODUCTION_HOSTS = {
    "api.testagram.site", "testagram.site", "www.testagram.site",
    "media.testagram.site", "cdn.testagram.site",
}

BLOCKERS = {
    "IDENTITY_REVIEW_MFA_UNAVAILABLE": (
        "WebAuthn identity-review readiness is not satisfied. Verify the four "
        "independent staff credentials, two reviewer roles, two approver roles, "
        "and the live private Admin Console readiness attestation."
    ),
    "IDENTITY_SERVICE_UNAVAILABLE": (
        "Native identity dependencies are unavailable. Verify PostgreSQL and "
        "the private Data Centre evidence lifecycle configuration."
    ),
    "IDENTITY_STORAGE_UNAVAILABLE": (
        "Private evidence storage health failed. Verify Data Centre reachability, "
        "provider credentials, bucket policy and replication health."
    ),
    "REGISTRATION_POLICY_NOT_CONFIGURED": (
        "Set TESTAGRAM_TERMS_VERSION and TESTAGRAM_PRIVACY_VERSION in the "
        "staging Backend runtime."
    ),
    "IDENTITY_STORE_UNAVAILABLE": (
        "Identity database state is unavailable. Verify migrations, schema "
        "permissions and PostgreSQL connectivity."
    ),
    "RATE_LIMIT_UNAVAILABLE": (
        "Registration rate-limit persistence is unavailable. Verify the native "
        "authentication database schema and write permissions."
    ),
}


def validate_staging_origin(raw: str) -> str:
    parsed = urllib.parse.urlsplit(raw)
    host = (parsed.hostname or "").lower().rstrip(".")
    if (
        parsed.scheme != "https"
        or not host
        or parsed.username
        or parsed.password
        or parsed.query
        or parsed.fragment
        or parsed.path not in ("", "/")
        or host in PRODUCTION_HOSTS
    ):
        raise ValueError(
            "TESTAGRAM_STAGING_API_URL must be a clean HTTPS staging origin, "
            "not a canonical production Testagram host."
        )
    return raw.rstrip("/")


def classify_response(status: int, envelope: object) -> tuple[bool, str]:
    if not isinstance(envelope, dict):
        return False, "Backend returned a non-object JSON envelope."
    error = envelope.get("error")
    code = error.get("code") if isinstance(error, dict) else None
    if status == 400 and code == "INVALID_REGISTRATION_APPLICATION":
        return True, "All native identity registration prerequisites passed."
    if isinstance(code, str) and code in BLOCKERS:
        return False, BLOCKERS[code]
    if status == 400 and code in {"INVALID_JSON", "IDEMPOTENCY_KEY_REQUIRED"}:
        return False, "The staging API route responded, but the preflight contract changed."
    return False, f"Unexpected registration preflight response (HTTP {status}, code {code or 'UNKNOWN'})."


def main() -> int:
    if os.environ.get("CONFIRM_STAGING_ONLY", "").lower() != "true":
        print("::error::Set confirm_staging_only=true after confirming this is an isolated staging API.", file=sys.stderr)
        return 1
    raw_base = os.environ.get("TESTAGRAM_STAGING_API_URL", "").strip()
    if not raw_base:
        print("::error::Set the staging environment variable TESTAGRAM_STAGING_API_URL.", file=sys.stderr)
        return 1
    try:
        base = validate_staging_origin(raw_base)
    except ValueError as error:
        print(f"::error::{error}", file=sys.stderr)
        return 1

    # Invalid values force the handler to stop before application insertion,
    # evidence creation, account provisioning, or email enqueue.
    body = json.dumps({
        "email": "not-an-email",
        "username": "x",
        "display_name": "x",
        "adult_confirmed": False,
        "terms_accepted": False,
        "privacy_accepted": False,
    }).encode()
    key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode().rstrip("=")
    request = urllib.request.Request(
        base + "/api/identity/applications",
        data=body,
        headers={
            "Accept": "application/json",
            "Content-Type": "application/json",
            "Idempotency-Key": key,
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=12) as response:
            status, raw = response.status, response.read(65536)
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read(65536)
    except (urllib.error.URLError, TimeoutError) as error:
        print(f"::error::Staging identity endpoint is unreachable ({type(error).__name__}).", file=sys.stderr)
        return 1

    try:
        envelope = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        print(f"::error::Identity endpoint returned non-JSON content (HTTP {status}).", file=sys.stderr)
        return 1

    ready, message = classify_response(status, envelope)
    if not ready:
        error = envelope.get("error") if isinstance(envelope, dict) else None
        code = error.get("code") if isinstance(error, dict) else "UNKNOWN"
        print(f"::error::Identity registration preflight blocked [{code}]: {message}", file=sys.stderr)
        return 1
    print("PASS: WebAuthn/Admin Console readiness, PostgreSQL, private evidence storage and registration policy checks passed.")
    print("PASS: deliberately invalid applicant payload was rejected before any application/account/evidence/email write.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
