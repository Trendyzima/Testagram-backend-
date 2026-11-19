from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ROOT / "deploy" / "self-hosted" / "docker-compose.yml"
DEPLOY = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"


class TestInstaBoxRuntimeHealthcheckPaths(unittest.TestCase):
    def test_compose_uses_the_path_baked_into_the_immutable_cdn_runtime_image(self):
        compose = COMPOSE.read_text(encoding="utf-8")
        self.assertIn('test: ["CMD", "/healthcheck"]', compose)
        self.assertNotIn("/app/healthcheck", compose)

    def test_deployment_and_post_deploy_checks_use_the_same_binary_path(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn('/healthcheck', deploy)
        self.assertIn('/healthcheck', workflow)
        self.assertNotIn("/app/healthcheck", deploy)
        self.assertNotIn("/app/healthcheck", workflow)


if __name__ == "__main__":
    unittest.main()
