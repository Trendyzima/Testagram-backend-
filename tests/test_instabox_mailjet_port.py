#!/usr/bin/env python3
"""Guard native Testagram mail against external mail-provider dependencies."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"
COMPOSE = ROOT / "deploy" / "self-hosted" / "docker-compose.yml"
SMTP = ROOT / "cmd" / "api" / "mail_smtp.go"

class NativeMailDeploymentTest(unittest.TestCase):
    def test_no_external_provider_credentials_or_api(self):
        for path in (SCRIPT, WORKFLOW, COMPOSE, SMTP):
            content = path.read_text(encoding="utf-8")
            self.assertNotIn("MAILJET_API_KEY", content, str(path))
            self.assertNotIn("MAILJET_SECRET_KEY", content, str(path))
            self.assertNotIn("api.mailjet.com", content, str(path))

    def test_vps_generates_and_persists_mail_encryption_key(self):
        script = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("ensure_random_secret TESTAGRAM_MAIL_ENCRYPTION_KEY", script)
        self.assertNotIn("secrets.TESTAGRAM_MAIL_ENCRYPTION_KEY", WORKFLOW.read_text(encoding="utf-8"))

    def test_native_deployment_script_has_real_line_break_before_sync(self):
        script = SCRIPT.read_text(encoding="utf-8")
        self.assertNotIn(r"used.\\n\\nsync_repo()", script)
        self.assertIn('\nsync_repo() {', script)

    def test_sender_delivers_directly_to_mx_with_starttls_and_no_auth(self):
        source = SMTP.read_text(encoding="utf-8")
        self.assertIn("LookupMX", source)
        self.assertIn('JoinHostPort(host, "25")', source)
        self.assertIn("client.StartTLS", source)
        self.assertNotIn("client.Auth", source)
        self.assertIn("no-reply@testagram.site", source)

if __name__ == "__main__":
    unittest.main()
