from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (ROOT / "scripts/start-instabox-deployment.sh").read_text()


class InstaBoxExecStdinContractTests(unittest.TestCase):
    def test_remote_script_uses_bounded_stdin_not_command_field(self):
        self.assertIn('remote_command_bytes="$(LC_ALL=C printf \'%s\' "$REMOTE_COMMAND" | wc -c', SCRIPT)
        self.assertIn("(( remote_command_bytes > 1048576 ))", SCRIPT)
        self.assertIn('--arg cmd "bash -s"', SCRIPT)
        self.assertIn('--arg stdin "$REMOTE_COMMAND"', SCRIPT)
        self.assertIn("'{cmd:$cmd,stdin:$stdin,envs:{", SCRIPT)
        self.assertIn("--data-binary @-", SCRIPT)

    def test_exec_request_is_not_retried_after_ambiguous_outcome(self):
        self.assertIn("execution outcome is unknown. Do not retry automatically.", SCRIPT)
        self.assertNotIn('curl --retry 3 --retry-all-errors --retry-delay 2 -fsS -X POST', SCRIPT)

    def test_http_error_is_reported_without_exposing_response_body(self):
        self.assertIn('if [[ "$http_status" != "200" ]]', SCRIPT)
        self.assertIn("InstaBox rejected remote deployment exec", SCRIPT)
        self.assertNotIn('jq -r \' .stderr, .stdout \'', SCRIPT)


if __name__ == "__main__":
    unittest.main()
