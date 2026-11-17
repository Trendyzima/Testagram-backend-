#!/usr/bin/env python3
"""External Instabox watchdog for Testagram. Python standard library only."""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any
from urllib.parse import quote, urlencode, urlparse

API_BASE = os.getenv("INSTABOX_API_BASE_URL", "https://api.instabox.sh").rstrip("/")
API_KEY = os.getenv("INSTABOX_WATCHDOG_API_KEY", "")
API_KEY_HEADER = os.getenv("INSTABOX_WATCHDOG_API_KEY_HEADER", "Authorization")
MACHINE_ID = os.getenv("INSTABOX_MACHINE_ID", "")
MACHINE_NAME = os.getenv("INSTABOX_MACHINE_NAME", "testagram")
HEALTH_URL = os.getenv("TESTAGRAM_HEALTH_URL", "")
STATE_FILE = os.getenv("WATCHDOG_STATE_FILE", ".watchdog-state.json")
WEBHOOK_URL = os.getenv("WATCHDOG_ALERT_WEBHOOK", "")
FAILURES_REQUIRED = int(os.getenv("WATCHDOG_FAILURES_REQUIRED", "3"))
PROBE_INTERVAL = int(os.getenv("WATCHDOG_PROBE_INTERVAL_SECONDS", "15"))
PROBE_TIMEOUT = int(os.getenv("WATCHDOG_PROBE_TIMEOUT_SECONDS", "10"))
RESTART_COOLDOWN = int(os.getenv("WATCHDOG_RESTART_COOLDOWN_SECONDS", "1800"))
OPERATION_TIMEOUT = int(os.getenv("WATCHDOG_OPERATION_TIMEOUT_SECONDS", "300"))
RECOVERY_HEALTH_TIMEOUT = int(os.getenv("WATCHDOG_RECOVERY_HEALTH_TIMEOUT_SECONDS", "300"))
DRY_RUN = os.getenv("WATCHDOG_DRY_RUN", "false").lower() == "true"


class WatchdogError(RuntimeError):
    pass


class ProbeConfigurationError(WatchdogError):
    """A 4xx health response indicates a bad probe contract, not an outage."""



@dataclass
class API:
    base: str = API_BASE
    key: str = API_KEY

    def request(self, method: str, path: str, body: dict[str, Any] | None = None,
                idempotency_key: str | None = None) -> dict[str, Any]:
        if not self.key:
            raise WatchdogError("INSTABOX_WATCHDOG_API_KEY is not configured")
        headers = {"Accept": "application/json"}
        if API_KEY_HEADER == "Authorization":
            headers["Authorization"] = f"Bearer {self.key}"
        elif API_KEY_HEADER == "X-API-Key":
            if method != "GET":
                raise WatchdogError("read-only InstaBox credentials cannot issue lifecycle actions")
            headers["X-API-Key"] = self.key
        else:
            raise WatchdogError("unsupported InstaBox API authentication header")
        payload = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            payload = json.dumps(body).encode()
        if idempotency_key:
            headers["Idempotency-Key"] = idempotency_key
        req = urllib.request.Request(self.base + path, data=payload, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=20) as response:
                raw = response.read()
        except urllib.error.HTTPError as exc:
            detail = exc.read(4096).decode("utf-8", "replace")
            if self.key:
                detail = detail.replace(self.key, "[REDACTED]")
            raise WatchdogError(f"Instabox API {method} {path} returned HTTP {exc.code}: {detail}") from exc
        except (urllib.error.URLError, TimeoutError) as exc:
            raise WatchdogError(f"Instabox API unavailable for {method} {path}: {exc}") from exc
        if not raw:
            return {}
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise WatchdogError(f"Instabox API returned non-JSON for {method} {path}") from exc
        if not isinstance(data, dict):
            raise WatchdogError(f"Instabox API returned an unexpected response for {method} {path}")
        return data


def unwrap(data: dict[str, Any], key: str | None = None) -> dict[str, Any]:
    if key and isinstance(data.get(key), dict):
        return data[key]
    if isinstance(data.get("data"), dict):
        inner = data["data"]
        if key and isinstance(inner.get(key), dict):
            return inner[key]
        return inner
    return data


def load_state() -> dict[str, Any]:
    try:
        with open(STATE_FILE, encoding="utf-8") as handle:
            value = json.load(handle)
        return value if isinstance(value, dict) else {}
    except FileNotFoundError:
        return {}
    except (OSError, json.JSONDecodeError):
        return {}


def save_state(state: dict[str, Any]) -> None:
    tmp = STATE_FILE + ".tmp"
    with open(tmp, "w", encoding="utf-8") as handle:
        json.dump(state, handle)
    os.replace(tmp, STATE_FILE)


def emit(message: str) -> None:
    print(f"instabox-watchdog: {message}", flush=True)


def notify(message: str, severity: str = "warning") -> None:
    emit(message)
    if not WEBHOOK_URL:
        return
    if severity == "critical":
        state = load_state()
        now = int(time.time())
        if now - int(state.get("last_critical_alert_at", 0)) < 1800:
            emit("critical webhook alert suppressed by 30-minute notification cooldown")
            return
        state["last_critical_alert_at"] = now
        save_state(state)
    payload = json.dumps({"text": f"[Testagram VPS {severity}] {message}"}).encode()
    req = urllib.request.Request(WEBHOOK_URL, data=payload,
                                 headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=10):
            pass
    except Exception as exc:  # notifications must not hide the recovery result
        emit(f"notification delivery failed: {type(exc).__name__}")


def probe(url: str) -> tuple[bool, str]:
    if not url:
        return False, "URL not configured"
    req = urllib.request.Request(url, headers={"User-Agent": "Testagram-Instabox-Watchdog/1.0"})
    try:
        with urllib.request.urlopen(req, timeout=PROBE_TIMEOUT) as response:
            if 200 <= response.status < 300:
                return True, f"HTTP {response.status}"
            return False, f"HTTP {response.status}"
    except urllib.error.HTTPError as exc:
        # A 4xx response is an endpoint, routing, or authorization problem.
        # Never interpret it as machine failure: an enabled watchdog must not
        # stop/start a healthy VPS because its probe URL is wrong or blocked.
        if 400 <= exc.code < 500:
            raise ProbeConfigurationError(
                f"Health probe {urlparse(url).path or '/'} returned HTTP {exc.code}; "
                "refusing automatic lifecycle action until the probe contract is corrected"
            ) from exc
        return False, f"HTTP {exc.code}"
    except (urllib.error.URLError, TimeoutError) as exc:
        return False, type(exc).__name__


def resolve_machine_id(api: API) -> str:
    if MACHINE_ID:
        return MACHINE_ID
    if not MACHINE_NAME:
        raise WatchdogError("Configure INSTABOX_MACHINE_NAME or INSTABOX_MACHINE_ID")
    cursor = None
    matches: list[dict[str, Any]] = []
    # Resolve by exact name across every page; never guess among duplicates.
    for _ in range(10):
        query = {"limit": "100"}
        if cursor:
            query["cursor"] = cursor
        result = api.request("GET", "/v1/machines?" + urlencode(query))
        page = unwrap(result)
        machines = page.get("machines")
        if not isinstance(machines, list):
            raise WatchdogError("Instabox machine-list response is missing its machines array")
        matches.extend(item for item in machines
                       if isinstance(item, dict) and item.get("name") == MACHINE_NAME)
        cursor = page.get("nextCursor")
        if not cursor:
            break
    else:
        raise WatchdogError("Instabox machine list exceeded ten pages; refusing ambiguous resolution")
    if len(matches) != 1 or not matches[0].get("id"):
        raise WatchdogError(
            f"Expected exactly one Instabox machine named {MACHINE_NAME!r}; found {len(matches)}"
        )
    return str(matches[0]["id"])


def wait_for_service() -> None:
    # Only public liveness is used to decide whether a machine lifecycle
    # action succeeded. Dependency readiness is intentionally private and is
    # enforced by Docker healthchecks inspected by the read-only health audit.
    deadline = time.monotonic() + RECOVERY_HEALTH_TIMEOUT
    while True:
        health_ok, health_detail = probe(HEALTH_URL)
        if health_ok:
            emit(f"recovery verified: public liveness={health_detail}")
            return
        if time.monotonic() >= deadline:
            raise WatchdogError(
                f"Machine lifecycle completed but Testagram liveness did not recover "
                f"within {RECOVERY_HEALTH_TIMEOUT}s (health={health_detail})"
            )
        time.sleep(PROBE_INTERVAL)


def machine_snapshot(api: API) -> dict[str, Any]:
    result = api.request("GET", f"/v1/machines/{quote(MACHINE_ID, safe='')}")
    machine = unwrap(result, "machine")
    required = ("id", "state", "desiredState")
    missing = [field for field in required if field not in machine]
    if missing:
        raise WatchdogError("Machine response missing required fields: " + ", ".join(missing))
    if str(machine["id"]) != MACHINE_ID:
        raise WatchdogError("Instabox returned a different machine ID; refusing lifecycle action")
    if MACHINE_NAME and machine.get("name") != MACHINE_NAME:
        raise WatchdogError("Instabox machine name changed; refusing lifecycle action")
    return machine


def operation_from(result: dict[str, Any]) -> dict[str, Any]:
    return unwrap(result, "operation")


def wait_operation(api: API, result: dict[str, Any], expected: str) -> None:
    op = operation_from(result)
    op_id = op.get("id")
    if not op_id:
        # Some synchronous responses return the machine directly.
        machine = unwrap(result, "machine")
        if machine.get("state") == expected:
            return
        raise WatchdogError("Lifecycle response contained no operation ID")
    deadline = time.monotonic() + OPERATION_TIMEOUT
    while time.monotonic() < deadline:
        current = unwrap(api.request("GET", f"/v1/operations/{quote(str(op_id), safe='')}"), "operation")
        state = current.get("state")
        if state == "succeeded":
            final_machine = machine_snapshot(api)
            if final_machine["state"] != expected:
                raise WatchdogError(
                    f"Instabox operation {op_id} succeeded but machine state is "
                    f"{final_machine['state']!r}, expected {expected!r}"
                )
            return
        if state == "failed":
            error = current.get("error") or {}
            raise WatchdogError(f"Instabox operation {op_id} failed: {json.dumps(error)[:1000]}")
        time.sleep(5)
    raise WatchdogError(f"Instabox operation {op_id} did not finish within {OPERATION_TIMEOUT}s")


def lifecycle(api: API, action: str) -> None:
    # Each logical operation gets a unique idempotency key. It is never reused
    # for a different action or a later incident.
    key = f"testagram-watchdog-{MACHINE_ID}-{action}-{int(time.time())}"
    result = api.request("POST", f"/v1/machines/{quote(MACHINE_ID, safe='')}/{action}", {},
                         idempotency_key=key)
    expected = "running" if action in ("start", "resume") else "stopped"
    wait_operation(api, result, expected)


def recover_stopped(api: API, state: dict[str, Any]) -> str:
    now = int(time.time())
    if DRY_RUN:
        return "DRY RUN: would start stopped machine"
    state.update(last_action_at=now, last_action="start")
    save_state(state)
    lifecycle(api, "start")
    wait_for_service()
    return "started stopped machine; public API liveness verified"


def main() -> int:
    global MACHINE_ID
    if not HEALTH_URL:
        raise WatchdogError("Configure TESTAGRAM_HEALTH_URL")
    if urlparse(HEALTH_URL).scheme != "https":
        raise WatchdogError("Health URL must use HTTPS")
    if WEBHOOK_URL and urlparse(WEBHOOK_URL).scheme != "https":
        raise WatchdogError("Alert webhook URL must use HTTPS")
    if (FAILURES_REQUIRED < 2 or PROBE_INTERVAL < 1 or PROBE_TIMEOUT < 1
            or OPERATION_TIMEOUT < 1 or RECOVERY_HEALTH_TIMEOUT < 1):
        raise WatchdogError("Unsafe watchdog thresholds: require >=2 failures, positive timeouts/intervals and >=300s restart cooldown")
    if not API_KEY:
        raise WatchdogError(
            "An InstaBox API key is required for provider-state monitoring. "
            "Configure INSTABOX_READONLY_API_KEY for read-only mode or "
            "INSTABOX_WATCHDOG_API_KEY for explicitly enabled recovery."
        )
    if not MACHINE_ID and not MACHINE_NAME:
        raise WatchdogError("Configure INSTABOX_MACHINE_NAME or INSTABOX_MACHINE_ID")
    if urlparse(API_BASE).scheme != "https":
        raise WatchdogError("Instabox API URL must use HTTPS")
    api = API()
    MACHINE_ID = resolve_machine_id(api)
    state = load_state()
    machine = machine_snapshot(api)
    machine_state = machine["state"]
    desired = machine["desiredState"]
    emit(f"machine={MACHINE_ID} state={machine_state} desiredState={desired}")

    cooldown_until = machine.get("cooldownUntil")
    if cooldown_until:
        from datetime import datetime, timezone
        try:
            deadline = datetime.fromisoformat(cooldown_until.replace("Z", "+00:00"))
        except (AttributeError, ValueError):
            raise WatchdogError("Instabox returned an invalid cooldownUntil timestamp")
        if deadline > datetime.now(timezone.utc):
            emit(f"provider cooldown is active until {cooldown_until}; not attempting recovery")
            return 0

    if machine.get("operationId"):
        emit(f"machine has active operation {machine['operationId']}; leaving it undisturbed")
        return 0

    # Respect explicit provider/operator intent. Recovery only applies when
    # the desired state remains running; never undo an intentional stop/pause.
    if desired != "running":
        emit(f"desiredState={desired}; respecting provider/operator intent")
        return 0

    if machine_state == "stopped":
        result = recover_stopped(api, state)
        if result.startswith("started"):
            notify(result, "recovery")
        else:
            emit(result)
        return 0
    if machine_state == "suspended":
        # InstaBox suspends idle machines intentionally. Do not issue a
        # scheduled resume or synthetic HTTP/SSH ping: real application
        # traffic or an operator's Paradev SSH connection wakes the box.
        emit("machine is idle-suspended; leaving provider auto-wake enabled for real requests/SSH")
        return 0
    if machine_state in ("creating", "starting", "stopping", "suspending", "resuming", "deleting"):
        emit(f"machine lifecycle is already {machine_state}; waiting for next scheduled check")
        return 0
    if machine_state == "deleted":
        raise WatchdogError("Machine is deleted; watchdog will not recreate or replace it")
    if machine_state == "error":
        raise WatchdogError("Machine is in provider error state; refusing automatic stop/start")
    if machine_state != "running":
        raise WatchdogError(f"Unrecognized machine state {machine_state!r}; refusing action")
    # A scheduled HTTP probe would wake an idle-suspended Free-plan box and
    # can defeat InstaBox's idle policy. Keep routine monitoring provider-side:
    # GET machine state is non-waking. App liveness is checked only after an
    # explicit recovery start, never as a periodic keepalive.
    state["last_provider_running_at"] = int(time.time())
    save_state(state)
    emit("provider reports machine running; no synthetic liveness request or lifecycle action issued")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except WatchdogError as exc:
        notify(str(exc), "critical")
        sys.exit(1)
