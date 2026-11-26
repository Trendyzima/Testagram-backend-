#!/usr/bin/env python3
"""Guard the native sensitive-action migration's hard schema dependencies."""

from pathlib import Path

MIGRATIONS = Path(__file__).resolve().parents[1] / "migrations"
TARGET = "20261126_native_sensitive_action_integrity.sql"
PREREQUISITES = (
    "20261019_testagram_independent_auth.sql",
    "20261115_testagram_native_auth_cutover_foundation.sql",
    "20261118_native_admin_console.sql",
    "20261121_native_staff_webauthn.sql",
)


def main() -> None:
    names = sorted(path.name for path in MIGRATIONS.glob("*.sql"))
    assert TARGET in names, f"missing ordered migration: {TARGET}"
    assert "061_native_sensitive_action_integrity.sql" not in names, (
        "the legacy numeric prefix sorts before its native-auth dependencies"
    )
    target_index = names.index(TARGET)
    for prerequisite in PREREQUISITES:
        assert prerequisite in names, f"missing prerequisite migration: {prerequisite}"
        assert names.index(prerequisite) < target_index, (
            f"{prerequisite} must run before {TARGET}"
        )

    sql = (MIGRATIONS / TARGET).read_text()
    for dependency in (
        "testagram_auth.native_sensitive_action_approvals",
        "testagram_auth.native_sensitive_action_requests",
        "testagram_auth.staff_webauthn_credentials",
    ):
        assert dependency in sql, f"expected dependency reference missing: {dependency}"
    assert sql.lstrip().startswith("--"), "migration must retain its explanatory header"
    assert "BEGIN;" in sql and "COMMIT;" in sql, "migration must remain transactional"
    print("Native sensitive-action migration dependency order: PASS")


if __name__ == "__main__":
    main()
