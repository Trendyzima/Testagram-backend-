#!/usr/bin/env python3
"""Guard the production InstaBox deployment against SMTP-only egress dependencies."""
from pathlib import Path
import unittest
ROOT=Path(__file__).resolve().parents[1]
SCRIPT=ROOT/"scripts"/"start-instabox-deployment.sh"
WORKFLOW=ROOT/".github"/"workflows"/"instabox-deploy.yml"
COMPOSE=ROOT/"deploy"/"self-hosted"/"docker-compose.yml"
class InstaBoxMailjetTransportTest(unittest.TestCase):
 def test_deployment_uses_mailjet_https_api_on_port_443(self):
  script=SCRIPT.read_text(encoding="utf-8")
  self.assertIn("api.mailjet.com:443",script);self.assertIn("MAILJET_API_KEY",script);self.assertIn("MAILJET_SECRET_KEY",script)
  self.assertNotIn("in-v3.mailjet.com",script);self.assertNotIn("MAILJET_SMTP_PORT",script)
 def test_workflow_maps_existing_mailjet_production_secrets(self):
  workflow=WORKFLOW.read_text(encoding="utf-8")
  self.assertIn("MAILJET_API_KEY: ${{ secrets.MAILJET_API_KEY }}",workflow);self.assertIn("MAILJET_SECRET_KEY: ${{ secrets.MAILJET_SECRET_KEY }}",workflow)
  self.assertNotIn("MAILJET_SMTP",workflow)
 def test_api_container_receives_mailjet_api_credentials(self):
  compose=COMPOSE.read_text(encoding="utf-8")
  self.assertIn("MAILJET_API_KEY: ${MAILJET_API_KEY:-}",compose);self.assertIn("MAILJET_SECRET_KEY: ${MAILJET_SECRET_KEY:-}",compose)
  self.assertNotIn("MAILJET_SMTP",compose)
if __name__=="__main__":unittest.main()
