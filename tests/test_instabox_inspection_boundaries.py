from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]


class InstaBoxInspectionBoundaryTests(unittest.TestCase):
    def test_github_workflow_is_strict_get_only(self):
        workflow = (ROOT / ".github/workflows/instabox-readonly-health.yml").read_text()
        self.assertIn("secrets.INSTABOX_READONLY_API_KEY", workflow)
        self.assertIn("GET only", workflow)
        self.assertIn("machines:read", workflow)
        self.assertIn("/v1/machines?limit=100", workflow)
        self.assertIn("/v1/machines/$INSTABOX_MACHINE_ID", workflow)
        self.assertNotIn("/exec", workflow)
        self.assertNotIn("-X POST", workflow)
        self.assertNotIn("INSTABOX_API_KEY", workflow.replace("INSTABOX_READONLY_API_KEY", ""))

    def test_operator_script_keeps_privileged_inspection_off_github(self):
        script = (ROOT / "deploy/self-hosted/inspect-production-readonly.sh").read_text()
        self.assertIn("docker inspect", script)
        self.assertIn("docker stats --no-stream", script)
        self.assertIn("apply-native-identity-migrations.sh", script)
        self.assertIn("CONFIRM_NATIVE_IDENTITY_MIGRATIONS=", script)
        self.assertNotIn("api.instabox.sh", script)
        self.assertNotIn("curl ", script)
        self.assertNotIn("docker compose up", script)
        self.assertNotIn("docker restart", script)
        self.assertNotIn("docker stop", script)
        self.assertNotIn("docker rm", script)


if __name__ == "__main__":
    unittest.main()
