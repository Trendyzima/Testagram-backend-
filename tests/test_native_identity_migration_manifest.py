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
            "20261127_native_staff_lifecycle_executor.sql",
            "20261124_testagram_email_verification_otp.sql",
            "20261124_testagram_social_email_consent.sql",
            "20261125_testagram_mail_delivery_events.sql",
            "20261126_testagram_native_mail_feedback.sql",
            "20261127_identity_evidence_retention_audit.sql",
        ]
        manifest = SOURCE.split("for file in", 1)[1].split("; do", 1)[0]
        applied = set(re.findall(r"(?m)^apply_migration\s+(\S+)", SOURCE))
        for name in required:
            with self.subTest(migration=name):
                self.assertIn(name, manifest)
                self.assertIn(name, applied)

    def test_postgres_is_recreated_after_verified_backup_to_refresh_bind_mounted_migrations(self):
        recreate = 'docker compose --env-file "$env_file" -f "$compose_file" up -d --force-recreate postgres'
        self.assertIn(recreate, SOURCE)
        self.assertLess(SOURCE.index("Verified SHA-256 and completed a full restore test"), SOURCE.index(recreate))

    def test_email_otp_and_social_email_consent_migrations_are_mounted_and_verified(self):
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        otp = "20261124_testagram_email_verification_otp.sql"
        consent = "20261124_testagram_social_email_consent.sql"
        delivery = "20261125_testagram_mail_delivery_events.sql"
        feedback = "20261126_testagram_native_mail_feedback.sql"
        for migration in (otp, consent, delivery, feedback):
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
        delivery_sql = (ROOT / "migrations" / delivery).read_text(encoding="utf-8")
        self.assertIn("delivery_events", delivery_sql)
        self.assertIn("recipient_suppressions", delivery_sql)
        self.assertIn("social_unsubscribe_tokens", delivery_sql)
        self.assertIn("recipient_fingerprint", delivery_sql)
        mail_events = (ROOT / "cmd/api/mail_events.go").read_text(encoding="utf-8")
        self.assertNotIn("MAILJET_API_KEY", mail_events)
        self.assertNotIn("MAILJET_SECRET_KEY", mail_events)
        deploy_script = (ROOT / "scripts" / "start-instabox-deployment.sh").read_text(encoding="utf-8")
        self.assertIn('verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api TESTAGRAM_MAIL_ENCRYPTION_KEY "$TESTAGRAM_MAIL_ENCRYPTION_KEY"', deploy_script)
        deploy_workflow = (ROOT / ".github" / "workflows" / "instabox-deploy.yml").read_text(encoding="utf-8")
        self.assertIn("TESTAGRAM_EDGE_PROXY_SHARED_TOKEN", deploy_workflow)
        self.assertIn("TESTAGRAM_EDGE_PROXY_SHARED_TOKEN", compose)
        self.assertNotIn("reconcileMailjetEvents", mail_events)
        self.assertIn("LookupMX", (ROOT / "cmd/api/mail_smtp.go").read_text(encoding="utf-8"))
        self.assertIn("List-Unsubscribe-Post", (ROOT / "cmd/api/mail.go").read_text(encoding="utf-8"))
        self.assertIn("socialEmailUnsubscribe", (ROOT / "cmd/api/mail_unsubscribe.go").read_text(encoding="utf-8"))

    def test_identity_evidence_retention_audit_migration_is_mounted_and_applied(self):
        migration = "20261127_identity_evidence_retention_audit.sql"
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        sql = (ROOT / "migrations" / migration).read_text(encoding="utf-8")
        self.assertIn(
            f"../../migrations/{migration}:/docker-entrypoint-initdb.d/{migration}:ro",
            compose,
        )
        self.assertIn(f"apply_migration {migration}", SOURCE)
        self.assertIn(migration, SOURCE.split("for file in", 1)[1].split("; do", 1)[0])
        self.assertIn("identity_evidence_quarantined", sql)
        self.assertIn("identity_evidence_deleted", sql)
        self.assertIn("verification_events_append_only", (ROOT / "migrations" / "20261114_native_identity_integrity.sql").read_text(encoding="utf-8"))
        self.assertIn("identity evidence retention quarantine and physical deletion audit", SOURCE)

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

    def test_native_staff_lifecycle_executor_migration_is_mounted_and_applied(self):
        migration = "20261127_native_staff_lifecycle_executor.sql"
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn(
            f"../../migrations/{migration}:/docker-entrypoint-initdb.d/{migration}:ro",
            compose,
        )
        self.assertIn(f"apply_migration {migration}", SOURCE)
        sql = (ROOT / "migrations" / migration).read_text(encoding="utf-8")
        for expected in (
            "staff.invite",
            "staff.role_change",
            "staff.revoke",
            "staff.permission_change",
            "staff.webauthn.enroll",
            "native_staff_invitations",
            "token_sha256",
            "native_staff_lifecycle_events",
            "native_staff_lifecycle_events_append_only",
            "reject_native_staff_lifecycle_event_mutation",
        ):
            with self.subTest(expected=expected):
                self.assertIn(expected, sql)
        self.assertIn("token_sha256 BYTEA NOT NULL UNIQUE", sql)
        self.assertNotIn("token TEXT", sql)

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
            "20261125_testagram_mail_delivery_events.sql",
            "20261126_testagram_native_mail_feedback.sql",
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
        self.assertIn("networks: [edge, data, monitoring, platform, egress, video-private]", compose)
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
        self.assertIn("column_name='federation_enabled'", SOURCE)
        migration = (ROOT / "migrations" / "20261107_federation_actor_consent.sql").read_text(encoding="utf-8")
        self.assertIn("ADD COLUMN IF NOT EXISTS federation_enabled BOOLEAN NOT NULL DEFAULT FALSE", migration)

    def test_schema_preflight_checks_optional_intake_fingerprint(self):
        self.assertIn("column_name='evidence_fingerprint_sha256'", SOURCE)
        self.assertIn("is_nullable='YES'", SOURCE)
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
        self.assertIn("historical_external_sensitive_action_requests archived or absent", SOURCE)
        self.assertIn("historical_external_sensitive_action_approvals archived or absent", SOURCE)
        self.assertIn("historical_external_admin_security_audit archived or absent", SOURCE)

    def test_failed_schema_preflight_reports_named_acceptance_predicates(self):
        self.assertIn("schema_diagnostics()", SOURCE)
        self.assertIn("Failed schema acceptance predicates:", SOURCE)
        self.assertIn("legacy staff roles archived or absent", SOURCE)
        self.assertIn("legacy staff role audit archived or absent", SOURCE)
        self.assertIn("historical_external_sensitive_action_requests archived or absent", SOURCE)
        self.assertIn("historical_external_sensitive_action_approvals archived or absent", SOURCE)
        self.assertIn("historical_external_admin_security_audit archived or absent", SOURCE)
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


    def test_legacy_admin_migration_does_not_recreate_missing_staff_roles(self):
        migration = (ROOT / "migrations" / "20261111_admin_security_controls.sql").read_text(encoding="utf-8")
        self.assertIn("IF to_regclass('testagram_auth.staff_roles') IS NOT NULL", migration)
        self.assertIn("column_name='appwrite_user_id'", migration)
        self.assertIn("column_name='role'", migration)
        self.assertNotIn("CREATE TABLE IF NOT EXISTS testagram_auth.staff_roles", migration)
        self.assertIn("CREATE TABLE IF NOT EXISTS testagram_auth.admin_permissions", migration)
        self.assertIn("CREATE TABLE IF NOT EXISTS testagram_auth.admin_rate_limits", migration)
        for legacy_table in (
            "staff_role_permissions",
            "staff_permission_overrides",
            "sensitive_action_requests",
            "sensitive_action_approvals",
            "admin_security_audit",
        ):
            self.assertIn("CREATE TABLE IF NOT EXISTS testagram_auth." + legacy_table, migration)

    def test_optional_legacy_archive_migrations_accept_absent_tables(self):
        staff = (ROOT / "migrations" / "20261120_native_identity_historical_staff_archive.sql").read_text(encoding="utf-8")
        admin = (ROOT / "migrations" / "20261120_native_identity_historical_admin_archive.sql").read_text(encoding="utf-8")
        for source in (staff, admin):
            self.assertIn("to_regclass(", source)
            self.assertIn("lock_optional_historical_", source)
            self.assertNotRegex(source, r"'testagram_auth\.historical_external_[^']+'::regclass")
            self.assertIn("REVOKE ALL ON TABLE %s FROM PUBLIC", source)

    def test_legacy_identity_migrations_replay_after_provider_ledgers_are_archived(self):
        pipeline = (ROOT / "migrations" / "20261111_identity_registration_pipeline.sql").read_text(encoding="utf-8")
        hardening = (ROOT / "migrations" / "20261113_identity_registration_hardening.sql").read_text(encoding="utf-8")
        integrity = (ROOT / "migrations" / "20261114_native_identity_integrity.sql").read_text(encoding="utf-8")
        evidence_audit = (ROOT / "migrations" / "20261115_identity_evidence_access_audit.sql").read_text(encoding="utf-8")
        native_admin = (ROOT / "migrations" / "20261118_native_admin_console.sql").read_text(encoding="utf-8")
        native_ledger = (ROOT / "migrations" / "20261119_native_identity_approval_ledger.sql").read_text(encoding="utf-8")
        admin_archive = (ROOT / "migrations" / "20261120_native_identity_historical_admin_archive.sql").read_text(encoding="utf-8")
        attribution_archive = (ROOT / "migrations" / "20261120_native_identity_historical_attribution_archive.sql").read_text(encoding="utf-8")
        integration = (ROOT / "scripts" / "test-identity-registration-migration.sh").read_text(encoding="utf-8")

        self.assertIn("historical_external_registration_provisioning", pipeline)
        self.assertIn("to_regclass('testagram_identity.registration_provisioning') IS NOT NULL", pipeline)
        self.assertIn("historical_external_sensitive_action_request_id", hardening)
        self.assertIn("to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL", hardening)
        self.assertIn("to_regclass('testagram_auth.staff_roles') IS NOT NULL", hardening)
        self.assertIn("to_regclass('testagram_identity.registration_provisioning') IS NOT NULL", hardening)
        self.assertIn("DROP TRIGGER IF EXISTS identity_approval_token_requires_two_person_approval", hardening)
        self.assertIn("to_regclass('testagram_auth.staff_roles') IS NOT NULL", integrity)
        self.assertIn("reviewer_appwrite_user_id", integrity)
        self.assertIn("'evidence_access_attempted','account_provisioned','email_verified'", evidence_audit)
        self.assertIn("to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL", native_ledger)
        self.assertIn("to_regclass('testagram_auth.admin_security_audit') IS NOT NULL", native_ledger)
        self.assertIn("reviewer_appwrite_user_id", native_admin)
        self.assertIn("DO $legacy_reviewer_columns$", native_admin)
        self.assertIn("DROP TRIGGER IF EXISTS identity_approval_token_requires_two_person_approval", admin_archive)

        replay_marker = integration.index("# Match the complete production runner manifest")
        replay_commands = (
            'migrations/20261111_identity_registration_pipeline.sql',
            'migrations/20261113_identity_registration_hardening.sql',
            'migrations/20261114_native_identity_integrity.sql',
            'migrations/20261118_native_admin_console.sql',
            'migrations/20261119_native_identity_approval_ledger.sql',
            'migrations/20261120_native_identity_historical_admin_archive.sql',
            'migrations/20261120_native_identity_historical_attribution_archive.sql',
            'migrations/20261120_native_identity_approval_circuit.sql',
        )
        positions = [integration.index(command, replay_marker) for command in replay_commands]
        self.assertEqual(positions, sorted(positions))
        self.assertIn("DO $retirement_acceptance$", integration)

    def test_schema_ready_reuses_validated_named_diagnostics(self):
        self.assertIn("failed_checks=\"$(schema_diagnostics)\"", SOURCE)
        self.assertIn("psql -X -v ON_ERROR_STOP=1", SOURCE)
        self.assertNotIn("psql -v ON_ERROR_STOP=1 -U", SOURCE.split("schema_ready() {", 1)[1].split("\n}", 1)[0])
        self.assertIn("Failed schema acceptance predicates:", SOURCE)


if __name__ == "__main__":
    unittest.main()
