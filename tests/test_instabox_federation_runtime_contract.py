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

    def test_deployment_uses_reviewed_immutable_service_digests(self):
        self.assertIn('ghcr.io/trendyzima/testagram-feed@sha256:d02513cdf2baf90ea2db3c45107e7026ccf6c2e32ac98cb96dccb314c1525c6c', self.script)
        self.assertIn('ghcr.io/trendyzima/testagram-federation@sha256:a84cbc8d6e82048a5d20e21dadd1ec6a429ce9284420f02cb92c43dfa218a8b6', self.script)


if __name__ == "__main__":
    unittest.main()
