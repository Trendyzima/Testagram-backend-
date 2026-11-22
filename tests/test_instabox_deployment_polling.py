from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / ".github/workflows/instabox-deploy.yml").read_text(encoding="utf-8")


class InstaBoxDeploymentPollingTests(unittest.TestCase):
    def test_transient_control_plane_failures_are_retried(self):
        step = WORKFLOW.split("- name: Wait for deployment and report logs", 1)[1].split("\n      - name:", 1)[0]
        self.assertIn("--connect-timeout 8 --max-time 20", step)
        self.assertIn('2>"$RUNNER_TEMP/instabox-poll-curl.err"', step)
        self.assertIn("InstaBox control plane is temporarily unreachable", step)
        self.assertIn("sleep 25", step)
        self.assertIn("continue", step)

    def test_non_execution_responses_retry_and_real_remote_failure_stays_fatal(self):
        step = WORKFLOW.split("- name: Wait for deployment and report logs", 1)[1].split("\n      - name:", 1)[0]
        self.assertIn('has("exitCode")', step)
        self.assertIn("non-execution response", step)
        self.assertIn("Remote deployment failed. Review the machine deployment log.", step)

    def test_success_marker_is_still_required(self):
        step = WORKFLOW.split("- name: Wait for deployment and report logs", 1)[1].split("\n      - name:", 1)[0]
        self.assertIn("DEPLOY_EXIT=0", step)
        self.assertIn("DEPLOY_EXIT=", step)


if __name__ == "__main__":
    unittest.main()
