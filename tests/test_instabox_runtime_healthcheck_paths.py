from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ROOT / "deploy" / "self-hosted" / "docker-compose.yml"
DEPLOY = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"
STACK_WORKFLOW = ROOT / ".github" / "workflows" / "self-hosted-stack.yml"


class TestInstaBoxRuntimeHealthcheckPaths(unittest.TestCase):
    def test_compose_uses_the_path_baked_into_the_immutable_cdn_runtime_image(self):
        compose = COMPOSE.read_text(encoding="utf-8")
        self.assertIn('test: ["CMD", "/healthcheck"]', compose)
        self.assertNotIn("/app/healthcheck", compose)

    def test_deployment_and_post_deploy_checks_use_the_same_binary_path(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        workflow = WORKFLOW.read_text(encoding="utf-8")
        stack_workflow = STACK_WORKFLOW.read_text(encoding="utf-8")
        self.assertIn('/healthcheck', deploy)
        self.assertIn('/healthcheck', workflow)
        self.assertIn('video-worker', deploy)
        self.assertIn('if [[ "$video_worker_health" != "healthy" ]]', deploy)
        self.assertIn('for attempt in $(seq 1 60); do', deploy)
        self.assertIn('[[ "$video_worker_health" == "healthy" ]] && break', deploy)
        self.assertIn('within 120 seconds', deploy)
        self.assertIn('video worker readiness healthcheck did not report healthy', deploy)
        self.assertIn("{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}", deploy)
        self.assertNotIn("/app/healthcheck", deploy)
        self.assertNotIn("/app/healthcheck", workflow)
        self.assertIn('if "/healthcheck" not in health:', stack_workflow)
        self.assertNotIn('if "/app/healthcheck" not in health:', stack_workflow)


if __name__ == "__main__":
    unittest.main()
