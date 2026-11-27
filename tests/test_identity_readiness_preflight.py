import importlib.util
import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "identity_readiness_preflight",
    ROOT / "scripts/identity-readiness-preflight.py",
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class IdentityReadinessPreflightTests(unittest.TestCase):
    def test_accepts_only_invalid_application_response_after_all_gates(self):
        ready, _ = MODULE.classify_response(
            400, {"ok": False, "error": {"code": "INVALID_REGISTRATION_APPLICATION"}}
        )
        self.assertTrue(ready)

    def test_reports_web_authn_and_admin_console_blocker(self):
        ready, message = MODULE.classify_response(
            503, {"error": {"code": "IDENTITY_REVIEW_MFA_UNAVAILABLE"}}
        )
        self.assertFalse(ready)
        self.assertIn("four independent staff credentials", message)
        self.assertIn("Admin Console", message)

    def test_reports_private_storage_blocker(self):
        ready, message = MODULE.classify_response(
            503, {"error": {"code": "IDENTITY_STORAGE_UNAVAILABLE"}}
        )
        self.assertFalse(ready)
        self.assertIn("private evidence storage", message)

    def test_reports_missing_policy_versions(self):
        ready, message = MODULE.classify_response(
            503, {"error": {"code": "REGISTRATION_POLICY_NOT_CONFIGURED"}}
        )
        self.assertFalse(ready)
        self.assertIn("TESTAGRAM_TERMS_VERSION", message)
        self.assertIn("TESTAGRAM_PRIVACY_VERSION", message)

    def test_does_not_treat_success_as_preflight_success(self):
        ready, _ = MODULE.classify_response(202, {"ok": True, "application_id": "unexpected"})
        self.assertFalse(ready)

    def test_rejects_canonical_production_hosts(self):
        for host in ("api.testagram.site", "testagram.site", "cdn.testagram.site"):
            with self.subTest(host=host):
                with self.assertRaises(ValueError):
                    MODULE.validate_staging_origin(f"https://{host}")

    def test_rejects_non_https_and_paths(self):
        for origin in ("http://staging-api.example", "https://staging-api.example/path"):
            with self.subTest(origin=origin):
                with self.assertRaises(ValueError):
                    MODULE.validate_staging_origin(origin)

    def test_accepts_clean_https_staging_origin(self):
        self.assertEqual(
            MODULE.validate_staging_origin("https://staging-api.example/"),
            "https://staging-api.example",
        )


if __name__ == "__main__":
    unittest.main()
