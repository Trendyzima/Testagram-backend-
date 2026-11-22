#!/usr/bin/env python3
"""Regression guard for the Cloudflare R2 managed-domain JSON request."""
from pathlib import Path
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/instabox-deploy.yml"


class R2ManagedDomainRequestTests(unittest.TestCase):
    def test_disable_request_is_generated_validated_and_sent_as_json_bytes(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn('body = json.dumps({"enabled": False}, separators=(",", ":")).encode("utf-8")', source)
        self.assertIn("urllib.request.Request(", source)
        self.assertIn('api_token = os.environ["CLOUDFLARE_API_TOKEN"].strip()', source)
        self.assertIn("api_token.isascii()", source)
        self.assertIn("invalid header characters", source)
        self.assertIn('"Content-Type": "application/json"', source)
        self.assertIn('method="PUT"', source)
        self.assertIn(".success == true and .result.enabled == false", source)

    def test_public_access_verification_remains_fail_closed(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("R2 r2.dev public access is not verified disabled.", source)
        self.assertIn("R2 identity-evidence bucket has an enabled public custom domain or its domain state cannot be verified.", source)


if __name__ == "__main__":
    unittest.main()

