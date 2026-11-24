from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class NativeMailDeploymentTest(unittest.TestCase):
    def test_vps_owns_and_mounts_dkim_private_key(self):
        script = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        self.assertIn("openssl genpkey -algorithm RSA", script)
        self.assertIn('chmod 600 "$MAIL_KEY_DIR/dkim-private.pem"', script)
        self.assertIn('chown 100:100 "$MAIL_KEY_DIR"', script)
        self.assertIn("/data/home/testagram/mail-keys:/run/testagram-mail:ro", compose)
        self.assertIn("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", compose)
        self.assertIn("ensure_hex_secret TESTAGRAM_MAIL_VERP_KEY", script)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", compose)
        self.assertIn('chmod 600 "$BASE_ENV"', script)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", (ROOT / "cmd/api/mail_return_path.go").read_text())

    def test_native_bounce_receiver_is_wired_to_port_25(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        main = (ROOT / "cmd/api/main.go").read_text()
        bounce = (ROOT / "cmd/api/mail_bounce.go").read_text()
        self.assertIn('"25:2525"', compose)
        self.assertIn("startNativeBounceSMTP(signalCtx, db, bounceAddr)", main)
        self.assertIn("nativeBounceDomain", bounce)
        self.assertIn("tls.Server(conn, tlsConfig)", bounce)
        self.assertIn("TESTAGRAM_MAIL_TLS_CERT_FILE", compose)
        self.assertIn("recipient does not match the original message", bounce)

    def test_native_feedback_migration_is_mounted_and_applied(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        script = (ROOT / "deploy/self-hosted/apply-native-identity-migrations.sh").read_text()
        migration = (ROOT / "migrations/20261126_testagram_native_mail_feedback.sql").read_text()
        self.assertIn("20261126_testagram_native_mail_feedback.sql", compose)
        self.assertIn("apply_migration 20261126_testagram_native_mail_feedback.sql", script)
        self.assertIn("return_path_tokens", migration)
        self.assertIn("inbound_reports", migration)
        self.assertIn("'mailjet.bounce'", migration)

    def test_native_sender_has_no_external_mail_provider_requirement(self):
        smtp = (ROOT / "cmd/api/mail_smtp.go").read_text()
        script = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        env_example = (ROOT / "deploy/self-hosted/.env.example").read_text()
        self.assertNotIn("MAILJET_SMTP_USERNAME", env_example)
        self.assertNotIn("MAILJET_SMTP_PASSWORD", env_example)
        self.assertNotIn("AUTH_FROM_EMAIL", env_example)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", env_example)
        self.assertIn("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", env_example)
        self.assertIn("LookupMX", smtp)
        self.assertIn("client.StartTLS", smtp)
        self.assertNotIn("MAILJET_API_KEY", script)
        self.assertNotIn("MAILJET_SECRET_KEY", script)
        self.assertIn("native DKIM signing failed", smtp)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", (ROOT / "cmd/api/mail.go").read_text())


if __name__ == "__main__":
    unittest.main()
