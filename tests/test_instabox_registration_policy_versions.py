from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"
IDENTITY = ROOT / "cmd" / "api" / "identity_native.go"


class TestInstaBoxRegistrationPolicyVersions(unittest.TestCase):
    def test_workflow_uses_the_policy_versions_shown_by_flutter_when_vars_are_absent(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("TESTAGRAM_TERMS_VERSION: ${{ vars.TESTAGRAM_TERMS_VERSION || '2026-11-11' }}", workflow)
        self.assertIn("TESTAGRAM_PRIVACY_VERSION: ${{ vars.TESTAGRAM_PRIVACY_VERSION || '2026-11-11' }}", workflow)

    def test_deployment_normalizes_and_persists_nonempty_policy_versions(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        self.assertIn("normalize_policy_version TESTAGRAM_TERMS_VERSION 2026-11-11", deploy)
        self.assertIn("normalize_policy_version TESTAGRAM_PRIVACY_VERSION 2026-11-11", deploy)
        self.assertIn('write_env_value TESTAGRAM_TERMS_VERSION "$TESTAGRAM_TERMS_VERSION"', deploy)
        self.assertIn('write_env_value TESTAGRAM_PRIVACY_VERSION "$TESTAGRAM_PRIVACY_VERSION"', deploy)

    def test_registration_handler_rejects_missing_policy_versions(self):
        identity = IDENTITY.read_text(encoding="utf-8")
        self.assertIn('env("TESTAGRAM_TERMS_VERSION", "")', identity)
        self.assertIn('env("TESTAGRAM_PRIVACY_VERSION", "")', identity)
        self.assertIn("REGISTRATION_POLICY_NOT_CONFIGURED", identity)


if __name__ == "__main__":
    unittest.main()
