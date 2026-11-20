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
