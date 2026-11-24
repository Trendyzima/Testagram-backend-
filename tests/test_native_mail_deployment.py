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
        self.assertIn("ensure_hex_secret TESTAGRAM_MAIL_ENCRYPTION_KEY", script)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", compose)
        self.assertIn('chmod 600 "$BASE_ENV"', script)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", (ROOT / "cmd/api/mail_return_path.go").read_text())

    def test_instabox_uses_http_bounce_ingress_not_unreachable_smtp_port(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        main = (ROOT / "cmd/api/main.go").read_text()
        bounce = (ROOT / "cmd/api/mail_bounce.go").read_text()
        self.assertIn("TESTAGRAM_MAIL_BOUNCE_LISTEN_ADDR:-}", compose)
        self.assertNotIn('"25:2525"', compose)
        self.assertIn("startNativeBounceSMTP(signalCtx, db, bounceAddr)", main)
        self.assertIn("nativeBounceDomain", bounce)
        self.assertIn("tls.Server(conn, tlsConfig)", bounce)
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

    def test_cloudflare_bounce_ingress_is_inbound_only_and_authenticated(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        main = (ROOT / "cmd/api/main.go").read_text()
        handler = (ROOT / "cmd/api/mail_bounce_http.go").read_text()
        worker = (ROOT / "cloudflare/email-bounce-worker/src/index.ts").read_text()
        wrangler = (ROOT / "cloudflare/email-bounce-worker/wrangler.toml").read_text()
        script = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        self.assertIn("TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET", compose)
        self.assertIn("TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET", script)
        self.assertIn("ensure_hex_secret TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET", script)
        self.assertIn("nativeBounceIngressPath", main)
        self.assertIn("hmac.New(sha256.New", handler)
        self.assertIn("nativeBounceIngressMaxSkew", handler)
        self.assertIn("processNativeBounceReport", handler)
        self.assertIn("message.raw", worker)
        self.assertIn('method: "POST"', worker)
        self.assertIn("HMAC", worker)
        self.assertNotIn("env.EMAIL.send", worker)
        self.assertIn('addresses = ["b@bounces.testagram.site"]', wrangler)

    def test_instabox_dns_and_cloudflare_bounce_worker_are_deployed_without_smtp_ingress(self):
        deploy = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        dns = (ROOT / "scripts/reconcile-instabox-native-mail-dns.sh").read_text()
        worker_deploy = (ROOT / "scripts/deploy-cloudflare-bounce-worker.sh").read_text()
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        self.assertIn("verify-instabox-native-mail-egress.sh", deploy)
        self.assertIn("reconcile-instabox-native-mail-dns.sh", deploy)
        self.assertIn("deploy-cloudflare-bounce-worker.sh", deploy)
        self.assertIn("CLOUDFLARE_DNS_API_TOKEN", workflow)
        self.assertIn("CLOUDFLARE_BOUNCE_WORKER_API_TOKEN", workflow)
        self.assertIn("TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4", workflow)
        self.assertIn("proxied:false", dns)
        self.assertIn("include:_spf.mx.cloudflare.net", dns)
        self.assertIn("ip4:$PUBLIC_IPV4", dns)
        self.assertIn("tg2026._domainkey.testagram.site", dns)
        self.assertNotIn('write_record MX', dns)
        self.assertIn('"support_subaddress":true', worker_deploy)
        self.assertIn("wrangler deploy", worker_deploy)
        self.assertIn("wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET", worker_deploy)
        self.assertIn("bounces.testagram.site", worker_deploy)
        self.assertIn("api.testagram.site/internal/mail/bounce", worker_deploy)

    def test_read_only_instabox_egress_diagnostic_cannot_deploy(self):
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        job_start = workflow.index("  diagnose-native-mail-egress:")
        job_end = workflow.index("  deploy:", job_start)
        diagnostic = workflow[job_start:job_end]
        self.assertIn("inputs.diagnose_native_mail_egress == true", diagnostic)
        self.assertIn("inputs.confirm_production_deploy != true", diagnostic)
        self.assertIn("/v1/machines/$INSTABOX_MACHINE_ID/exec", diagnostic)
        self.assertIn("socket.create_connection", diagnostic)
        self.assertIn("socket.gethostbyaddr", diagnostic)
        self.assertNotIn("CLOUDFLARE_DNS_API_TOKEN", diagnostic)
        self.assertNotIn("wrangler deploy", diagnostic)
        self.assertNotIn("docker compose", diagnostic)

    def test_verp_hmac_preserves_case_sensitive_token(self):
        handler = (ROOT / "cmd/api/mail_bounce_http.go").read_text()
        worker = (ROOT / "cloudflare/email-bounce-worker/src/index.ts").read_text()
        self.assertIn("canonicalNativeBounceRecipient", handler)
        self.assertIn("recipient[:at]", handler)
        self.assertNotIn("message.to.trim().toLowerCase()", worker)
        self.assertIn("rawRecipient.slice(0, at)", worker)
        self.assertIn("rawRecipient.slice(at + 1).toLowerCase()", worker)
        self.assertIn("authentication-results", worker)
        self.assertIn("mx\\\\.cloudflare", worker)

    def test_native_sender_has_no_external_mail_provider_requirement(self):
        smtp = (ROOT / "cmd/api/mail_smtp.go").read_text()
        script = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        env_example = (ROOT / "deploy/self-hosted/.env.example").read_text()
        self.assertNotIn("MAILJET_SMTP_USERNAME", env_example)
        self.assertNotIn("MAILJET_SMTP_PASSWORD", env_example)
        self.assertNotIn("AUTH_FROM_EMAIL", env_example)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", env_example)
        self.assertIn("TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET", env_example)
        self.assertIn("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", env_example)
        self.assertIn("LookupMX", smtp)
        self.assertIn("client.StartTLS", smtp)
        self.assertNotIn("MAILJET_API_KEY", script)
        self.assertNotIn("MAILJET_SECRET_KEY", script)
        self.assertIn("native DKIM signing failed", smtp)
        self.assertIn("TESTAGRAM_MAIL_VERP_KEY", (ROOT / "cmd/api/mail_return_path.go").read_text())


if __name__ == "__main__":
    unittest.main()
