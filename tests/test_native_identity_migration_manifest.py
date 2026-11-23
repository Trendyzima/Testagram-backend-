#!/usr/bin/env python3
"""Guard the native identity migration manifest against schema drift."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "deploy/self-hosted/apply-native-identity-migrations.sh"
SOURCE = SCRIPT.read_text(encoding="utf-8")


class NativeIdentityMigrationManifestTests(unittest.TestCase):
    def test_legacy_identity_import_utility_is_retired(self):
        retired_provider = "".join(chr(value) for value in (65, 112, 112, 119, 114, 105, 116, 101))
        legacy_import = ROOT / "scripts" / f"migrate-{retired_provider.lower()}-identities.sql"
        self.assertFalse(
            legacy_import.exists(),
            "legacy identity import must not provision accounts outside the native two-person approval workflow",
        )

    def test_every_required_migration_is_preflighted_and_applied(self):
        required = [
            "029_federation_actor_consent.sql",
            "034_admin_security_controls.sql",
            "035_identity_registration_pipeline.sql",
            "039_identity_registration_hardening.sql",
            "041_native_identity_verification.sql",
            "042_native_identity_evidence.sql",
            "043_native_identity_integrity.sql",
            "044_identity_evidence_access_audit.sql",
            "045_native_identity_status_access.sql",
            "049_native_auth_cutover_foundation.sql",
            "050_native_approved_identity_provisioning.sql",
            "051_native_auth_runtime_grants.sql",
            "053_native_staff_reviewer_grants.sql",
            "054_native_admin_console.sql",
            "055_native_identity_approval_ledger.sql",
            "056_native_verification_decider.sql",
            "057_testagram_mail_outbox.sql",
            "058_testagram_mail_outbox_runtime_role.sql",
            "058a_native_identity_historical_account_archive.sql",
            "058b_native_identity_historical_staff_archive.sql",
            "058c_native_identity_historical_admin_archive.sql",
            "058d_native_identity_historical_attribution_archive.sql",
            "059_native_identity_approval_circuit.sql",
            "20261121_native_identity_intake_optional_fingerprint.sql",
            "060_native_staff_webauthn.sql",
            "061_native_sensitive_action_integrity.sql",
            "20261124_testagram_email_verification_otp.sql",
            "20261124_testagram_social_email_consent.sql",
        ]
        manifest = SOURCE.split("for file in", 1)[1].split("; do", 1)[0]
        applied = set(re.findall(r"(?m)^apply_migration\s+(\S+)", SOURCE))
        for name in required:
            with self.subTest(migration=name):
                self.assertIn(name, manifest)
                self.assertIn(name, applied)

    def test_email_otp_and_social_email_consent_migrations_are_mounted_and_verified(self):
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        otp = "20261124_testagram_email_verification_otp.sql"
        consent = "20261124_testagram_social_email_consent.sql"
        for migration in (otp, consent):
            with self.subTest(migration=migration):
                self.assertIn(
                    f"../../migrations/{migration}:/docker-entrypoint-initdb.d/{migration}:ro",
                    compose,
                )
                self.assertIn(f"apply_migration {migration}", SOURCE)
                self.assertIn(migration, SOURCE.split("for file in", 1)[1].split("; do", 1)[0])
        self.assertIn("verification_otp_hash", SOURCE)
        self.assertIn("verification_otp_expires_at", SOURCE)
        self.assertIn("verification_otp_attempts", SOURCE)
        self.assertIn("email_notification_preferences", SOURCE)
        self.assertIn("has_table_privilege", SOURCE)
        consent_sql = (ROOT / "migrations" / consent).read_text(encoding="utf-8")
        self.assertIn("preference_user_id", consent_sql)
        self.assertIn("preference_category", consent_sql)
        self.assertIn("CONSENT_WITHDRAWN", (ROOT / "cmd/api/social_email_notifications.go").read_text(encoding="utf-8"))

    def test_sensitive_action_integrity_migration_is_mounted_and_applied(self):
        migration = "061_native_sensitive_action_integrity.sql"
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn(
            f"../../migrations/{migration}:/docker-entrypoint-initdb.d/{migration}:ro",
            compose,
        )
        self.assertIn(f"apply_migration {migration}", SOURCE)
        sql = (ROOT / "migrations" / migration).read_text(encoding="utf-8")
        self.assertIn("native_sensitive_action_request_immutable", sql)
        self.assertIn("native_sensitive_action_approver_guard", sql)
        self.assertIn("native_sensitive_action_approval_append_only", sql)
        self.assertIn("native_sensitive_action_request_append_only", sql)
        self.assertIn("native_sensitive_action_request_append_only", SOURCE)
        self.assertIn("native sensitive-action approval decisions are append-only", sql)
        self.assertIn("native sensitive-action request status transition is invalid or terminal", sql)
        self.assertIn("completed approval decisions require a decision timestamp", sql)
        self.assertIn("NEW.payload IS DISTINCT FROM OLD.payload", sql)
        self.assertIn("NEW.payload_sha256 IS DISTINCT FROM OLD.payload_sha256", sql)
        self.assertIn("v_status<>'pending'", sql)
        self.assertIn("v_expires_at<=now()", sql)
        self.assertIn("requester cannot approve their own sensitive action", sql)
        self.assertIn("sp.enabled=TRUE", sql)
        self.assertIn("sp.mfa_enrolled_at IS NOT NULL", sql)
        self.assertIn("wc.revoked_at IS NULL", sql)
        self.assertIn("wc.user_verified=TRUE", sql)
        self.assertIn("wc.clone_warning=FALSE", sql)
        self.assertIn("native sensitive-action approval quorum is incomplete", sql)
        self.assertIn("FOR UPDATE OF sp,i,rg,wc", sql)
        self.assertIn("webauthn_rpid", sql)
        self.assertIn("webauthn_credential_id", sql)
        self.assertIn("wc.rpid=a.webauthn_rpid", sql)
        self.assertIn("wc.credential_id=a.webauthn_credential_id", sql)
        self.assertIn("wc.credential_id=NEW.webauthn_credential_id", sql)
        self.assertIn("approval must identify the exact WebAuthn credential used for step-up", sql)
        self.assertIn("expired sensitive-action request cannot be approved", sql)
        self.assertIn("approval request is missing, expired or no longer pending", sql)
        self.assertIn("v_eligible_approved_count", sql)
        self.assertIn("OLD.required_approvals", sql)
        self.assertIn("NEW.status='approved'", sql)
        self.assertIn("native sensitive-action requests are append-only", sql)
        self.assertIn("native_sensitive_action_request_append_only", sql)
        self.assertIn("native sensitive-action rejection decision is missing", sql)

    def test_archive_migrations_are_mounted_and_applied_in_order(self):
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        apply_script = SOURCE
        migrations = (
            ("20261120_native_identity_historical_account_archive.sql", "058a_native_identity_historical_account_archive.sql"),
            ("20261120_native_identity_historical_staff_archive.sql", "058b_native_identity_historical_staff_archive.sql"),
            ("20261120_native_identity_historical_admin_archive.sql", "058c_native_identity_historical_admin_archive.sql"),
            ("20261120_native_identity_historical_attribution_archive.sql", "058d_native_identity_historical_attribution_archive.sql"),
        )
        positions = []
        for source, mounted_name in migrations:
            with self.subTest(migration=source):
                self.assertIn(f"../../migrations/{source}:/docker-entrypoint-initdb.d/{mounted_name}:ro", compose)
                self.assertIn(f"apply_migration {mounted_name}", apply_script)
                positions.append(apply_script.index(f"apply_migration {mounted_name}"))
        self.assertEqual(positions, sorted(positions))

    def test_historical_external_identity_data_is_renamed_and_runtime_locked(self):
        migration = (ROOT / "migrations" / "20261120_native_identity_historical_account_archive.sql").read_text(encoding="utf-8")
        self.assertIn("RENAME TO historical_external_identity_accounts", migration)
        self.assertIn("RENAME TO historical_external_identity_link_audit", migration)
        self.assertIn("RENAME TO historical_external_registration_consents", migration)
        self.assertIn("RENAME TO historical_external_registration_provisioning", migration)
        self.assertIn("RENAME COLUMN appwrite_user_id TO source_account_id", migration)
        self.assertIn("historical_external_identity_accounts", migration)
        self.assertIn("ILIKE '%appwrite%'", migration)
        self.assertIn("source_provider_created", migration)
        self.assertIn("native_registration_provisioning", migration)
        self.assertIn("REVOKE ALL ON TABLE", migration)
        self.assertIn("historical_external_provider", migration)
        self.assertNotIn("DROP TABLE", migration)

    def test_historical_staff_roles_cannot_authorize_native_staff(self):
        migration = (ROOT / "migrations" / "20261120_native_identity_historical_staff_archive.sql").read_text(encoding="utf-8")
        self.assertIn("RENAME TO historical_external_staff_roles", migration)
        self.assertIn("RENAME TO historical_external_staff_role_audit", migration)
        self.assertIn("RENAME COLUMN appwrite_user_id TO source_staff_id", migration)
        self.assertIn("staff_roles_active_role_idx", migration)
        self.assertIn("ILIKE '%appwrite%'", migration)
        self.assertIn("historical_external_staff_roles_active_role_idx", migration)
        self.assertIn("REVOKE ALL ON TABLE", migration)
        self.assertIn("never authorize native Testagram staff access", migration)
        self.assertNotIn("DROP TABLE", migration)

    def test_legacy_admin_ledgers_are_archived_outside_native_console(self):
        migration = (ROOT / "migrations" / "20261120_native_identity_historical_admin_archive.sql").read_text(encoding="utf-8")
        self.assertIn("RENAME TO historical_external_sensitive_action_requests", migration)
        self.assertIn("RENAME TO historical_external_sensitive_action_approvals", migration)
        self.assertIn("RENAME TO historical_external_admin_security_audit", migration)
        self.assertIn("RENAME TO historical_external_staff_permission_overrides", migration)
        self.assertIn("RENAME COLUMN requested_by_appwrite_user_id TO source_requester_staff_id", migration)
        self.assertIn("ILIKE '%appwrite%'", migration)
        self.assertIn("REVOKE ALL ON TABLE", migration)
        self.assertNotIn("DROP TABLE", migration)

    def test_historical_attribution_columns_are_neutralized_without_losing_values(self):
        migration = (ROOT / "migrations" / "20261120_native_identity_historical_attribution_archive.sql").read_text(encoding="utf-8")
        self.assertIn("RENAME COLUMN reviewer_appwrite_user_id TO historical_source_reviewer_id", migration)
        self.assertIn("RENAME COLUMN decided_by_appwrite_user_id TO historical_source_decider_id", migration)
        self.assertIn("RENAME COLUMN consumed_by_appwrite_user_id TO historical_consumed_by_source_id", migration)
        self.assertIn("RENAME COLUMN sensitive_action_request_id TO historical_external_sensitive_action_request_id", migration)
        self.assertIn("identity_applications_historical_request_fk", migration)
        self.assertIn("historical_external_identity_applications_sensitive_request_idx", migration)
        self.assertIn("historical_source_verification_reviews_reviewer_idx", migration)
        self.assertIn("identity_application_reviews_historical_source_reviewer_fk", migration)
        self.assertIn("verification_reviews_historical_source_reviewer_fk", migration)
        self.assertIn("verification_sessions_historical_source_decider_fk", migration)
        self.assertIn("identity_approval_tokens_historical_first_approver_fk", migration)
        self.assertIn("identity_approval_tokens_historical_second_approver_fk", migration)
        self.assertIn("first_approver_user_id", migration)
        self.assertIn("second_approver_user_id", migration)
        self.assertNotIn("DROP COLUMN", migration)
        self.assertNotIn("UPDATE testagram_identity", migration)

    def test_identity_integration_executes_legacy_retirement_and_native_acceptance(self):
        script = (ROOT / "scripts" / "test-identity-registration-migration.sh").read_text(encoding="utf-8")
        for migration in (
            "20261106_appwrite_identity_bridge.sql",
            "20261106_appwrite_identity_link_audit.sql",
            "20261114_appwrite_registration_consent.sql",
            "20261120_native_identity_historical_account_archive.sql",
            "20261120_native_identity_historical_staff_archive.sql",
            "20261120_native_identity_historical_admin_archive.sql",
            "20261120_native_identity_historical_attribution_archive.sql",
            "20261124_testagram_email_verification_otp.sql",
            "20261124_testagram_social_email_consent.sql",
        ):
            with self.subTest(migration=migration):
                self.assertIn(migration, script)
        self.assertIn("provider-keyed identity or staff tables remain in the live schema", script)
        self.assertIn("runtime role can still read archived historical identity or staff data", script)
        self.assertIn("runtime role lost required native identity or approval access", script)
        self.assertIn("SET status='appwrite_created'", script)
        self.assertIn("status='source_provider_created'", script)
        self.assertIn("CREATE DATABASE native_clean_archive_test", script)
        self.assertIn("clean install unexpectedly created optional historical provider tables", script)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_account_archive.sql"), 2)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_staff_archive.sql"), 2)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_admin_archive.sql"), 2)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_attribution_archive.sql"), 2)

    def test_original_provider_migrations_are_history_only(self):
        # Keep applied migration files immutable, but never mount or execute
        # them in a fresh-install or production upgrade manifest.
        legacy_migrations = (
            "20261106_appwrite_identity_bridge.sql",
            "20261106_appwrite_identity_link_audit.sql",
            "20261114_appwrite_registration_consent.sql",
        )
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        production_runner = SOURCE
        migration_test = (ROOT / "scripts" / "test-identity-registration-migration.sh").read_text(encoding="utf-8")

        for migration in legacy_migrations:
            with self.subTest(migration=migration):
                self.assertTrue((ROOT / "migrations" / migration).is_file(), "applied migration history must remain immutable")
                self.assertNotIn(migration, compose, "fresh-install Compose must not mount retired provider migrations")
                self.assertNotIn(migration, production_runner, "production upgrade runner must not execute retired provider migrations")
                self.assertIn(migration, migration_test, "isolated CI fixture must continue testing upgrades from historical schemas")

    def test_provider_archive_privilege_changes_are_table_scoped(self):
        archive_names = (
            "20261120_native_identity_historical_account_archive.sql",
            "20261120_native_identity_historical_staff_archive.sql",
            "20261120_native_identity_historical_admin_archive.sql",
            "20261120_native_identity_historical_attribution_archive.sql",
        )
        for name in archive_names:
            migration = (ROOT / "migrations" / name).read_text(encoding="utf-8")
            with self.subTest(migration=name):
                self.assertNotRegex(migration, r"(?i)\bREVOKE\s+ALL\s+ON\s+SCHEMA\b")
                self.assertNotRegex(migration, r"(?i)\bREVOKE\s+ALL\s+ON\s+ALL\s+TABLES\s+IN\s+SCHEMA\b")
                self.assertNotRegex(migration, r"(?i)\bDROP\s+SCHEMA\b")
                self.assertNotRegex(migration, r"(?i)\bDROP\s+TABLE\b")
                revocations = re.findall(r"(?is)\bREVOKE\b.*?;", migration)
                for statement in revocations:
                    self.assertRegex(statement, r"(?is)\bREVOKE\s+ALL\s+ON\s+TABLE\b")

        manifest = SOURCE.split("for file in", 1)[1].split("; do", 1)[0]
        ordered = (
            "058a_native_identity_historical_account_archive.sql",
            "058b_native_identity_historical_staff_archive.sql",
            "058c_native_identity_historical_admin_archive.sql",
            "058d_native_identity_historical_attribution_archive.sql",
            "059_native_identity_approval_circuit.sql",
        )
        positions = [manifest.index(name) for name in ordered]
        self.assertEqual(positions, sorted(positions))

    def test_backend_admin_console_readiness_is_explicitly_wired_privately(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text(encoding="utf-8")
        env_example = (ROOT / "deploy/self-hosted/.env.example").read_text(encoding="utf-8")
        self.assertIn(
            "TESTAGRAM_ADMIN_CONSOLE_READINESS_URL: ${TESTAGRAM_ADMIN_CONSOLE_READINESS_URL:-http://admin-console:8090/internal/webauthn/readiness}",
            compose,
        )
        self.assertGreaterEqual(
            compose.count("TESTAGRAM_WEBAUTHN_READINESS_TOKEN: ${TESTAGRAM_WEBAUTHN_READINESS_TOKEN:-}"),
            2,
        )
        self.assertIn("networks: [edge, data, monitoring, platform, egress]", compose)
        self.assertIn("networks: [edge, monitoring, platform]", compose)
        self.assertIn(
            "TESTAGRAM_ADMIN_CONSOLE_READINESS_URL=http://admin-console:8090/internal/webauthn/readiness",
            env_example,
        )

    def test_corrective_mail_role_migration_is_mounted_and_applied(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text(encoding="utf-8")
        migration = "20261123_testagram_mail_outbox_runtime_role.sql"
        self.assertIn(migration + ":/docker-entrypoint-initdb.d/058_testagram_mail_outbox_runtime_role.sql:ro", compose)
        self.assertIn("CREATE ROLE testagram_app NOLOGIN", (ROOT / "migrations" / migration).read_text(encoding="utf-8"))
        self.assertIn("DROP ROLE testagram_app", (ROOT / "scripts/test-identity-registration-migration.sh").read_text(encoding="utf-8"))

    def test_federation_actor_consent_is_preflighted_and_fail_closed(self):
        self.assertIn("Federation actor consent column", SOURCE)
        self.assertIn("column_name='\\''federation_enabled'\\''", SOURCE)
        migration = (ROOT / "migrations" / "20261107_federation_actor_consent.sql").read_text(encoding="utf-8")
        self.assertIn("ADD COLUMN IF NOT EXISTS federation_enabled BOOLEAN NOT NULL DEFAULT FALSE", migration)

    def test_schema_preflight_checks_optional_intake_fingerprint(self):
        self.assertIn("column_name='\\''evidence_fingerprint_sha256'\\''", SOURCE)
        self.assertIn("is_nullable='\\''YES'\\''", SOURCE)
        self.assertIn("identity_approval_token_requires_verified_evidence", SOURCE)

    def test_schema_preflight_matches_archived_external_and_native_approval_schema(self):
        for expected in (
            "historical_external_sensitive_action_requests",
            "historical_external_sensitive_action_approvals",
            "historical_external_admin_security_audit",
            "native_sensitive_action_approvals",
            "native_admin_security_audit",
            "native sensitive action approver_user_id",
            "native admin audit actor_user_id",
            "historical sensitive-action requests inaccessible to runtime",
            "historical admin audit inaccessible to runtime",
        ):
            with self.subTest(expected=expected):
                self.assertIn(expected, SOURCE)
        self.assertNotIn("to_regclass('testagram_auth.sensitive_action_requests')", SOURCE)
        self.assertNotIn("to_regclass('testagram_auth.sensitive_action_approvals')", SOURCE)
        self.assertNotIn("to_regclass('testagram_auth.admin_security_audit')", SOURCE)

    def test_failed_schema_preflight_reports_named_acceptance_predicates(self):
        self.assertIn("schema_diagnostics()", SOURCE)
        self.assertIn("Failed schema acceptance predicates:", SOURCE)
        self.assertIn("identity application evidence fingerprint nullable", SOURCE)
        self.assertIn("identity evidence-complete approval trigger", SOURCE)
        self.assertIn("two-person identity approval trigger", SOURCE)
        self.assertIn("testagram_app mail outbox UPDATE", SOURCE)
        self.assertIn("staff WebAuthn users table", SOURCE)
        self.assertIn("staff WebAuthn credentials table", SOURCE)
        self.assertIn("staff WebAuthn challenges table", SOURCE)

    def test_migration_requires_explicit_authorization_and_verified_restore(self):
        self.assertIn("APPLY-NATIVE-IDENTITY-MIGRATIONS", SOURCE)
        self.assertIn("pg_restore --exit-on-error", SOURCE)
        self.assertIn("schema_ready", SOURCE)


if __name__ == "__main__":
    unittest.main()
