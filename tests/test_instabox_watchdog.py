import io
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts import instabox_watchdog as watchdog

watchdog.MACHINE_ID = "machine-123"
watchdog.MACHINE_NAME = "testagramdb"


class FakeAPI:
    def __init__(self, machine=None):
        self.machine = machine or {
            "id": "machine-123",
            "name": "testagramdb",
            "state": "running",
            "desiredState": "running",
            "operationId": None,
        }
        self.calls = []

    def request(self, method, path, body=None, idempotency_key=None):
        self.calls.append((method, path, body, idempotency_key))
        if method == "GET" and path == "/v1/machines/machine-123":
            return self.machine
        if method == "POST" and path.endswith(("/stop", "/start", "/resume")):
            action = path.rsplit("/", 1)[-1]
            self.machine["state"] = "stopped" if action == "stop" else "running"
            self.machine["operationId"] = None
            return {"id": "op-1", "machineId": "machine-123", "state": "queued"}
        if method == "GET" and path == "/v1/operations/op-1":
            return {"id": "op-1", "machineId": "machine-123", "state": "succeeded"}
        raise AssertionError(f"Unexpected API request: {method} {path}")


class WatchdogTests(unittest.TestCase):
    def test_dry_run_without_provider_key_runs_public_liveness_only(self):
        health_url = "https://api.example.test/healthz"
        probe_calls = []

        def successful_probe(url):
            probe_calls.append(url)
            return True, "HTTP 200"

        with (
            patch.object(watchdog, "DRY_RUN", True),
            patch.object(watchdog, "API_KEY", ""),
            patch.object(watchdog, "MACHINE_ID", ""),
            patch.object(watchdog, "HEALTH_URL", health_url),
            patch.object(watchdog, "probe", side_effect=successful_probe),
            patch.object(watchdog, "API") as api_factory,
        ):
            self.assertEqual(watchdog.main(), 0)

        self.assertEqual(probe_calls, [health_url])
        api_factory.assert_not_called()

    def test_dry_run_without_provider_key_reports_liveness_failure_without_api_access(self):
        with (
            patch.object(watchdog, "DRY_RUN", True),
            patch.object(watchdog, "API_KEY", ""),
            patch.object(watchdog, "MACHINE_ID", ""),
            patch.object(watchdog, "HEALTH_URL", "https://api.example.test/healthz"),
            patch.object(watchdog, "FAILURES_REQUIRED", 2),
            patch.object(watchdog, "PROBE_INTERVAL", 1),
            patch.object(watchdog, "probe", return_value=(False, "HTTP 500")),
            patch.object(watchdog.time, "sleep"),
            patch.object(watchdog, "API") as api_factory,
        ):
            with self.assertRaisesRegex(
                watchdog.WatchdogError,
                "read-only public liveness probes failed",
            ):
                watchdog.main()

        api_factory.assert_not_called()

    def test_read_only_liveness_404_fails_closed_without_provider_access(self):
        with (
            patch.object(watchdog, "DRY_RUN", True),
            patch.object(watchdog, "API_KEY", ""),
            patch.object(watchdog, "MACHINE_ID", ""),
            patch.object(watchdog, "HEALTH_URL", "https://api.example.test/healthz"),
            patch.object(
                watchdog,
                "probe",
                side_effect=watchdog.ProbeConfigurationError(
                    "Health probe /healthz returned HTTP 404; refusing automatic lifecycle action"
                ),
            ),
            patch.object(watchdog, "API") as api_factory,
        ):
            with self.assertRaises(watchdog.ProbeConfigurationError):
                watchdog.main()

        api_factory.assert_not_called()

    def test_missing_provider_key_without_dry_run_fails_closed(self):
        with (
            patch.object(watchdog, "DRY_RUN", False),
            patch.object(watchdog, "API_KEY", ""),
            patch.object(watchdog, "HEALTH_URL", "https://api.example.test/healthz"),
            patch.object(watchdog, "API") as api_factory,
        ):
            with self.assertRaisesRegex(
                watchdog.WatchdogError,
                "INSTABOX_WATCHDOG_API_KEY is required",
            ):
                watchdog.main()

        api_factory.assert_not_called()

    def test_resolves_machine_by_unique_exact_name(self):
        class ListAPI:
            def request(self, method, path, body=None, idempotency_key=None):
                return {"machines": [
                    {"id": "machine-123", "name": "testagramdb"},
                    {"id": "other-id", "name": "another-machine"}
                ], "nextCursor": None}
        with patch.object(watchdog, "MACHINE_ID", ""):
            self.assertEqual(watchdog.resolve_machine_id(ListAPI()), "machine-123")

    def test_machine_name_resolution_fails_closed_on_duplicates(self):
        class ListAPI:
            def request(self, method, path, body=None, idempotency_key=None):
                return {"machines": [
                    {"id": "machine-123", "name": "testagramdb"},
                    {"id": "machine-456", "name": "testagramdb"}
                ], "nextCursor": None}
        with patch.object(watchdog, "MACHINE_ID", ""), \
             self.assertRaisesRegex(watchdog.WatchdogError, "found 2"):
            watchdog.resolve_machine_id(ListAPI())

    def test_wait_for_service_requires_public_liveness_only(self):
        with patch.object(watchdog, "probe", side_effect=[
            (False, "UNCLASSIFIED_503: refusing automatic lifecycle action"), (True, "HTTP 200")
        ]) as probe, patch.object(watchdog.time, "sleep"):
            watchdog.wait_for_service()
        self.assertEqual(probe.call_count, 2)
        self.assertFalse(hasattr(watchdog, "READY_URL"))

    def test_probe_404_is_a_configuration_error_not_an_outage(self):
        error = watchdog.urllib.error.HTTPError(
            "https://api.example.test/healthz", 404, "Not Found", {}, None
        )
        with patch.object(watchdog.urllib.request, "urlopen", side_effect=error):
            with self.assertRaisesRegex(
                watchdog.ProbeConfigurationError,
                "HTTP 404.*refusing automatic lifecycle action",
            ):
                watchdog.probe("https://api.example.test/healthz")

    def test_probe_503_remains_a_service_failure(self):
        error = watchdog.urllib.error.HTTPError(
            "https://api.example.test/healthz", 503, "Unavailable", {}, None
        )
        with patch.object(watchdog.urllib.request, "urlopen", side_effect=error):
            self.assertEqual(
                watchdog.probe("https://api.example.test/healthz"),
                (False, "UNCLASSIFIED_503: refusing automatic lifecycle action"),
            )

    def test_machine_snapshot_rejects_wrong_machine_id(self):
        api = FakeAPI({"id": "another-machine", "state": "running", "desiredState": "running"})
        with self.assertRaisesRegex(watchdog.WatchdogError, "different machine ID"):
            watchdog.machine_snapshot(api)

    def test_machine_snapshot_requires_state_fields(self):
        api = FakeAPI({"id": "machine-123", "state": "running"})
        with self.assertRaisesRegex(watchdog.WatchdogError, "desiredState"):
            watchdog.machine_snapshot(api)

    def test_read_only_api_key_uses_x_api_key_header(self):
        class Response:
            def __enter__(self):
                return self
            def __exit__(self, *args):
                return False
            def read(self):
                return b'{}'

        with patch.object(watchdog, "API_KEY_HEADER", "X-API-Key"), \
             patch.object(watchdog.urllib.request, "urlopen", return_value=Response()) as urlopen:
            watchdog.API(base="https://api.instabox.sh", key="read-only-key").request(
                "GET", "/v1/machines"
            )
        request = urlopen.call_args.args[0]
        self.assertEqual(request.get_header("X-api-key"), "read-only-key")
        self.assertIsNone(request.get_header("Authorization"))

    def test_read_only_api_key_cannot_issue_lifecycle_actions(self):
        with patch.object(watchdog, "API_KEY_HEADER", "X-API-Key"):
            with self.assertRaisesRegex(watchdog.WatchdogError, "cannot issue lifecycle actions"):
                watchdog.API(base="https://api.instabox.sh", key="read-only-key").request(
                    "POST", "/v1/machines/machine-123/start", {}
                )

    def test_lifecycle_uses_idempotency_key_and_polls_operation(self):
        api = FakeAPI()
        watchdog.lifecycle(api, "start")
        post = api.calls[0]
        self.assertEqual(post[0], "POST")
        self.assertTrue(post[1].endswith("/start"))
        self.assertEqual(post[2], {})
        self.assertTrue(post[3].startswith("testagram-watchdog-machine-123-start-"))
        self.assertEqual(api.calls[1][1], "/v1/operations/op-1")
        self.assertEqual(api.calls[2][1], "/v1/machines/machine-123")

    def test_active_operation_is_not_interrupted(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "running",
            "desiredState": "running", "operationId": "op-in-progress"
        })
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "notify") as notify,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertEqual(len(api.calls), 1)
        notify.assert_not_called()

    def test_intentional_stop_is_never_overridden(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "stopped", "operationId": None
        })
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "notify") as notify,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertEqual(len(api.calls), 1)
        notify.assert_not_called()

    def test_unexpected_stop_is_started_when_desired_state_is_running(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "running", "operationId": None
        })
        with patch.object(watchdog, "API", return_value=api), \
             patch.object(watchdog, "load_state", return_value={"last_action_at": __import__("time").time()}), \
             patch.object(watchdog, "save_state"), \
             patch.object(watchdog, "notify") as notify, \
             patch.object(watchdog, "wait_for_service"), \
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"), \
             patch.object(watchdog, "RESTART_COOLDOWN", 300):
            self.assertEqual(watchdog.main(), 0)
        self.assertTrue(any(call[0] == "POST" and call[1].endswith("/start") for call in api.calls))
        notify.assert_called_once()

    def test_running_unhealthy_machine_is_power_cycled_only_after_all_probes_fail(self):
        api = FakeAPI()
        with patch.object(watchdog, "API", return_value=api), \
             patch.object(watchdog, "load_state", return_value={}), \
             patch.object(watchdog, "save_state"), \
             patch.object(watchdog, "notify") as notify, \
             patch.object(watchdog, "wait_for_service"), \
             patch.object(watchdog, "probe", return_value=(False, "HTTP 503")), \
             patch.object(watchdog.time, "sleep"), \
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"), \
             patch.object(watchdog, "FAILURES_REQUIRED", 3), \
             patch.object(watchdog, "RESTART_COOLDOWN", 300):
            self.assertEqual(watchdog.main(), 0)
        actions = [call[1].rsplit("/", 1)[-1] for call in api.calls if call[0] == "POST"]
        self.assertEqual(actions, ["stop", "start"])
        notify.assert_called_once()

    def test_running_machine_restart_is_stop_then_start(self):
        api = FakeAPI()
        with patch.object(watchdog, "RESTART_COOLDOWN", 300), \
             patch.object(watchdog, "DRY_RUN", False), \
             patch.object(watchdog, "save_state"), \
             patch.object(watchdog, "wait_for_service"):
            result = watchdog.restart_running(api, {})
        lifecycle_posts = [call[1].rsplit("/", 1)[-1] for call in api.calls if call[0] == "POST"]
        self.assertEqual(lifecycle_posts, ["stop", "start"])
        self.assertIn("power-cycled", result)

    def test_cooldown_prevents_repeated_restart(self):
        api = FakeAPI()
        with patch.object(watchdog, "RESTART_COOLDOWN", 1800), \
             patch.object(watchdog, "DRY_RUN", False), \
             patch.object(watchdog, "save_state"):
            result = watchdog.restart_running(api, {"last_restart_at": __import__("time").time()})
        self.assertIn("cooldown", result)
        self.assertEqual(api.calls, [])

    def test_probe_identifies_in_progress_insta_box_wake_and_retry_after(self):
        error = watchdog.urllib.error.HTTPError(
            "https://example.test/healthz",
            503,
            "Service Unavailable",
            {"Retry-After": "7"},
            io.BytesIO(b"Sandbox is waking up"),
        )
        with patch.object(watchdog.urllib.request, "urlopen", side_effect=error):
            self.assertEqual(
                watchdog.probe("https://example.test/healthz"),
                (False, "WAKE_PENDING Retry-After=7"),
            )

    def test_probe_identifies_manual_offline_and_cooldown_as_provider_states(self):
        offline = watchdog.urllib.error.HTTPError(
            "https://example.test/healthz", 503, "Offline", {},
            io.BytesIO(b"Sandbox offline"),
        )
        cooldown = watchdog.urllib.error.HTTPError(
            "https://example.test/healthz", 409, "Conflict",
            {"Retry-After": "90"}, io.BytesIO(b'{"code":"machine_cooldown"}'),
        )
        with patch.object(watchdog.urllib.request, "urlopen", side_effect=offline):
            self.assertEqual(
                watchdog.probe("https://example.test/healthz"),
                (False, "PROVIDER_OFFLINE: machine was explicitly stopped or paused"),
            )
        with patch.object(watchdog.urllib.request, "urlopen", side_effect=cooldown):
            self.assertEqual(
                watchdog.probe("https://example.test/healthz"),
                (False, "PROVIDER_COOLDOWN Retry-After=90"),
            )

    def test_running_machine_wake_pending_does_not_trigger_lifecycle_action(self):
        api = FakeAPI()
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "probe", return_value=(False, "WAKE_PENDING Retry-After=5")),
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
            patch.object(watchdog, "FAILURES_REQUIRED", 3),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertFalse(any(call[0] == "POST" for call in api.calls))

    def test_running_machine_unreachable_does_not_trigger_lifecycle_action(self):
        api = FakeAPI()
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "probe", return_value=(False, "UNREACHABLE: URLError")),
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
            patch.object(watchdog, "FAILURES_REQUIRED", 3),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertFalse(any(call[0] == "POST" for call in api.calls))

    def test_workflow_uses_separate_write_key_and_canonical_backend_health_urls(self):
        workflow = Path(".github/workflows/instabox-vps-watchdog.yml").read_text()
        self.assertIn("secrets.INSTABOX_WATCHDOG_API_KEY", workflow)
        self.assertIn("secrets.INSTABOX_READONLY_API_KEY", workflow)
        self.assertIn("WATCHDOG_AUTO_RECOVERY_ENABLED", workflow)
        self.assertIn("INSTABOX_WATCHDOG_API_KEY_HEADER=X-API-Key", workflow)
        self.assertIn("dedicated INSTABOX_WATCHDOG_API_KEY", workflow)
        self.assertIn("secrets.INSTABOX_MACHINE_ID", workflow)
        self.assertIn("vars.INSTABOX_MACHINE_NAME || 'testagramdb'", workflow)
        self.assertIn("https://8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech/healthz", workflow)
        self.assertNotIn("TESTAGRAM_READINESS_URL", workflow)
        self.assertNotIn("/readyz", workflow)
        self.assertIn("8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech", workflow)
        self.assertIn("vars.WATCHDOG_AUTO_RECOVERY_ENABLED != 'true'", workflow)


if __name__ == "__main__":
    unittest.main()
