import unittest
from unittest.mock import patch

from scripts import instabox_watchdog as watchdog

watchdog.MACHINE_ID = "machine-123"


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

    def test_machine_snapshot_rejects_wrong_machine_id(self):
        api = FakeAPI({"id": "another-machine", "state": "running", "desiredState": "running"})
        with self.assertRaisesRegex(watchdog.WatchdogError, "different machine ID"):
            watchdog.machine_snapshot(api)

    def test_machine_snapshot_requires_state_fields(self):
        api = FakeAPI({"id": "machine-123", "state": "running"})
        with self.assertRaisesRegex(watchdog.WatchdogError, "desiredState"):
            watchdog.machine_snapshot(api)

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
        with patch.object(watchdog, "API", return_value=api), \
             patch.object(watchdog, "load_state", return_value={}), \
             patch.object(watchdog, "notify") as notify, \
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"), \
             patch.object(watchdog, "READY_URL", "https://example.test/readyz"):
            self.assertEqual(watchdog.main(), 0)
        self.assertEqual(len(api.calls), 1)
        notify.assert_not_called()

    def test_intentional_stop_is_never_overridden(self):
        api = FakeAPI({
            "id": "machine-123", "name": "testagramdb", "state": "stopped",
            "desiredState": "stopped", "operationId": None
        })
        with patch.object(watchdog, "API", return_value=api), \
             patch.object(watchdog, "load_state", return_value={}), \
             patch.object(watchdog, "notify") as notify, \
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"), \
             patch.object(watchdog, "READY_URL", "https://example.test/readyz"):
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
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"), \
             patch.object(watchdog, "READY_URL", "https://example.test/readyz"), \
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
             patch.object(watchdog, "probe", return_value=(False, "HTTP 503")), \
             patch.object(watchdog.time, "sleep"), \
             patch.object(watchdog, "API_KEY", "test-key"), \
             patch.object(watchdog, "MACHINE_ID", "machine-123"), \
             patch.object(watchdog, "HEALTH_URL", "https://example.test/healthz"), \
             patch.object(watchdog, "READY_URL", "https://example.test/readyz"), \
             patch.object(watchdog, "FAILURES_REQUIRED", 3), \
             patch.object(watchdog, "ACTION_COOLDOWN", 300):
            self.assertEqual(watchdog.main(), 0)
        actions = [call[1].rsplit("/", 1)[-1] for call in api.calls if call[0] == "POST"]
        self.assertEqual(actions, ["stop", "start"])
        notify.assert_called_once()

    def test_running_machine_restart_is_stop_then_start(self):
        api = FakeAPI()
        with patch.object(watchdog, "ACTION_COOLDOWN", 300), \
             patch.object(watchdog, "DRY_RUN", False), \
             patch.object(watchdog, "save_state"):
            result = watchdog.restart_running(api, {})
        lifecycle_posts = [call[1].rsplit("/", 1)[-1] for call in api.calls if call[0] == "POST"]
        self.assertEqual(lifecycle_posts, ["stop", "start"])
        self.assertIn("power-cycled", result)

    def test_cooldown_prevents_repeated_restart(self):
        api = FakeAPI()
        with patch.object(watchdog, "ACTION_COOLDOWN", 1800), \
             patch.object(watchdog, "DRY_RUN", False), \
             patch.object(watchdog, "save_state"):
            result = watchdog.restart_running(api, {"last_restart_at": __import__("time").time()})
        self.assertIn("cooldown", result)
        self.assertEqual(api.calls, [])


if __name__ == "__main__":
    unittest.main()
