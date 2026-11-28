#!/usr/bin/env python3
"""Guard sensitive-action migration prerequisites in every explicit runner."""

from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MIGRATIONS = ROOT / "migrations"
TARGET = "061_native_sensitive_action_integrity.sql"
WEBAUTHN_DUAL_CONTROL = "20261128_native_staff_webauthn_dual_control.sql"
APPROVER_REVALIDATION = "20261128_native_staff_webauthn_approver_permission_revalidation.sql"
STAFF_LIFECYCLE = "20261127_native_staff_lifecycle_executor.sql"
PREREQUISITES = (
    "20261019_testagram_independent_auth.sql",
    "20261115_testagram_native_auth_cutover_foundation.sql",
    "20261118_native_admin_console.sql",
    "20261121_native_staff_webauthn.sql",
)


def main() -> None:
    target = MIGRATIONS / TARGET
    assert target.is_file(), f"applied migration history must remain immutable: {TARGET}"
    assert not (MIGRATIONS / "20261126_native_sensitive_action_integrity.sql").exists(), (
        "do not fork or rename an already-referenced migration to repair runner ordering"
    )
    for prerequisite in PREREQUISITES:
        assert (MIGRATIONS / prerequisite).is_file(), f"missing prerequisite migration: {prerequisite}"

    compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
    runner = (ROOT / "deploy/self-hosted/apply-native-identity-migrations.sh").read_text()
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    sql = target.read_text()

    # Fresh VPS install uses stable numbered aliases. Verify the source files
    # are mounted to the expected aliases and the ordered runner applies them
    # before the immutable 061 migration.
    aliases = (
        ("20261115_testagram_native_auth_cutover_foundation.sql", "049_native_auth_cutover_foundation.sql"),
        ("20261118_native_admin_console.sql", "054_native_admin_console.sql"),
        ("20261120_native_verification_decider.sql", "056_native_verification_decider.sql"),
        ("20261120_native_identity_historical_attribution_archive.sql", "058d_native_identity_historical_attribution_archive.sql"),
        ("20261121_native_staff_webauthn.sql", "060_native_staff_webauthn.sql"),
    )
    for source, alias in aliases:
        mount = f"../../migrations/{source}:/docker-entrypoint-initdb.d/{alias}:ro"
        assert mount in compose, f"missing prerequisite mount: {mount}"
        assert f"apply_migration {alias}" in runner, f"runner omits prerequisite: {alias}"

    assert f"../../migrations/{TARGET}:/docker-entrypoint-initdb.d/{TARGET}:ro" in compose
    assert f"apply_migration {TARGET}" in runner
    assert f"../../migrations/{WEBAUTHN_DUAL_CONTROL}:/docker-entrypoint-initdb.d/{WEBAUTHN_DUAL_CONTROL}:ro" in compose
    assert f"../../migrations/{APPROVER_REVALIDATION}:/docker-entrypoint-initdb.d/{APPROVER_REVALIDATION}:ro" in compose
    migration_list = runner[runner.index("for file in"):runner.index("apply_migration() {")]
    assert WEBAUTHN_DUAL_CONTROL in migration_list, "runner omits dual-control enrollment migration"
    assert migration_list.index(TARGET) < migration_list.index(STAFF_LIFECYCLE) < migration_list.index(
        WEBAUTHN_DUAL_CONTROL
    ), "dual-control enrollment schema must follow the sensitive-action ledger and staff lifecycle tables"
    assert APPROVER_REVALIDATION in migration_list
    assert runner.index(f"apply_migration {STAFF_LIFECYCLE}") < runner.index(
        f"apply_migration {WEBAUTHN_DUAL_CONTROL}"
    ) < runner.index(f"apply_migration {APPROVER_REVALIDATION}"), (
        "staff lifecycle, dual-control and permission revalidation migrations must be applied in order"
    )
    assert runner.index("apply_migration 060_native_staff_webauthn.sql") < runner.index(
        f"apply_migration {TARGET}"
    ), "WebAuthn credentials must exist before sensitive-action integrity triggers are installed"
    assert runner.index("apply_migration 056_native_verification_decider.sql") < runner.index(
        "apply_migration 058d_native_identity_historical_attribution_archive.sql"
    ), "native verification decider UUID must exist before historical attribution is archived"

    # The isolated PostgreSQL integration uses canonical migration filenames
    # and must apply the same dependency chain before 061.
    admin_job = workflow.split("admin-security-schema-integration:", 1)[1]
    ordered = (
        "migrations/20261019_testagram_independent_auth.sql",
        "migrations/20261115_testagram_native_auth_cutover_foundation.sql",
        "migrations/20261118_native_admin_console.sql",
        "migrations/20261121_native_staff_webauthn.sql",
        f"migrations/{TARGET}",
        f"migrations/{STAFF_LIFECYCLE}",
        f"migrations/{WEBAUTHN_DUAL_CONTROL}",
        f"migrations/{APPROVER_REVALIDATION}",
    )
    positions = [admin_job.index(item) for item in ordered]
    assert positions == sorted(positions), "Backend PostgreSQL integration migration order regressed"

    for dependency in (
        "testagram_auth.native_sensitive_action_approvals",
        "testagram_auth.native_sensitive_action_requests",
        "testagram_auth.staff_webauthn_credentials",
    ):
        assert dependency in sql, f"expected dependency reference missing: {dependency}"

    enrollment_sql = (MIGRATIONS / WEBAUTHN_DUAL_CONTROL).read_text()
    for invariant in (
        "first_approver_user_id<>second_approver_user_id",
        "staff_webauthn_enrollment_authorization_is_valid",
        "native_sensitive_action_approvals",
        "consumed_at IS NULL AND revoked_at IS NULL",
    ):
        assert invariant in enrollment_sql, f"missing WebAuthn dual-control invariant: {invariant}"
    assert "BEGIN;" in sql and "COMMIT;" in sql, "migration must remain transactional"
    permission_sql = (MIGRATIONS / APPROVER_REVALIDATION).read_text()
    for invariant in (
        "native_staff_permission_overrides",
        "staff_role_permissions",
        "staff.webauthn.enroll.approve",
        "owner_role.role='owner'",
        "override.granted=TRUE",
    ):
        assert invariant in permission_sql, f"missing live approver permission revalidation: {invariant}"
    assert "BEGIN;" in permission_sql and "COMMIT;" in permission_sql, (
        "approver permission revalidation migration must remain transactional"
    )
    assert "BEGIN;" in enrollment_sql and "COMMIT;" in enrollment_sql, (
        "WebAuthn dual-control migration must remain transactional"
    )
    print("Native sensitive-action migration dependency order: PASS")


if __name__ == "__main__":
    main()
