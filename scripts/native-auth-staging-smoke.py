#!/usr/bin/env python3
"""Staging-only smoke test for Testagram native authentication and revocation."""
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


OPENER = urllib.request.build_opener(NoRedirect)


def fail(message: str) -> "NoReturn":
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def request(base: str, path: str, *, method: str = "GET", body: dict | None = None,
            bearer: str | None = None) -> tuple[int, dict]:
    headers = {"Accept": "application/json"}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    if bearer:
        headers["Authorization"] = f"Bearer {bearer}"
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=20) as response:
            raw = response.read(1 << 20)
            status = response.status
    except urllib.error.HTTPError as error:
        raw = error.read(1 << 20)
        status = error.code
    except (urllib.error.URLError, TimeoutError) as error:
        fail(f"Staging authentication endpoint could not be reached: {type(error).__name__}")
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        fail(f"{path} returned a non-JSON response (HTTP {status})")
    if not isinstance(value, dict):
        fail(f"{path} returned an invalid JSON envelope (HTTP {status})")
    return status, value


def success(data: dict, step: str) -> dict:
    if data.get("ok") is not True or not isinstance(data.get("data"), dict):
        fail(f"{step} did not return the documented successful response")
    return data["data"]


def login(base: str, email: str, password: str) -> dict:
    status, envelope = request(base, "/api/auth/login", method="POST",
                               body={"email": email, "password": password})
    if status != 200:
        error = envelope.get("error")
        code = error.get("code", "UNKNOWN") if isinstance(error, dict) else "UNKNOWN"
        fail(f"Staging test identity cannot sign in (HTTP {status}, {code}); confirm it is email-verified and active")
    tokens = success(envelope, "login")
    access, refresh = tokens.get("access_token"), tokens.get("refresh_token")
    if not isinstance(access, str) or len(access) < 32 or not isinstance(refresh, str) or len(refresh) < 40:
        fail("Login returned incomplete native session credentials")
    if tokens.get("token_type") != "Bearer" or tokens.get("expires_in") != 900:
        fail("Login returned an unexpected native access-token contract")
    return tokens


def require_status(base: str, path: str, expected: int, *, method: str = "GET",
                   body: dict | None = None, bearer: str | None = None) -> dict:
    status, envelope = request(base, path, method=method, body=body, bearer=bearer)
    if status != expected:
        fail(f"{method} {path}: expected HTTP {expected}, received {status}")
    return envelope


def require_error_code(base: str, path: str, expected_status: int, expected_code: str, *,
                       method: str = "POST", body: dict | None = None) -> None:
    envelope = require_status(base, path, expected_status, method=method, body=body)
    error = envelope.get("error")
    code = error.get("code") if isinstance(error, dict) else None
    if code != expected_code:
        fail(f"{method} {path}: expected error code {expected_code}, received {code or 'UNKNOWN'}")


def main() -> None:
    raw_base = os.environ.get("TESTAGRAM_STAGING_API_URL", "").strip()
    email = os.environ.get("TESTAGRAM_AUTH_SMOKE_EMAIL", "").strip()
    password = os.environ.get("TESTAGRAM_AUTH_SMOKE_PASSWORD", "")
    if not raw_base or not email or not password:
        fail("Configure TESTAGRAM_STAGING_API_URL and the dedicated staging smoke email/password secrets")
    parsed = urllib.parse.urlsplit(raw_base)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        fail("TESTAGRAM_STAGING_API_URL must be a clean HTTPS origin")
    production_hosts = {"api.testagram.site", "testagram.site", "www.testagram.site", "media.testagram.site", "cdn.testagram.site"}
    if parsed.path not in ("", "/") or parsed.hostname.lower().rstrip(".") in production_hosts:
        fail("Refusing to run against a non-staging path or a canonical production Testagram host")
    base = raw_base.rstrip("/")

    require_status(base, "/api/me/profile", 401)
    require_error_code(
        base, "/api/auth/signup", 400, "INVALID_PASSWORD",
        body={"email": "signup-readiness@example.invalid", "username": "signup_readiness", "password": "short"},
    )
    print("PASS: anonymous profile access is denied and public signup reaches ordinary password validation independently of identity review")

    first = login(base, email, password)
    require_status(base, "/api/me/profile", 200, bearer=first["access_token"])
    print("PASS: native login and authenticated profile access")

    status, rotated_envelope = request(base, "/api/auth/refresh", method="POST",
                                       body={"refresh_token": first["refresh_token"]})
    if status != 200:
        fail(f"Refresh rotation failed (HTTP {status})")
    rotated = success(rotated_envelope, "refresh")
    if rotated.get("refresh_token") == first["refresh_token"] or rotated.get("access_token") == first["access_token"]:
        fail("Refresh did not rotate both native credentials")
    if not isinstance(rotated.get("refresh_token"), str) or len(rotated["refresh_token"]) < 40:
        fail("Rotated refresh credential is invalid")
    require_status(base, "/api/me/profile", 200, bearer=rotated["access_token"])
    print("PASS: refresh rotation and continued authenticated access")

    require_status(base, "/api/auth/refresh", 401, method="POST",
                   body={"refresh_token": first["refresh_token"]})
    require_status(base, "/api/me/profile", 401, bearer=rotated["access_token"])
    print("PASS: refresh-token replay revokes the entire session family")

    second = login(base, email, password)
    require_status(base, "/api/auth/logout", 200, method="POST", body={}, bearer=second["access_token"])
    require_status(base, "/api/me/profile", 401, bearer=second["access_token"])
    require_status(base, "/api/auth/refresh", 401, method="POST",
                   body={"refresh_token": second["refresh_token"]})
    print("PASS: logout revokes both access and refresh credentials")

    third = login(base, email, password)
    require_status(base, "/api/auth/logout-all", 200, method="POST", body={}, bearer=third["access_token"])
    require_status(base, "/api/me/profile", 401, bearer=third["access_token"])
    require_status(base, "/api/auth/refresh", 401, method="POST",
                   body={"refresh_token": third["refresh_token"]})
    print("PASS: logout-all revokes access and refresh credentials across the account")
    print("PASS: Testagram native authentication staging smoke completed")


if __name__ == "__main__":
    main()
