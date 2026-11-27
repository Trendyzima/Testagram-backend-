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

    def test_retired_settings_are_not_configured_on_runtime_surfaces(self):
        retired = (
            "MAILJET_API_KEY",
            "MAILJET_SECRET_KEY",
            "SMTP_HOST",
            "SMTP_USERNAME",
            "SMTP_PASSWORD",
        )
        surfaces = (
            SCRIPT,
            WORKFLOW,
            COMPOSE,
            SMTP,
            ROOT / "deploy" / "self-hosted" / ".env.example",
            ROOT / "deploy" / "self-hosted" / "README.md",
        )
        for path in surfaces:
            content = path.read_text(encoding="utf-8")
            for name in retired:
                self.assertNotIn(name, content, f"{name} reintroduced in {path.relative_to(ROOT)}")

        # The deploy script may mention the retired sender override only in
        # its one-way cleanup command, which removes it from an old VPS .env.
        script_lines = [
            line.strip()
            for line in SCRIPT.read_text(encoding="utf-8").splitlines()
            if "AUTH_FROM_EMAIL" in line
        ]
        self.assertEqual(
            script_lines,
            ['sed -i \'/^MAILJET_/d; /^AUTH_FROM_EMAIL=/d\' "$BASE_ENV"'],
            "AUTH_FROM_EMAIL may only be referenced by the legacy-config cleanup",
        )

    def test_vps_generates_and_persists_mail_encryption_key(self):
        script = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("ensure_hex_secret TESTAGRAM_MAIL_ENCRYPTION_KEY", script)
        self.assertIn("ensure_hex_secret TESTAGRAM_MAIL_VERP_KEY", script)
        self.assertNotIn("secrets.TESTAGRAM_MAIL_ENCRYPTION_KEY", WORKFLOW.read_text(encoding="utf-8"))

    def test_native_deployment_script_has_real_line_break_before_sync(self):
        script = SCRIPT.read_text(encoding="utf-8")
        self.assertNotIn(r"used.\n\nsync_repo()", script)
        self.assertIn('\\nsync_repo() {', script)

    def test_sender_delivers_directly_to_mx_with_starttls_and_no_auth(self):
        source = SMTP.read_text(encoding="utf-8")
        self.assertIn("LookupMX", source)
        self.assertIn('JoinHostPort(host, "25")', source)
        self.assertIn("client.StartTLS", source)
        self.assertNotIn("client.Auth", source)
        self.assertIn("no-reply@testagram.site", source)


if __name__ == "__main__":
    unittest.main()
