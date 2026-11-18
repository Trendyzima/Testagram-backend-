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

    def test_federation_identity_uses_the_canonical_apex(self):
        self.assertIn('write_dc_env_value FEDERATION_PUBLIC_BASE_URL "https://testagram.site"', self.script)
        self.assertIn('write_dc_env_value FEDERATION_INSTANCE_DOMAIN "testagram.site"', self.script)
        self.assertNotIn('FEDERATION_PUBLIC_BASE_URL=https://federation.testagram.site', self.script)

    def test_deployment_uses_currently_published_algorithm_and_federation_releases(self):
        self.assertIn('testagram-feed:sha-da8e90e79150636a6efe8e03c2cbc8fb9357764d', self.script)
        self.assertIn('testagram-federation:sha-1a8cb7895caa7e27f9bdb3f1a2bb68ef755634b4', self.script)
        self.assertNotIn('testagram-feed:sha-6ae0532c171383b84731063d590d775524f9da1e', self.script)
        self.assertNotIn('testagram-federation:sha-edf4f59f843514596d35273c888ff1c16ec0f73e', self.script)


if __name__ == "__main__":
    unittest.main()
