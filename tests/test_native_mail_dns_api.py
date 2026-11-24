from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/reconcile-native-mail-dns.sh"


class NativeMailDNSAPITest(unittest.TestCase):
    def test_uses_cloudflare_api_and_scoped_token_without_persisting_it(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("api.cloudflare.com/client/v4", source)
        self.assertIn('"$API/zones?name=$DOMAIN', source)
        # The token is interpolated by printf into a mode-0600 temporary curl
        # config; avoid matching the full Authorization header in CI logs,
        # where GitHub secret masking can redact portions of the assertion.
        self.assertIn("header =", source)
        self.assertIn('"$CLOUDFLARE_DNS_API_TOKEN"', source)
        self.assertIn("mktemp -d", source)
        self.assertIn('--config "$TMP_DIR/cloudflare.curlrc"', source)
        self.assertIn('chmod 600 "$TMP_DIR/cloudflare.curlrc"', source)
        self.assertNotIn("echo $CLOUDFLARE_DNS_API_TOKEN", source)

    def test_only_reconciles_dedicated_mail_records(self):
        source = SCRIPT.read_text(encoding="utf-8")
        for name in ("mail.testagram.site", "bounces.testagram.site",
                     "tg2026._domainkey.testagram.site", "_dmarc.testagram.site"):
            self.assertIn(name, source)
        self.assertNotIn('"testagram.site" ""', source)
        self.assertIn("proxied:false", source)
        self.assertIn("priority:$priority", source)
        self.assertIn("v=spf1 ip4:$PUBLIC_IPV4 ~all", source)
        self.assertIn('TESTAGRAM_NATIVE_MAIL_PUBLIC_IPV4:-', source)
        self.assertNotIn("api.ipify.org", source)
        self.assertIn("not a globally routable IPv4 address", source)
        self.assertIn("refresh_total_pages", source)

    def test_fails_closed_on_ambiguous_zone_or_policy_records(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertIn('[[ "$ZONE_COUNT" == 1 ]]', source)
        self.assertIn('[[ "$count" -le 1 ]]', source)
        self.assertIn('[[ "$relevant_count" -le 1 ]]', source)
        self.assertIn("refusing ambiguous reconciliation", source.lower())

    def test_instabox_deployment_does_not_publish_a_nat_egress_ip_as_mail_dns(self):
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        deploy = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        self.assertNotIn("CLOUDFLARE_DNS_API_TOKEN", workflow)
        self.assertNotIn("cloudflare_dns_token", deploy)
        self.assertNotIn('bash "$BACKEND/scripts/reconcile-native-mail-dns.sh"', deploy)
        self.assertIn("cannot expose TCP/25", deploy)
        self.assertIn("dedicated SMTP host", deploy)

    def test_does_not_claim_to_configure_ptr_or_dmarc_reporting(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("PTR/rDNS is a VPS-provider setting", source)
        self.assertIn("Preserving the existing DMARC policy", source)
        self.assertNotIn("rua=mailto:", source)


if __name__ == "__main__":
    unittest.main()
