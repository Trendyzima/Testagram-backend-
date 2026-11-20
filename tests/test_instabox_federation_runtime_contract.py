from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]


class TestInstaBoxFederationRuntimeContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = (ROOT / "scripts/start-instabox-deployment.sh").read_text()

    def test_federation_database_uses_a_persistent_least_privilege_role(self):
        self.assertIn('FEDERATION_DATABASE_USER="$(read_dc_env_value FEDERATION_DATABASE_USER)"', self.script)
        self.assertIn('FEDERATION_DATABASE_PASSWORD="$(read_dc_env_value FEDERATION_DATABASE_PASSWORD)"', self.script)
        self.assertIn('FEDERATION_DATABASE_PASSWORD="$(openssl rand -hex 32)"', self.script)
        self.assertIn('write_dc_env_value FEDERATION_DATABASE_USER "$FEDERATION_DATABASE_USER"', self.script)
        self.assertIn('write_dc_env_value FEDERATION_DATABASE_PASSWORD "$FEDERATION_DATABASE_PASSWORD"', self.script)
        self.assertIn('postgresql://${FEDERATION_DATABASE_USER}:${FEDERATION_DATABASE_PASSWORD}@federation-db:5432/${FEDERATION_DATABASE_NAME}?sslmode=disable', self.script)
        self.assertNotIn('postgresql://testagram:$POSTGRES_PASSWORD@federation-db:', self.script)

    def test_media_database_uses_a_persistent_least_privilege_role(self):
        self.assertIn('MEDIA_DATABASE_USER="$(read_dc_env_value MEDIA_DATABASE_USER)"', self.script)
        self.assertIn('MEDIA_DATABASE_PASSWORD="$(read_dc_env_value MEDIA_DATABASE_PASSWORD)"', self.script)
        self.assertIn('MEDIA_DATABASE_PASSWORD="$(openssl rand -hex 32)"', self.script)
        self.assertIn('write_dc_env_value MEDIA_DATABASE_USER "$MEDIA_DATABASE_USER"', self.script)
        self.assertIn('write_dc_env_value MEDIA_DATABASE_PASSWORD "$MEDIA_DATABASE_PASSWORD"', self.script)
        self.assertIn('postgresql://${MEDIA_DATABASE_USER}:${MEDIA_DATABASE_PASSWORD}@federation-db:5432/${MEDIA_DATABASE_NAME}?sslmode=disable', self.script)
        self.assertNotIn('postgresql://testagram:$POSTGRES_PASSWORD@postgres:5432/testagram_media', self.script)

    def test_federation_identity_uses_the_canonical_apex(self):
        self.assertIn('write_dc_env_value FEDERATION_PUBLIC_BASE_URL "https://testagram.site"', self.script)
        self.assertIn('write_dc_env_value FEDERATION_INSTANCE_DOMAIN "testagram.site"', self.script)
        self.assertNotIn('FEDERATION_PUBLIC_BASE_URL=https://federation.testagram.site', self.script)

    def test_existing_environment_syncs_algorithm_token_and_activates_workers(self):
        self.assertIn('write_dc_env_value ALGORITHM_INTERNAL_TOKEN "$ALGORITHM_INTERNAL_TOKEN"', self.script)
        for flag in (
            "FEDERATION_DELIVERY_WORKER_ENABLED",
            "FEDERATION_OUTBOX_POLLER_ENABLED",
            "FEDERATION_INBOX_WORKER_ENABLED",
            "FEDERATION_INBOX_ACCEPT_ENABLED",
        ):
            self.assertIn(f"write_dc_env_value {flag} true", self.script)
        self.assertIn("/internal/v1/worker-status", self.script)
        self.assertIn(".workers.delivery_worker == true", self.script)
        self.assertIn(".workers.outbox_poller == true", self.script)
        self.assertIn(".workers.inbox_worker == true", self.script)
        self.assertIn(".workers.inbox_accept == true", self.script)

    def test_algorithm_deployment_probe_sends_the_required_timestamp_and_feed_mode(self):
        probe = self.script.split("algorithm_response=", 1)[1].split("\n", 1)[0]
        self.assertIn("2026-11-20T00:00:00Z", probe)
        self.assertIn("feedMode", probe)
        self.assertIn("candidates", probe)
        self.assertIn("X-Testagram-API-Version: 1", probe)
        self.assertIn("X-Request-ID: testagram-deploy-acceptance", probe)

    def test_deployment_health_gate_requires_runtime_services_not_optional_monitoring(self):
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        health_gate = workflow.split("containers=(", 1)[1].split(")", 1)[0]
        for required in (
            "testagram-self-hosted-postgres-1",
            "testagram-self-hosted-api-1",
            "testagram-self-hosted-object-storage-1",
            "testagram-self-hosted-media-cache-1",
            "testagram-data-centre-algorithm-1",
            "testagram-data-centre-federation-api-1",
        ):
            self.assertIn(required, health_gate)
        for optional in (
            "testagram-self-hosted-node-exporter-1",
            "testagram-self-hosted-alertmanager-1",
            "testagram-self-hosted-grafana-1",
        ):
            self.assertNotIn(optional, health_gate)

    def test_apex_health_probe_is_available_on_the_canonical_host(self):
        caddy = (ROOT / "deploy/self-hosted/Caddyfile").read_text()
        apex = caddy.split("testagram.site {", 1)[1].split("\n}", 1)[0]
        self.assertIn("handle /_testagram/edge-health {", apex)
        self.assertIn('respond `{"ok":true,"service":"testagram-apex-edge"}` 200', apex)
        edge = caddy.split(":8443 {", 1)[1].split("\n}", 1)[0]
        self.assertIn("handle /_testagram/edge-health {", edge)
        self.assertIn('respond `{"ok":true,"service":"testagram-apex-edge"}` 200', edge)
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        self.assertNotIn('Host: testagram.site', workflow)

    def test_go_cdn_origin_port_is_bound_for_instabox_publication(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        media_cache = compose.split("  media-cache:", 1)[1].split("\n  caddy:", 1)[0]
        self.assertIn('127.0.0.1:8081:8081', media_cache)
        self.assertIn('LISTEN_ADDR: ":8081"', media_cache)
        self.assertNotIn('0.0.0.0:8081', media_cache)

    def test_self_hosted_stack_allows_only_loopback_go_cdn_public_port(self):
        stack = (ROOT / ".github/workflows/self-hosted-stack.yml").read_text()
        self.assertIn('cdn_ports != ["127.0.0.1:8081:8081"]', stack)
        self.assertIn('"media-cache": ("8081", 8081)', stack)
        self.assertIn('{"3000", "8080", "8081", "9000"}', stack)
        self.assertNotIn('if "ports" in cdn:', stack)

    def test_caddy_is_recreated_without_enabling_its_admin_api(self):
        workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        remote_health = workflow.split("REMOTE_HEALTH_CHECK", 1)[1].split("REMOTE_HEALTH_CHECK", 1)[0]
        caddy = (ROOT / "deploy/self-hosted/Caddyfile").read_text()
        self.assertIn("admin off", caddy)
        self.assertIn("caddy validate --config /etc/caddy/Caddyfile", remote_health)
        self.assertIn("up -d --force-recreate caddy", remote_health)
        self.assertIn("127.0.0.1:8443/_testagram/edge-health", remote_health)
        self.assertNotIn("caddy reload", remote_health)
        apex = workflow.split("Publish the apex federation edge over HTTPS", 1)[1].split("Configure public CDN URL", 1)[0]
        self.assertNotIn('Host: testagram.site', apex)
        self.assertIn('/_testagram/edge-health', apex)

    def test_deployment_uses_reviewed_immutable_service_digests(self):
        self.assertIn('ghcr.io/trendyzima/testagram-feed@sha256:6db5714bf3c10d6fe5507904ad48cb52e25ddf8739f7e1bc243a01fac2bd0ed8', self.script)
        self.assertIn('ghcr.io/trendyzima/testagram-federation@sha256:5ee24cb38db3dc460fd9ac96f758fa917a0d3f84bdc9adb915f5bb6cce323f0e', self.script)


if __name__ == "__main__":
    unittest.main()
