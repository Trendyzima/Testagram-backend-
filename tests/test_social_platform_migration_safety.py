from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ROOT / "deploy" / "self-hosted" / "docker-compose.yml"
SCRIPT = ROOT / "deploy" / "self-hosted" / "apply-social-platform-product-migration.sh"
MIGRATION = ROOT / "migrations" / "20261120_social_platform_product_foundation.sql"
README = ROOT / "deploy" / "self-hosted" / "README.md"


class TestSocialPlatformMigrationSafety(unittest.TestCase):
    def test_compose_mounts_the_reviewed_migration_for_new_volumes(self):
        compose = COMPOSE.read_text(encoding="utf-8")
        self.assertIn(
            "../../migrations/20261120_social_platform_product_foundation.sql:"
            "/docker-entrypoint-initdb.d/063_testagram_social_platform_product_foundation.sql:ro",
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

    def test_operator_documentation_requires_the_backup_first_workflow(self):
        readme = README.read_text(encoding="utf-8")
        self.assertIn("./apply-social-platform-product-migration.sh", readme)
        self.assertIn("APPLY-TESTAGRAM-SOCIAL-PLATFORM-FOUNDATION", readme)


if __name__ == "__main__":
    unittest.main()
