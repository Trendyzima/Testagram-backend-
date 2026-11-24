#!/usr/bin/env python3
"""Guard the production InstaBox deployment against SMTP port fallback."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"


class InstaBoxMailjetPortTest(unittest.TestCase):
    def test_deployment_is_pinned_to_starttls_port_587(self):
        script = SCRIPT.read_text(encoding="utf-8")
        self.assertIn('MAILJET_SMTP_PORT="${MAILJET_SMTP_PORT:-587}"', script)
        self.assertIn('[[ "$MAILJET_SMTP_PORT" == "587" ]] ||', script)
        self.assertIn("in-v3.mailjet.com:587", script)
        self.assertNotIn("in-v3.mailjet.com:2525", script)
        self.assertNotIn("MAILJET_SMTP_PORT=2525", script)

    def test_workflow_enforces_port_587(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("MAILJET_SMTP_PORT: '587'", workflow)
        self.assertIn('[[ "${MAILJET_SMTP_PORT:-587}" != "587" ]]', workflow)


if __name__ == "__main__":
    unittest.main()
