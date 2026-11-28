#!/usr/bin/env python3
"""Fail CI if a retired hosted email provider or SMTP relay returns to active project files."""
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
POLICY = "docs/architecture/native-replacement-ownership.md"
NEGATIVE_TEST = "tests/test_native_mail_provider_policy.py"
HISTORICAL_MIGRATION = "migrations/20261126_testagram_native_mail_feedback.sql"
LEGACY_CLEANUP = "scripts/start-instabox-deployment.sh"

# Match provider spelling even when separators/case are varied, without
# making this guard itself an allowed source of the forbidden identifier.
PROVIDER = re.compile(r"mail[\\s._-]*jet|api[.]mailjet[.]com", re.IGNORECASE)
RETIRED_CONFIG = re.compile(
    r"\\b(?:MAILJET_API_KEY|MAILJET_SECRET_KEY|SMTP_HOST|SMTP_USERNAME|SMTP_PASSWORD|"
    r"CLOUDFLARE_EMAIL_ACCOUNT_ID|CLOUDFLARE_EMAIL_API_TOKEN|AUTH_FROM_EMAIL)\\b"
)

def tracked_files():
    result = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, check=True, capture_output=True
    )
    return [ROOT / item.decode() for item in result.stdout.split(b"\\0") if item]

def main():
    violations = []
    for path in tracked_files():
        relative = path.relative_to(ROOT).as_posix()
        if relative in {POLICY, NEGATIVE_TEST, HISTORICAL_MIGRATION}:
            continue
        try:
            content = path.read_text(encoding="utf-8")
        except (UnicodeDecodeError, OSError):
            continue
        if relative == LEGACY_CLEANUP:
            # This one-way cleanup only removes old values from an existing
            # VPS .env. It cannot configure or activate a retired transport.
            content = "\\n".join(
                line for line in content.splitlines()
                if "sed -i '/^MAILJET_/d; /^AUTH_FROM_EMAIL=/d'" not in line
            )
        if PROVIDER.search(content) or RETIRED_CONFIG.search(content):
            violations.append(relative)
    if violations:
        print("Forbidden email provider/relay configuration detected:", file=sys.stderr)
        for item in violations:
            print(f" - {item}", file=sys.stderr)
        return 1
    print("Native mail policy guard passed: no retired provider or relay configuration.")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
