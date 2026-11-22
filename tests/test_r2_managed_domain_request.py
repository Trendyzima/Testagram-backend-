#!/usr/bin/env python3
"""Regression guard for the Cloudflare R2 managed-domain JSON request."""
from pathlib import Path
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/instabox-deploy.yml"


class R2ManagedDomainRequestTests(unittest.TestCase):
    def test_disable_request_is_generated_validated_and_sent_as_json_bytes(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn(r'''jq -cn '{enabled:false}' | head -c 17 > "$managed_request"''', source)
        self.assertIn("jq -e 'type == \"object\" and .enabled == false and (keys == [\"enabled\"])' \"$managed_request\"", source)
        self.assertIn("-H 'Content-Type: application/json'", source)
        self.assertIn("--data-binary \"@$managed_request\"", source)
        self.assertIn(".success == true and .result.enabled == false", source)

    def test_public_access_verification_remains_fail_closed(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("R2 r2.dev public access is not verified disabled.", source)
        self.assertIn("R2 identity-evidence bucket has an enabled public custom domain or its domain state cannot be verified.", source)


if __name__ == "__main__":
    unittest.main()
