#!/usr/bin/env python3
"""Guard sensitive-action migration prerequisites in every explicit runner."""

from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MIGRATIONS = ROOT / "migrations"
TARGET = "061_native_sensitive_action_integrity.sql"
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
        ("20261121_native_staff_webauthn.sql", "060_native_staff_webauthn.sql"),
    )
    for source, alias in aliases:
        mount = f"../../migrations/{source}:/docker-entrypoint-initdb.d/{alias}:ro"
        assert mount in compose, f"missing prerequisite mount: {mount}"
        assert f"apply_migration {alias}" in runner, f"runner omits prerequisite: {alias}"

    assert f"../../migrations/{TARGET}:/docker-entrypoint-initdb.d/{TARGET}:ro" in compose
    assert f"apply_migration {TARGET}" in runner
    assert runner.index("apply_migration 060_native_staff_webauthn.sql") < runner.index(
        f"apply_migration {TARGET}"
    ), "WebAuthn credentials must exist before sensitive-action integrity triggers are installed"

    # The isolated PostgreSQL integration uses canonical migration filenames
    # and must apply the same dependency chain before 061.
    admin_job = workflow.split("admin-security-schema-integration:", 1)[1]
    ordered = (
        "migrations/20261019_testagram_independent_auth.sql",
        "migrations/20261115_testagram_native_auth_cutover_foundation.sql",
        "migrations/20261118_native_admin_console.sql",
        "migrations/20261121_native_staff_webauthn.sql",
        f"migrations/{TARGET}",
    )
    positions = [admin_job.index(item) for item in ordered]
    assert positions == sorted(positions), "Backend PostgreSQL integration migration order regressed"

    for dependency in (
        "testagram_auth.native_sensitive_action_approvals",
        "testagram_auth.native_sensitive_action_requests",
        "testagram_auth.staff_webauthn_credentials",
    ):
        assert dependency in sql, f"expected dependency reference missing: {dependency}"
    assert "BEGIN;" in sql and "COMMIT;" in sql, "migration must remain transactional"
    print("Native sensitive-action migration dependency order: PASS")


if __name__ == "__main__":
    main()
