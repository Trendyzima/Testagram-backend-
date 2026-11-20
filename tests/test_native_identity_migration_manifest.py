#!/usr/bin/env python3
"""Guard the native identity migration manifest against schema drift."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "deploy/self-hosted/apply-native-identity-migrations.sh"
SOURCE = SCRIPT.read_text(encoding="utf-8")


class NativeIdentityMigrationManifestTests(unittest.TestCase):
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
        ]
        manifest = SOURCE.split("for file in", 1)[1].split("; do", 1)[0]
        applied = set(re.findall(r"(?m)^apply_migration\s+(\S+)", SOURCE))
        for name in required:
            with self.subTest(migration=name):
                self.assertIn(name, manifest)
                self.assertIn(name, applied)

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
        self.assertIn("identity_applications_historical_external_sensitive_action_request_fk", migration)
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
        ):
            with self.subTest(migration=migration):
                self.assertIn(migration, script)
        self.assertIn("provider-keyed identity or staff tables remain in the live schema", script)
        self.assertIn("runtime role can still read archived historical identity or staff data", script)
        self.assertIn("runtime role lost required native identity or approval access", script)
        self.assertIn("SET status='appwrite_created'", script)
        self.assertIn("status='source_provider_created'", script)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_account_archive.sql"), 2)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_staff_archive.sql"), 2)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_admin_archive.sql"), 2)
        self.assertGreaterEqual(script.count("psql_ci < migrations/20261120_native_identity_historical_attribution_archive.sql"), 2)

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
