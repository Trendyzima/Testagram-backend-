import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class InstaBoxNativeMailEgressTests(unittest.TestCase):
    def test_egress_check_requires_provider_confirmed_ipv4_and_forward_reverse_dns(self):
        script = (ROOT / "scripts/verify-instabox-native-mail-egress.sh").read_text()
        self.assertIn("TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4", script)
        self.assertIn("api.ipify.org", script)
        self.assertIn("checkip.amazonaws.com", script)
        self.assertIn("socket.gethostbyaddr", script)
        self.assertIn("mail.testagram.site", script)
        self.assertIn("socket.gethostbyaddr", script)
        self.assertIn("address.is_global", script)

    def test_deploy_requires_the_confirmed_egress_ip(self):
        deploy = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        self.assertIn("verify-instabox-native-mail-egress.sh", deploy)
        self.assertIn("expected_mail_ipv4", deploy)
        self.assertIn("TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4", workflow)

    def test_instabox_disables_inbound_smtp_listener_and_host_port_probe(self):
        deploy = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        self.assertIn('write_env_value TESTAGRAM_MAIL_BOUNCE_LISTEN_ADDR ""', deploy)
        self.assertNotIn('write_env_value TESTAGRAM_MAIL_BOUNCE_LISTEN_ADDR ":2525"', deploy)
        self.assertNotIn('/dev/tcp/127.0.0.1/25', deploy)
        self.assertNotIn('"25:2525"', compose)


if __name__ == "__main__":
    unittest.main()
