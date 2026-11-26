#!/usr/bin/env python3
"""Manual, staging-only creator video lifecycle acceptance test.

Uses the existing staging environment URL and dedicated native-auth smoke
identity. Never prints credentials or signed playback tokens.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def fail(message: str) -> "NoReturn":
    print(f"::error::{message}", file=sys.stderr)
    raise RuntimeError(message)


def http_request(base: str, path: str, *, method: str = "GET",
                 body: bytes | None = None, bearer: str | None = None,
                 headers: dict[str, str] | None = None,
                 follow_redirects: bool = True) -> tuple[int, dict[str, str], bytes]:
    request_headers = {"Accept": "application/json"}
    if headers:
        request_headers.update(headers)
    if bearer:
        request_headers["Authorization"] = f"Bearer {bearer}"
    req = urllib.request.Request(
        urllib.parse.urljoin(base.rstrip("/") + "/", path.lstrip("/")),
        data=body, headers=request_headers, method=method,
    )
    opener = urllib.request.build_opener() if follow_redirects else urllib.request.build_opener(NoRedirect())
    try:
        with opener.open(req, timeout=30) as response:
            return response.status, dict(response.headers.items()), response.read(32 << 20)
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers.items()), error.read(32 << 20)
    except (urllib.error.URLError, TimeoutError) as error:
        fail(f"{method} {path} could not reach staging ({type(error).__name__})")


def json_request(base: str, path: str, *, method: str = "GET",
                 payload: dict | None = None, bearer: str | None = None,
                 expected: tuple[int, ...] = (200,)) -> dict | list:
    body = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"} if payload is not None else {}
    status, _, raw = http_request(base, path, method=method, body=body,
                                  bearer=bearer, headers=headers)
    if status not in expected:
        fail(f"{method} {path}: expected HTTP {expected}, received {status}")
    try:
        return json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        fail(f"{method} {path}: response was not valid JSON")


def login(base: str, email: str, password: str) -> str:
    envelope = json_request(base, "/api/auth/login", method="POST",
                            payload={"email": email, "password": password})
    if not isinstance(envelope, dict) or envelope.get("ok") is not True:
        fail("Dedicated staging identity could not sign in")
    data = envelope.get("data")
    token = data.get("access_token") if isinstance(data, dict) else None
    if not isinstance(token, str) or len(token) < 32:
        fail("Staging login returned no valid native access token")
    return token


def make_fixture(directory: pathlib.Path) -> pathlib.Path:
    output = directory / "testagram-staging-acceptance.mp4"
    command = [
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
        "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25",
        "-t", "45", "-an", "-c:v", "libx264", "-preset", "ultrafast",
        "-pix_fmt", "yuv420p", "-movflags", "+faststart", str(output),
    ]
    subprocess.run(command, check=True, timeout=90)
    if not output.is_file() or output.stat().st_size < 1024:
        fail("FFmpeg did not create a usable MP4 acceptance fixture")
    return output


def upload_video(base: str, token: str, channel_id: str, fixture: pathlib.Path, video_title: str) -> str:
    boundary = "----TestagramAcceptance" + uuid.uuid4().hex
    fields = {
        "channel_id": channel_id,
        "title": video_title,
        "description": "Temporary staging-only playback acceptance fixture",
        "visibility": "private",
    }
    chunks: list[bytes] = []
    for name, value in fields.items():
        chunks.extend([
            f"--{boundary}\r\n".encode(),
            f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode(),
            value.encode(), b"\r\n",
        ])
    chunks.extend([
        f"--{boundary}\r\n".encode(),
        f'Content-Disposition: form-data; name="video"; filename="{fixture.name}"\r\n'.encode(),
        b"Content-Type: video/mp4\r\n\r\n",
        fixture.read_bytes(), b"\r\n",
        f"--{boundary}--\r\n".encode(),
    ])
    body = b"".join(chunks)
    status, _, raw = http_request(
        base, "/api/video/uploads", method="POST", body=body, bearer=token,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    if status != 202:
        fail(f"Video upload did not queue successfully (HTTP {status})")
    try:
        data = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        fail("Video upload returned an invalid response")
    video_id = data.get("id") if isinstance(data, dict) else None
    if not isinstance(video_id, str) or not video_id:
        fail("Video upload response omitted its video ID")
    return video_id


def feed_item(base: str, video_id: str) -> dict | None:
    value = json_request(base, "/api/video/feed")
    if not isinstance(value, list):
        fail("Video feed did not return the expected list")
    return next((item for item in value if isinstance(item, dict) and item.get("id") == video_id), None)


def wait_until_terminal(base: str, token: str, video_id: str, timeout: int = 360) -> str:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = json_request(base, f"/api/video/uploads/{video_id}", bearer=token)
        status = value.get("status") if isinstance(value, dict) else None
        if status in ("ready", "failed", "blocked"):
            return status
        if status not in ("uploading", "queued", "processing"):
            fail(f"Unexpected video processing state: {status!r}")
        time.sleep(5)
    fail("Video processing did not reach a terminal state within six minutes")


def playlist_urls(base: str, location: str) -> tuple[str, str, list[str]]:
    master_url = urllib.parse.urljoin(base.rstrip("/") + "/", location)
    status, _, master = http_request(base, master_url, follow_redirects=True)
    if status != 200:
        fail(f"Master HLS playlist returned HTTP {status}")
    lines = [line.strip() for line in master.decode("utf-8").splitlines() if line.strip()]
    variants = [line for line in lines if not line.startswith("#") and ".m3u8" in line]
    if not variants:
        fail("Master HLS playlist contained no variant playlist")
    variant_url = urllib.parse.urljoin(master_url, variants[0])
    status, _, variant = http_request(base, variant_url)
    if status != 200:
        fail(f"Variant HLS playlist returned HTTP {status}")
    segments = [
        urllib.parse.urljoin(variant_url, line.strip())
        for line in variant.decode("utf-8").splitlines()
        if line.strip() and not line.startswith("#") and ".ts" in line
    ]
    if len(segments) < 2:
        fail("Acceptance fixture did not produce at least two HLS segments")
    return master_url, variant_url, segments


def assert_rejected(base: str, url: str, expected: int, label: str) -> None:
    status, _, _ = http_request(base, url)
    if status != expected:
        fail(f"{label}: expected HTTP {expected}, received {status}")


def write_github_outputs(output_path: str, video_id: str, video_title: str, token: str) -> None:
    values = {
        "video_id": video_id,
        "video_title": video_title,
        "access_token": token,
    }
    for key, value in values.items():
        if not value or "\n" in value or "\r" in value:
            fail(f"Unsafe GitHub step output value: {key}")
    with open(output_path, "a", encoding="utf-8") as output:
        for key, value in values.items():
            output.write(f"{key}={value}\n")


def cleanup_with_token(base: str, token: str, video_id: str) -> None:
    cleanup_status, _, _ = http_request(
        base, f"/api/video/uploads/{video_id}", method="DELETE", bearer=token
    )
    if cleanup_status == 409:
        final_state = wait_until_terminal(base, token, video_id, timeout=360)
        if final_state not in ("ready", "failed", "blocked"):
            fail("Video did not reach a deletable state; staging cleanup is incomplete")
        cleanup_status, _, _ = http_request(
            base, f"/api/video/uploads/{video_id}", method="DELETE", bearer=token
        )
    if cleanup_status != 204:
        fail(f"Staging video cleanup failed (HTTP {cleanup_status}); video may remain")
    gone_status, _, _ = http_request(
        base, f"/api/video/uploads/{video_id}", bearer=token
    )
    if gone_status != 404:
        fail(f"Deleted staging video remained addressable (HTTP {gone_status})")
    if feed_item(base, video_id) is not None:
        fail("Deleted staging video remained visible in the public feed")
    print("PASS: disposable video deleted and removed from feed")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cleanup-video-id", default="")
    args = parser.parse_args()
    raw_base = os.environ.get("TESTAGRAM_STAGING_API_URL", "").strip()
    email = os.environ.get("TESTAGRAM_AUTH_SMOKE_EMAIL", "").strip()
    password = os.environ.get("TESTAGRAM_AUTH_SMOKE_PASSWORD", "")
    if os.environ.get("CONFIRM_STAGING_ONLY") != "true":
        fail("Set confirm_staging_only=true only after confirming this is the isolated staging origin")
    if os.environ.get("CONFIRM_PUBLIC_FEED_TEST") != "true":
        fail("Set confirm_public_feed_test=true to authorize temporary publication in the staging feed")
    if os.environ.get("CONFIRM_VIDEO_CLEANUP") != "true":
        fail("Set confirm_video_cleanup=true to authorize deletion of the acceptance video after the run")
    if not raw_base or not email or not password:
        fail("Configure TESTAGRAM_STAGING_API_URL and the existing dedicated staging auth smoke secrets")

    parsed = urllib.parse.urlsplit(raw_base)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password
            or parsed.query or parsed.fragment or parsed.path not in ("", "/")):
        fail("Staging API URL must be a clean HTTPS origin")
    if parsed.hostname.lower() in {"api.testagram.site", "testagram.site", "www.testagram.site"}:
        fail("Refusing to run video acceptance against a production Testagram hostname")
    base = raw_base.rstrip("/")
    token = login(base, email, password)
    print("PASS: dedicated Testagram-native staging identity authenticated")
    if args.cleanup_video_id:
        cleanup_with_token(base, token, args.cleanup_video_id)
        return

    # Probe the owner-delete route before creating any content. An unknown ID
    # must be accepted idempotently, not rejected as a missing HTTP method.
    probe = "00000000-0000-4000-8000-000000000001"
    status, _, _ = http_request(base, f"/api/video/uploads/{probe}", method="DELETE", bearer=token)
    if status != 204:
        fail(f"Owner-delete cleanup route is not ready on staging (probe HTTP {status})")
    print("PASS: owner-delete cleanup route is present before upload")

    account_tag = hashlib.sha256(email.lower().encode()).hexdigest()[:10]
    handle = "stg-" + account_tag
    channels = json_request(base, "/api/video/channels", bearer=token)
    rows = channels.get("items", []) if isinstance(channels, dict) else []
    channel = next((item for item in rows if item.get("handle") == handle), None)
    if channel is None:
        channel = json_request(base, "/api/video/channels", method="POST",
                               payload={"handle": handle, "name": "Staging Acceptance",
                                        "description": "Private creator channel for automated staging acceptance"},
                               bearer=token, expected=(201,))
        channel_id = channel.get("id") if isinstance(channel, dict) else None
    else:
        channel_id = channel.get("id")
    if not isinstance(channel_id, str) or not channel_id:
        fail("Could not create or resolve the dedicated staging creator channel")
    print("PASS: dedicated creator channel created or reused")

    video_id: str | None = None
    video_title = "Testagram staging acceptance " + os.environ.get("GITHUB_RUN_ID", "manual")
    fixture_dir = pathlib.Path(os.environ.get("RUNNER_TEMP", "/tmp"))
    succeeded = False
    try:
        fixture = make_fixture(fixture_dir)
        video_id = upload_video(base, token, channel_id, fixture, video_title)
        print("PASS: generated MP4 uploaded through the canonical Backend")

        initial_state = json_request(base, f"/api/video/uploads/{video_id}", bearer=token)
        active_state = initial_state.get("status") if isinstance(initial_state, dict) else None
        if active_state not in ("uploading", "queued", "processing"):
            fail(f"Video left the active processing state too quickly to test delete protection: {active_state!r}")
        active_delete_status, _, _ = http_request(
            base, f"/api/video/uploads/{video_id}", method="DELETE", bearer=token
        )
        if active_delete_status != 409:
            fail(f"Active video deletion must be rejected with HTTP 409, received {active_delete_status}")
        print("PASS: owner deletion is refused while upload/transcoding is active")

        state = wait_until_terminal(base, token, video_id)
        if state != "ready":
            fail(f"FFmpeg processing finished in state {state!r}, not ready")
        print("PASS: Data Centre FFmpeg worker produced a ready video")

        private_status, _, _ = http_request(
            base, f"/api/video/uploads/{video_id}/manifest", follow_redirects=False
        )
        if private_status != 404 or feed_item(base, video_id) is not None:
            fail("Private, unpublished video was exposed through the manifest or public feed")
        print("PASS: private/unpublished video is hidden")

        published = json_request(base, f"/api/video/uploads/{video_id}/publish",
                                 method="POST", payload={}, bearer=token)
        if not isinstance(published, dict) or published.get("status") != "public":
            fail("Explicit video publish did not return public status")
        item = feed_item(base, video_id)
        if item is None or item.get("status") != "ready" or item.get("visibility") != "public":
            fail("Published video did not appear in the public Testagram video feed")
        published_feed = json_request(base, "/api/video/feed")
        if not isinstance(published_feed, list) or not published_feed or published_feed[0].get("id") != video_id:
            fail("The disposable video is not the newest feed item; refusing an ambiguous Flutter playback test")
        print("PASS: explicit publish and exact newest-feed-item visibility")

        status, headers, _ = http_request(
            base, f"/api/video/uploads/{video_id}/manifest", follow_redirects=False
        )
        if status != 307 or not headers.get("Location"):
            fail(f"Backend manifest did not return its signed HLS redirect (HTTP {status})")
        location = headers["Location"]
        master_url, variant_url, segments = playlist_urls(base, location)
        print("PASS: master and variant HLS playlists loaded through Backend")

        status, segment_headers, first_segment = http_request(base, segments[0])
        if status != 200 or not first_segment:
            fail(f"First HLS segment failed (HTTP {status})")
        status, _, second_segment = http_request(base, segments[1])
        if status != 200 or not second_segment:
            fail(f"Second HLS segment failed (HTTP {status})")
        print("PASS: multiple HLS segments streamed")

        status, range_headers, ranged = http_request(
            base, segments[0], headers={"Range": "bytes=0-1023"}
        )
        if status != 206 or not range_headers.get("Content-Range") or not ranged:
            fail(f"Byte-range seek did not return HTTP 206 (received {status})")
        print("PASS: HLS byte-range seeking")

        token_value = urllib.parse.parse_qs(urllib.parse.urlsplit(master_url).query).get("token", [""])[0]
        if not token_value or "." not in token_value:
            fail("Signed HLS token was missing from the manifest redirect")
        token_parts = token_value.split(".", 1)
        bad_mac = ("0" if token_parts[0][0] != "0" else "1") + token_parts[0][1:]
        invalid_token = bad_mac + "." + token_parts[1]
        expired_token = token_parts[0] + ".1"
        segment = urllib.parse.urlsplit(segments[0])
        def token_url(value: str) -> str:
            return urllib.parse.urlunsplit((segment.scheme, segment.netloc, segment.path,
                                            urllib.parse.urlencode({"token": value}), ""))
        assert_rejected(base, token_url(invalid_token), 403, "Invalid HLS token")
        assert_rejected(base, token_url(expired_token), 403, "Expired HLS token")
        print("PASS: invalid and expired HLS tokens rejected")

        playback_session = "staging-" + os.environ.get("GITHUB_RUN_ID", uuid.uuid4().hex)
        # The playback session is an explicit header; retry the exact same ID.
        for _ in range(2):
            view_status, _, view_raw = http_request(
                base, f"/api/video/uploads/{video_id}/view", method="POST",
                bearer=token, headers={"X-Playback-Session": playback_session}
            )
            if view_status != 202:
                fail(f"View event returned HTTP {view_status}")
            view_result = json.loads(view_raw)
            if _ == 0 and view_result.get("counted") is not True:
                fail("First playback session was not counted")
            if _ == 1 and view_result.get("counted") is not False:
                fail("Repeated playback session was counted twice")
        print("PASS: repeated playback-session view is deduplicated")

        before = feed_item(base, video_id)
        before_likes = int(before.get("likes", 0))
        for _ in range(2):
            liked = json_request(base, f"/api/video/uploads/{video_id}/like",
                                 method="POST", payload={}, bearer=token)
            if not isinstance(liked, dict) or liked.get("liked") is not True:
                fail("Like request did not return liked=true")
        after = feed_item(base, video_id)
        if after is None or int(after.get("likes", 0)) != before_likes + 1:
            fail("Repeated like requests incremented the public like counter more than once")
        print("PASS: repeated like is idempotent")

        comment_body = "staging-acceptance-" + os.environ.get("GITHUB_RUN_ID", uuid.uuid4().hex)
        created = json_request(base, f"/api/video/uploads/{video_id}/comments",
                               method="POST", payload={"body": comment_body}, bearer=token,
                               expected=(201,))
        comments = json_request(base, f"/api/video/uploads/{video_id}/comments", bearer=token)
        if not isinstance(created, dict) or created.get("created") is not True:
            fail("Video comment was not created")
        if not isinstance(comments, list) or not any(
            isinstance(row, dict) and row.get("body") == comment_body for row in comments
        ):
            fail("Created video comment could not be retrieved")
        print("PASS: comment create and read-back")

        print("PASS: staging API acceptance completed")
        succeeded = True
    finally:
        if video_id:
            if succeeded and os.environ.get("PRESERVE_FOR_FLUTTER") == "true":
                output_path = os.environ.get("GITHUB_OUTPUT")
                if not output_path:
                    cleanup_with_token(base, token, video_id)
                    fail("GITHUB_OUTPUT is unavailable; refusing to leave a staging video behind")
                print("::add-mask::" + token)
                write_github_outputs(output_path, video_id, video_title, token)
                print("PASS: staging video held temporarily for the real Flutter Android feed test")
            else:
                cleanup_with_token(base, token, video_id)


if __name__ == "__main__":
    try:
        main()
    except RuntimeError:
        raise SystemExit(1)
