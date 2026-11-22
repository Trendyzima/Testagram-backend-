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
    def test_missing_provider_key_fails_closed_without_public_probe(self):
        with (
            patch.object(watchdog, "DRY_RUN", True),
            patch.object(watchdog, "API_KEY", ""),
            patch.object(watchdog, "HEALTH_URL", "https://api.example.test/healthz"),
            patch.object(watchdog, "probe", return_value=(True, "HTTP 200")) as probe,
            patch.object(watchdog, "API") as api_factory,
        ):
            with self.assertRaisesRegex(watchdog.WatchdogError, "INSTABOX_WATCHDOG_API_KEY is required"):
                watchdog.main()
        probe.assert_not_called()
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
            (False, "HTTP 503"), (True, "HTTP 200")
        ]) as probe, patch.object(watchdog.time, "sleep"):
            watchdog.wait_for_service()
        self.assertEqual(probe.call_count, 2)
        self.assertFalse(hasattr(watchdog, "READY_URL"))

    def test_post_resume_404_does_not_erase_provider_resume_success(self):
        with patch.object(
            watchdog, "probe",
            side_effect=watchdog.ProbeConfigurationError(
                "Health probe /healthz returned HTTP 404; refusing automatic lifecycle action until the probe contract is corrected"
            ),
        ):
            result = watchdog.wait_for_service()
        self.assertIn("application liveness unverified", result)
        self.assertIn("HTTP 404", result)

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
                (False, "HTTP 503"),
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

    def test_provider_cooldown_fails_visibly_instead_of_reporting_success(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "suspended",
            "desiredState": "suspended", "operationId": None,
            "cooldownUntil": "2099-01-01T00:00:00Z"
        })
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
        ):
            with self.assertRaisesRegex(watchdog.WatchdogError, "provider cooldown is active"):
                watchdog.main()

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

    def test_stopped_machine_is_restarted_even_when_desired_state_is_stopped(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "stopped", "operationId": None
        })
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "wait_for_service"),
            patch.object(watchdog, "notify") as notify,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "AUTO_RECOVERY_RAW", "true"),
            patch.object(watchdog, "DRY_RUN", False),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
        ):
            self.assertEqual(watchdog.main(), 0)
        actions = [call[1].rsplit("/", 1)[-1] for call in api.calls if call[0] == "POST"]
        self.assertEqual(actions, ["start"])
        notify.assert_called_once()

    def test_unexpected_stop_is_started_when_desired_state_is_running(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "running", "operationId": None
        })
        with patch.object(watchdog, "API", return_value=api), \
             patch.object(watchdog, "load_state", return_value={}), \
             patch.object(watchdog, "save_state"), \
             patch.object(watchdog, "notify") as notify, \
             patch.object(watchdog, "wait_for_service"), \
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "AUTO_RECOVERY_RAW", "true"), \
             patch.object(watchdog, "DRY_RUN", False), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"):

            self.assertEqual(watchdog.main(), 0)
        self.assertTrue(any(call[0] == "POST" and call[1].endswith("/start") for call in api.calls))
        notify.assert_called_once()



    def test_recent_recovery_attempt_obeys_local_cooldown(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "running", "operationId": None
        })
        with (
            patch.object(watchdog, "AUTO_RECOVERY_RAW", "true"),
            patch.object(watchdog, "DRY_RUN", False),
            patch.object(watchdog, "RESTART_COOLDOWN", 900),
            patch.object(watchdog.time, "time", return_value=10_000),
            patch.object(watchdog, "save_state"),
            self.assertRaisesRegex(watchdog.WatchdogError, "local recovery cooldown active"),
        ):
            watchdog.recover_stopped(api, {"last_action_at": 9_500})
        self.assertFalse(any(call[0] == "POST" for call in api.calls))

    def test_invalid_future_state_timestamp_fails_closed(self):
        with (
            patch.object(watchdog, "AUTO_RECOVERY_RAW", "true"),
            patch.object(watchdog, "DRY_RUN", False),
            patch.object(watchdog.time, "time", return_value=10_000),
        ):
            with self.assertRaisesRegex(watchdog.WatchdogError, "timestamp is in the future"):
                watchdog.recover_stopped(FakeAPI(), {"last_action_at": 20_000})

    def test_stopped_machine_stays_read_only_without_explicit_recovery_approval(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "running", "operationId": None
        })
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "notify") as notify,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "AUTO_RECOVERY_RAW", "false"),
            patch.object(watchdog, "DRY_RUN", False),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertEqual([call[0] for call in api.calls], ["GET"])
        notify.assert_not_called()

    def test_invalid_recovery_switch_fails_closed(self):
        with (
            patch.object(watchdog, "AUTO_RECOVERY_RAW", "yes"),
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
        ):
            with self.assertRaisesRegex(
                watchdog.WatchdogError,
                "must be exactly true or false",
            ):
                watchdog.main()

    def test_running_machine_probes_liveness_but_never_restarts(self):
        api = FakeAPI()
        state = {}
        with (
            patch.object(watchdog, "probe", return_value=(True, "HTTP 200")) as probe,
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "lifecycle") as lifecycle,
        ):
            result = watchdog.check_running_service(api, state)
        self.assertIn("no restart triggered", result)
        probe.assert_called_once()
        lifecycle.assert_not_called()
        self.assertFalse(any(call[0] == "POST" for call in api.calls))
        self.assertEqual(state["consecutive_health_failures"], 0)

    def test_running_machine_probes_liveness_and_does_not_restart(self):
        api = FakeAPI()
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "probe", return_value=(True, "HTTP 200")) as probe,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertEqual([call[0] for call in api.calls], ["GET"])
        probe.assert_called_once()

    def test_running_machine_with_failing_health_probe_still_never_restarts(self):
        api = FakeAPI()
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "probe", return_value=(False, "HTTP 503")) as probe,
            patch.object(watchdog, "notify") as notify,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
        ):
            self.assertEqual(watchdog.main(), 0)
        self.assertEqual(probe.call_count, 3)
        self.assertEqual([call[0] for call in api.calls], ["GET"])
        notify.assert_called_once()

    def test_suspended_machine_is_force_resumed_even_when_desired_state_is_suspended(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "suspended",
            "desiredState": "suspended", "operationId": None
        })
        with (
            patch.object(watchdog, "API", return_value=api),
            patch.object(watchdog, "load_state", return_value={}),
            patch.object(watchdog, "save_state"),
            patch.object(watchdog, "wait_for_service"),
            patch.object(watchdog, "notify") as notify,
            patch.object(watchdog, "API_KEY", "test-key"),
            patch.object(watchdog, "AUTO_RECOVERY_RAW", "true"),
            patch.object(watchdog, "DRY_RUN", False),
            patch.object(watchdog, "MACHINE_ID", "machine-123"),
            patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"),
        ):
            self.assertEqual(watchdog.main(), 0)
        actions = [call[1].rsplit("/", 1)[-1] for call in api.calls if call[0] == "POST"]
        self.assertEqual(actions, ["resume"])
        notify.assert_called_once()

    def test_existing_watchdog_secret_uses_authorization_for_provider_requests(self):
        class Response:
            def __enter__(self):
                return self
            def __exit__(self, *args):
                return False
            def read(self):
                return b'{}'

        with patch.object(watchdog.urllib.request, "urlopen", return_value=Response()) as urlopen:
            watchdog.API(base="https://api.instabox.sh", key="existing-secret").request(
                "GET", "/v1/machines"
            )
        request = urlopen.call_args.args[0]
        self.assertEqual(request.get_header("Authorization"), "Bearer existing-secret")
        self.assertIsNone(request.get_header("X-api-key"))

    def test_default_machine_name_is_testagramdb(self):
        source = Path("scripts/instabox_watchdog.py").read_text()
        self.assertIn('MACHINE_NAME = os.getenv("INSTABOX_MACHINE_NAME", "testagramdb")', source)

    def test_workflow_reuses_existing_secret_and_preserves_pr_validation(self):
        workflow = Path(".github/workflows/instabox-vps-watchdog.yml").read_text()
        self.assertIn("secrets.INSTABOX_WATCHDOG_API_KEY || secrets.INSTABOX_API_KEY", workflow)
        self.assertIn("WATCHDOG_AUTO_RECOVERY_ENABLED", workflow)
        self.assertIn("export INSTABOX_WATCHDOG_API_KEY_HEADER=Authorization", workflow)
        self.assertNotIn("INSTABOX_READONLY_API_KEY", workflow)
        self.assertIn("secrets.INSTABOX_MACHINE_ID", workflow)
        self.assertIn("vars.INSTABOX_MACHINE_NAME || 'testagramdb'", workflow)
        self.assertIn("https://api.testagram.site/healthz", workflow)
        self.assertIn("provider reports VPS running but public API liveness failed three consecutive probes", Path("scripts/instabox_watchdog.py").read_text())
        self.assertIn("idle-suspended", Path("scripts/instabox_watchdog.py").read_text())
        self.assertNotIn("WATCHDOG_FAILURES_REQUIRED", workflow)
        self.assertNotIn("WATCHDOG_RESTART_COOLDOWN_SECONDS", workflow)
        self.assertNotIn("TESTAGRAM_READINESS_URL", workflow)
        self.assertNotIn("/readyz", workflow)
        self.assertNotIn("8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech", workflow)
        self.assertIn("vars.WATCHDOG_AUTO_RECOVERY_ENABLED || 'false'", workflow)
        self.assertNotIn('  schedule:', workflow)
        self.assertNotIn('*/5 * * * *', workflow)
        self.assertIn('operator-dispatched only', workflow)
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.dry_run", workflow)
        self.assertIn("pull_request:", workflow)


if __name__ == "__main__":
    unittest.main()
