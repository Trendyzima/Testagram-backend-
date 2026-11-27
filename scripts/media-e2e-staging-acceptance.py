#!/usr/bin/env python3
"""Authenticated staging acceptance for Backend -> Data Centre media -> CDN."""
from __future__ import annotations

import hashlib
import json
import os
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zlib


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


OPENER = urllib.request.build_opener(NoRedirect)


PRODUCTION_HOSTS = {
    "api.testagram.site",
    "testagram.site",
    "www.testagram.site",
    "media.testagram.site",
    "cdn.testagram.site",
}


def fail(message: str) -> "NoReturn":
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def png_chunk(kind: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)


def fixture_png() -> bytes:
    # Deterministic 1x1 RGB PNG generated with the Python standard library.
    header = struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0)
    pixels = zlib.compress(b"\x00\x23\x7a\xc1")
    return b"\x89PNG\r\n\x1a\n" + png_chunk(b"IHDR", header) + png_chunk(b"IDAT", pixels) + png_chunk(b"IEND", b"")


def json_request(base: str, path: str, *, method: str = "GET", body: dict | None = None,
                 bearer: str | None = None, timeout: int = 30) -> tuple[int, dict]:
    headers = {"Accept": "application/json"}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    if bearer:
        headers["Authorization"] = f"Bearer {bearer}"
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=timeout) as response:
            raw, status = response.read(1 << 20), response.status
    except urllib.error.HTTPError as error:
        raw, status = error.read(1 << 20), error.code
    except (urllib.error.URLError, TimeoutError) as error:
        fail(f"Staging media endpoint unavailable: {type(error).__name__}")
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        fail(f"{method} {path} returned non-JSON HTTP {status}")
    if not isinstance(value, dict):
        fail(f"{method} {path} returned an invalid JSON envelope")
    return status, value


def raw_request(url: str, *, method: str = "GET", headers: dict | None = None,
                body: bytes | None = None, timeout: int = 45) -> tuple[int, dict, bytes]:
    req = urllib.request.Request(url, data=body, headers=headers or {}, method=method)
    try:
        with OPENER.open(req, timeout=timeout) as response:
            return response.status, dict(response.headers.items()), response.read(32 << 20)
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers.items()), error.read(32 << 20)
    except (urllib.error.URLError, TimeoutError) as error:
        fail(f"Staging media transfer failed: {type(error).__name__}")


def envelope_data(status: int, envelope: dict, expected: set[int], step: str) -> dict:
    if status not in expected or envelope.get("ok") is not True or not isinstance(envelope.get("data"), dict):
        error = envelope.get("error")
        code = error.get("code", "UNKNOWN") if isinstance(error, dict) else "UNKNOWN"
        fail(f"{step} failed (HTTP {status}, {code})")
    return envelope["data"]


def clean_https_origin(raw: str, label: str) -> tuple[str, str]:
    parsed = urllib.parse.urlsplit(raw)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
        fail(f"{label} must be a clean HTTPS origin")
    if parsed.hostname.lower() in PRODUCTION_HOSTS:
        fail(f"Refusing to run against canonical production host for {label}")
    return raw.rstrip("/"), parsed.hostname.lower().rstrip(".")


def main() -> None:
    if os.environ.get("TESTAGRAM_STAGING_FRESH_MEDIA_ENABLED", "").lower() != "true":
        fail("The staging Backend must explicitly enable FRESH_MEDIA_ENABLED for this Data Centre media acceptance")
    raw_api = os.environ.get("TESTAGRAM_STAGING_API_URL", "").strip()
    email = os.environ.get("TESTAGRAM_AUTH_SMOKE_EMAIL", "").strip()
    password = os.environ.get("TESTAGRAM_AUTH_SMOKE_PASSWORD", "")
    raw_cdn = os.environ.get("TESTAGRAM_STAGING_CDN_BASE_URL", "").strip()
    b2_host = os.environ.get("TESTAGRAM_STAGING_B2_HOST", "").strip().lower().rstrip(".")
    r2_host = os.environ.get("TESTAGRAM_STAGING_R2_HOST", "").strip().lower().rstrip(".")
    if not raw_api or not email or not password or not raw_cdn or not b2_host or not r2_host:
        fail("Configure staging API, CDN origin, B2/R2 provider hosts and dedicated verified smoke identity")

    api, api_host = clean_https_origin(raw_api, "TESTAGRAM_STAGING_API_URL")
    cdn, cdn_host = clean_https_origin(raw_cdn, "TESTAGRAM_STAGING_CDN_BASE_URL")
    if cdn_host in PRODUCTION_HOSTS or b2_host in PRODUCTION_HOSTS or r2_host in PRODUCTION_HOSTS:
        fail("Production provider/CDN hosts are not permitted in staging acceptance")
    allowed_provider_hosts = {b2_host, r2_host}
    if len({api_host, cdn_host, b2_host, r2_host}) != 4:
        fail("Staging API, CDN, B2 and R2 hosts must be four distinct origins")
    if not all(host and not any(char.isspace() for char in host) and "/" not in host and ":" not in host and "@" not in host for host in allowed_provider_hosts):
        fail("Expected B2/R2 provider settings must be hostnames only")

    status, envelope = json_request(api, "/api/auth/login", method="POST",
                                    body={"email": email, "password": password})
    tokens = envelope.get("data") if status == 200 else None
    access = tokens.get("access_token") if isinstance(tokens, dict) else None
    if envelope.get("ok") is not True or not isinstance(access, str) or len(access) < 32:
        error = envelope.get("error")
        code = error.get("code", "UNKNOWN") if isinstance(error, dict) else "UNKNOWN"
        fail(f"Verified staging smoke identity could not sign in (HTTP {status}, {code})")

    payload = fixture_png()
    digest = hashlib.sha256(payload).hexdigest()

    denied_status, _ = json_request(
        api, "/api/media/upload-intent", method="POST",
        body={"name": "unauthenticated-staging-probe.png", "content_type": "image/png",
              "visibility": "private", "size_bytes": len(payload)},
    )
    if denied_status != 401:
        fail(f"Unauthenticated private upload-intent request was not rejected (HTTP {denied_status})")
    print("PASS: unauthenticated private upload intent rejected")

    key = ""
    asset_ready = False
    deleted = False
    cdn_url = ""
    cdn_authorization = ""
    try:
        status, envelope = json_request(
            api, "/api/media/upload-intent", method="POST", bearer=access,
            body={"name": "testagram-staging-private.png", "content_type": "image/png",
                  "visibility": "private", "size_bytes": len(payload)},
        )
        intent = envelope_data(status, envelope, {201}, "private Data Centre media upload intent")
        key = intent.get("key", "")
        upload_path = intent.get("url", "")
        if not isinstance(key, str) or not key.startswith("users/") or not isinstance(upload_path, str):
            fail("Backend returned an invalid media upload intent")
        upload_url = urllib.parse.urljoin(api + "/", upload_path)
        parsed_upload = urllib.parse.urlsplit(upload_url)
        if parsed_upload.scheme != "https" or (parsed_upload.hostname or "").lower().rstrip(".") != api_host or parsed_upload.username or parsed_upload.password or parsed_upload.fragment:
            fail("Upload gateway URL escaped the authenticated staging Backend origin")
        put_status, _, _ = raw_request(upload_url, method="PUT",
                                       headers={"Authorization": f"Bearer {access}", "Content-Type": "image/png"},
                                       body=payload)
        if put_status != 204:
            fail(f"Bounded authenticated upload gateway returned HTTP {put_status}, expected 204")
        print("PASS: authenticated upload intent and bounded private upload gateway")

        completed = False
        for attempt in range(8):
            status, envelope = json_request(
                api, "/api/media/complete", method="POST", bearer=access,
                body={"key": key, "visibility": "private", "content_type": "image/png",
                      "checksum_sha256": digest},
                timeout=35,
            )
            if status == 201:
                asset = envelope_data(status, envelope, {201}, "media completion")
                if asset.get("key") != key or asset.get("status") != "ready" or asset.get("size_bytes") != len(payload):
                    fail("Data Centre media did not reach ready state with the exact uploaded metadata")
                completed = True
                asset_ready = True
                break
            pending = envelope.get("data")
            if status != 202 or not isinstance(pending, dict) or pending.get("status") != "replication_pending":
                error = envelope.get("error")
                code = error.get("code", "UNKNOWN") if isinstance(error, dict) else "UNKNOWN"
                fail(f"Data Centre verification/replication failed (HTTP {status}, {code})")
            print(f"WAIT: Data Centre media replication is pending (attempt {attempt + 1}/8)")
            time.sleep(2)
        if not completed:
            fail("Data Centre media did not reach protected/ready state within the staging acceptance window")
        print("PASS: Data Centre B2/R2 verification, replica protection and Backend metadata commit")

        unauth_status, _ = json_request(
            api, "/api/media/private-download-url", method="POST", body={"key": key}
        )
        if unauth_status != 401:
            fail(f"Unauthenticated private download grant was not rejected (HTTP {unauth_status})")

        status, envelope = json_request(api, "/api/media/private-download-url", method="POST",
                                        bearer=access, body={"key": key})
        direct = envelope_data(status, envelope, {200}, "private Data Centre download grant")
        direct_url = direct.get("url", "")
        parsed_direct = urllib.parse.urlsplit(direct_url)
        if parsed_direct.scheme != "https" or (parsed_direct.hostname or "").lower().rstrip(".") not in allowed_provider_hosts or parsed_direct.username or parsed_direct.password or parsed_direct.fragment:
            fail("Private download grant did not resolve to the configured staging B2/R2 origins")
        direct_status, direct_headers, direct_body = raw_request(direct_url)
        if direct_status != 200 or direct_body != payload or direct_headers.get("Content-Type", "").split(";")[0].strip() != "image/png":
            fail("Direct private Data Centre download did not reproduce the exact uploaded PNG bytes")
        print("PASS: owner-authorized direct download from verified staging B2/R2 origin")

        status, envelope = json_request(api, "/api/media/cdn-download-url", method="POST",
                                        bearer=access, body={"key": key})
        cdn_grant = envelope_data(status, envelope, {200}, "private CDN authorization grant")
        cdn_url = cdn_grant.get("url", "")
        cdn_authorization = cdn_grant.get("authorization", "")
        parsed_cdn = urllib.parse.urlsplit(cdn_url)
        if parsed_cdn.scheme != "https" or (parsed_cdn.hostname or "").lower().rstrip(".") != cdn_host or parsed_cdn.username or parsed_cdn.password or parsed_cdn.fragment or not parsed_cdn.path.startswith("/v1/") or not cdn_authorization.startswith("Bearer "):
            fail("Private CDN grant did not target the configured staging CDN origin")

        anon_status, _, _ = raw_request(cdn_url)
        if anon_status not in (401, 403, 404):
            fail(f"Anonymous request to private CDN object was not denied (HTTP {anon_status})")

        viewer_headers = {"Authorization": cdn_authorization}
        cdn_status, cdn_headers, cdn_body = raw_request(cdn_url, headers=viewer_headers)
        if cdn_status != 200 or cdn_body != payload or not all(marker in cdn_headers.get("Cache-Control", "").lower() for marker in ("private", "no-store")):
            fail("Authorized private CDN playback did not return exact bytes with private/no-store caching")
        range_status, range_headers, range_body = raw_request(
            cdn_url, headers={**viewer_headers, "Range": "bytes=0-3"}
        )
        if range_status != 206 or range_body != payload[:4] or not range_headers.get("Content-Range", "").startswith(f"bytes 0-3/{len(payload)}"):
            fail("Private CDN byte-range playback failed")
        print("PASS: private CDN anonymous denial, authorized playback, byte ranges and cache isolation")

        unauth_delete_status, _ = json_request(
            api, "/api/media/delete", method="POST", body={"key": key}
        )
        if unauth_delete_status != 401:
            fail(f"Unauthenticated private media deletion was not rejected (HTTP {unauth_delete_status})")

        for attempt in range(30):
            status, envelope = json_request(api, "/api/media/delete", method="POST", bearer=access,
                                            body={"key": key}, timeout=15)
            data = envelope.get("data") if isinstance(envelope, dict) else None
            if status == 200 and isinstance(data, dict) and data.get("status") == "deleted":
                deleted = True
                break
            if status != 202 or not isinstance(data, dict) or data.get("status") != "deletion_pending":
                error = envelope.get("error")
                code = error.get("code", "UNKNOWN") if isinstance(error, dict) else "UNKNOWN"
                fail(f"Durable Data Centre media deletion failed (HTTP {status}, {code})")
            time.sleep(1)
        if not deleted:
            fail("Data Centre B2/R2 and metadata sidecar deletion did not finish within 30 seconds")

        status, envelope = json_request(api, "/api/media/private-download-url", method="POST",
                                        bearer=access, body={"key": key})
        if status != 404:
            fail(f"Deleted media remained available through Backend private download (HTTP {status})")
        after_status, _, _ = raw_request(cdn_url, headers=viewer_headers)
        if after_status not in (404, 410):
            fail(f"CDN still served a deleted private object after purge (HTTP {after_status})")
        print("PASS: durable deletion, private-origin denial and CDN purge verification")
        print("PASS: authenticated Backend -> Data Centre B2/R2 -> CDN media lifecycle completed")
    finally:
        if key and asset_ready and not deleted:
            try:
                for _ in range(10):
                    cleanup_status, cleanup_envelope = json_request(
                        api, "/api/media/delete", method="POST", bearer=access,
                        body={"key": key}, timeout=15,
                    )
                    cleanup_data = cleanup_envelope.get("data")
                    if cleanup_status == 200 and isinstance(cleanup_data, dict) and cleanup_data.get("status") == "deleted":
                        deleted = True
                        break
                    if cleanup_status != 202 or not isinstance(cleanup_data, dict) or cleanup_data.get("status") != "deletion_pending":
                        break
                    time.sleep(1)
                if not deleted:
                    print("::warning::Automatic cleanup remains pending; inspect the unique staging object before rerunning.")
            except SystemExit:
                print("::warning::Automatic cleanup attempt could not be confirmed; inspect the unique staging object before rerunning.")
        elif key and not asset_ready:
            print("::warning::The unique upload intent did not reach ready state; confirm the Backend janitor removes its expired upload and object.");



if __name__ == "__main__":
    main()
