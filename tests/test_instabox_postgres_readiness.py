from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
DEPLOY_SCRIPT = ROOT / "scripts" / "start-instabox-deployment.sh"


class TestInstaBoxPostgresReadinessGate(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = DEPLOY_SCRIPT.read_text(encoding="utf-8")

    def test_postgres_readiness_is_bounded_authenticated_and_precedes_migrations(self):
        start = self.script.index(
            'sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" up -d postgres'
        )
        gate_start = self.script.index("postgres_ready=false", start)
        gate_success = self.script.index(
            'echo "PostgreSQL is accepting authenticated connections; continuing to migration preflight."',
            gate_start,
        )
        migration = self.script.index("apply-native-identity-migrations.sh", gate_success)
        gate = self.script[gate_start:gate_success]

        self.assertLess(start, gate_start)
        self.assertLess(gate_start, gate_success)
        self.assertLess(gate_success, migration)
        self.assertIn("for attempt in $(seq 1 60)", gate)
        self.assertIn(
            'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"',
            gate,
        )
        self.assertIn("sleep 2", gate)
        self.assertIn("PostgreSQL did not become ready within 120 seconds", gate)
        self.assertIn("ps postgres", gate)
        self.assertIn("logs --tail=120 postgres", gate)
        self.assertIn("exit 1", gate)

    def test_migrations_cannot_run_before_authenticated_postgres_probe(self):
        start = self.script.index(
            'sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" up -d postgres'
        )
        readiness = self.script.index("pg_isready", start)
        migration = self.script.index("apply-native-identity-migrations.sh", start)
        self.assertLess(start, readiness)
        self.assertLess(readiness, migration)
        self.assertIn("postgres_ready=true", self.script[readiness:migration])


if __name__ == "__main__":
    unittest.main()
