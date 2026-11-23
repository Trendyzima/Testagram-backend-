from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ROOT / "deploy" / "self-hosted" / "docker-compose.yml"
SCRIPT = ROOT / "deploy" / "self-hosted" / "apply-social-platform-product-migration.sh"
MIGRATION = ROOT / "migrations" / "20261120_social_platform_product_foundation.sql"
README = ROOT / "deploy" / "self-hosted" / "README.md"
TOPIC_CATALOG = ROOT / "migrations" / "064_knowledge_topic_seed_catalog.sql"


class TestSocialPlatformMigrationSafety(unittest.TestCase):
    def test_compose_mounts_the_reviewed_migration_for_new_volumes(self):
        compose = COMPOSE.read_text(encoding="utf-8")
        self.assertIn(
            "../../migrations/20261120_social_platform_product_foundation.sql:"
            "/docker-entrypoint-initdb.d/063_testagram_social_platform_product_foundation.sql:ro",
            compose,
        )
        self.assertIn(
            "../../migrations/064_knowledge_topic_seed_catalog.sql:"
            "/docker-entrypoint-initdb.d/064_knowledge_topic_seed_catalog.sql:ro",
            compose,
        )

    def test_operator_script_backs_up_and_verifies_before_confirmation_and_apply(self):
        script = SCRIPT.read_text(encoding="utf-8")
        backup = script.index("pg_dump --format=custom")
        archive_validation = script.index("pg_restore --list")
        checksum = script.index("sha256sum -c")
        confirmation = script.index("APPLY-TESTAGRAM-SOCIAL-PLATFORM-FOUNDATION")
        apply = script.index('psql -v ON_ERROR_STOP=1', confirmation)
        self.assertLess(backup, archive_validation)
        self.assertLess(archive_validation, checksum)
        self.assertLess(checksum, confirmation)
        self.assertLess(confirmation, apply)
        self.assertIn("schema_ready", script)
        self.assertIn("062_testagram_knowledge_engine.sql", script)
        self.assertIn("063_testagram_social_platform_product_foundation.sql", script)
        self.assertIn("064_knowledge_topic_seed_catalog.sql", script)
        self.assertIn("catalog_ready", script)
        self.assertIn("testagram_local.questions", script)
        self.assertIn("no migration applied", script)
        self.assertIn("social_moderation_appeals", script)

    def test_migration_is_additive_and_has_expected_product_tables(self):
        migration = MIGRATION.read_text(encoding="utf-8")
        for table in (
            "social_lists",
            "social_list_members",
            "social_list_posts",
            "post_drafts",
            "scheduled_posts",
            "stories",
            "social_reports",
            "social_moderation_actions",
            "social_moderation_appeals",
        ):
            self.assertIn(f"CREATE TABLE IF NOT EXISTS testagram_local.{table}", migration)
        self.assertNotRegex(migration, r"(?im)^\s*DROP\s+(TABLE|SCHEMA|DATABASE)")

    def test_topic_catalog_is_seeded_additively_and_replay_safely(self):
        migration = TOPIC_CATALOG.read_text(encoding="utf-8")
        expected = (
            "kenya", "east-africa", "technology", "software-engineering",
            "artificial-intelligence", "entrepreneurship", "business", "agriculture",
            "education", "science", "climate", "finance", "design", "photography",
            "sports", "music", "film", "travel", "history", "creative-writing",
        )
        for slug in expected:
            self.assertIn(f"('{slug}'", migration)
        self.assertIn("ON CONFLICT (slug) DO NOTHING", migration)
        self.assertNotRegex(migration, r"(?im)^\\s*DROP\\s+(TABLE|SCHEMA|DATABASE)")

    def test_operator_documentation_requires_the_backup_first_workflow(self):
        readme = README.read_text(encoding="utf-8")
        self.assertIn("bash ./apply-social-platform-product-migration.sh", readme)
        self.assertIn("migration 062 (Knowledge Engine)", readme)
        self.assertIn("migration 064 (curated topic catalog)", readme)
        self.assertIn("APPLY-TESTAGRAM-SOCIAL-PLATFORM-FOUNDATION", readme)


if __name__ == "__main__":
    unittest.main()
