#!/usr/bin/env python3
"""Regression tests for explicit Testagram media deployment approvals."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class MediaFeatureGateWiringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        cls.deployer = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
        cls.compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        cls.env_example = (ROOT / "deploy/self-hosted/.env.example").read_text()

    def test_r2_bucket_provisioning_uses_the_same_normalized_media_gates_as_remote_deploy(self):
        self.assertIn("if: steps.media-gates.outputs.media_enabled == 'true'", self.workflow)
        self.assertIn('DATA_CENTRE_MEDIA_ENABLED: ${{ vars.DATA_CENTRE_MEDIA_ENABLED || \'false\' }}', self.workflow)
        self.assertIn('FRESH_MEDIA_ENABLED: ${{ vars.FRESH_MEDIA_ENABLED || \'false\' }}', self.workflow)
        self.assertIn('normalize_flag "${DATA_CENTRE_MEDIA_ENABLED:-false}"', self.workflow)
        self.assertIn('normalize_flag "${FRESH_MEDIA_ENABLED:-false}"', self.workflow)


    def test_workflow_flag_normalizer_preserves_true_and_false(self):
        match = re.search(r"normalize_flag\(\) \{.*?\}", self.workflow)
        self.assertIsNotNone(match)
        helper = match.group(0)
        script = helper + "\n" + "\n".join([
            "printf '%s\\n' \"$(normalize_flag true)\"",
            "printf '%s\\n' \"$(normalize_flag false)\"",
        ]) + "\n"
        result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["true", "false"])

    def test_b2_identity_bucket_gate_uses_same_normalized_private_media_flags(self):
        start = self.workflow.index("Provision dedicated private B2 identity-evidence bucket")
        end = self.workflow.index("Start deployment on the machine", start)
        step = self.workflow[start:end]
        self.assertIn("if: steps.media-gates.outputs.media_enabled == 'true'", step)
        self.assertIn("DATA_CENTRE_MEDIA_ENABLED: ${{ vars.DATA_CENTRE_MEDIA_ENABLED || 'false' }}", step)
        self.assertIn("FRESH_MEDIA_ENABLED: ${{ vars.FRESH_MEDIA_ENABLED || 'false' }}", step)
        self.assertIn("normalize_flag", step)
        self.assertIn('normalize_flag "$DATA_CENTRE_MEDIA_ENABLED"', step)
        self.assertIn('normalize_flag "$FRESH_MEDIA_ENABLED"', step)

    def test_workflow_local_fresh_media_gate_name_does_not_shadow_step_environment(self):
        self.assertEqual(self.workflow.count('fresh_gate_enabled="$(normalize_flag'), 2)
        self.assertNotIn('fresh_media_enabled="$(normalize_flag', self.workflow)
        self.assertIn('Internal workflow error: R2 provisioning ran without both media gates.', self.workflow)
        self.assertIn('Internal workflow error: B2 provisioning ran without both media gates.', self.workflow)
        self.assertIn("FRESH_MEDIA_ENABLED: ${{ vars.FRESH_MEDIA_ENABLED || 'false' }}", self.workflow)

    def test_provider_native_private_access_controls_replace_unsupported_s3_policy_calls(self):
        self.assertNotIn("mc anonymous set none", self.workflow + self.deployer)
        self.assertIn("Verified R2 identity-evidence bucket has r2.dev disabled", self.workflow)
        self.assertIn("B2 identity-evidence bucket ACL is private", self.workflow)
        self.assertIn("provider-native privacy was verified by the deployment workflow", self.deployer)

    def test_pinned_minio_client_alias_commands_use_supported_flags(self):
        for source in (self.workflow, self.deployer):
            alias_lines = [line for line in source.splitlines() if "mc alias set " in line]
            self.assertTrue(alias_lines)
            for line in alias_lines:
                self.assertNotIn("--region", line)
                self.assertIn("--api S3v4", line)
                self.assertIn("--path auto", line)

    def test_production_evidence_bucket_names_are_dedicated_and_provisioned_private(self):
        for token in (
            "testagram-identity-evidence-production-b2",
            "testagram-identity-evidence-production-r2",
            'aws s3api put-bucket-acl --bucket "$B2_BUCKET" --acl private',
            'aws s3api get-bucket-acl --bucket "$B2_BUCKET"',
            '/domains/managed',
            '''body = json.dumps({"enabled": False}, separators=(",", ":")).encode("utf-8")''',
            '''request = urllib.request.Request(''',
            '''"Content-Type": "application/json"''',
            'managed_status="$(cat "$managed_status_file")"',
            'result.enabled == false',
            '/domains/custom',
            'mc stat "b2/$B2_BUCKET"',
            'mc stat "r2/$R2_IDENTITY_BUCKET"',
            "B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET:",
            "CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET:",
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.workflow)

    def test_workflow_keeps_legacy_replicator_off_and_preserves_data_centre_gates(self):
        self.assertIn("DATA_CENTRE_MEDIA_ENABLED: ${{ vars.DATA_CENTRE_MEDIA_ENABLED || 'false' }}", self.workflow)
        self.assertIn("FRESH_MEDIA_ENABLED: ${{ vars.FRESH_MEDIA_ENABLED || 'false' }}", self.workflow)
        self.assertIn("MEDIA_REPLICATION_ENABLED: 'false'", self.workflow)
        self.assertIn("The legacy Backend multi-bucket replicator is not the canonical media path.", self.workflow)

    def test_identity_registration_is_readiness_gated_without_a_legacy_kill_switch(self):
        self.assertIn("DATA_CENTRE_MEDIA_ENABLED: ${{ vars.DATA_CENTRE_MEDIA_ENABLED || 'false' }}", self.workflow)
        self.assertIn("FRESH_MEDIA_ENABLED: ${{ vars.FRESH_MEDIA_ENABLED || 'false' }}", self.workflow)
        self.assertNotIn("confirm_identity_registration", self.workflow)
        self.assertNotIn("TESTAGRAM_IDENTITY_REGISTRATION_ENABLED", self.workflow)
        self.assertNotIn("TESTAGRAM_IDENTITY_REGISTRATION_ENABLED", self.deployer)
        self.assertIn('normalize_boolean FRESH_MEDIA_ENABLED false', self.deployer)
        self.assertIn('normalize_boolean DATA_CENTRE_MEDIA_ENABLED false', self.deployer)
        self.assertIn("native self-service email-verified signup remains enabled.", self.deployer)
        self.assertIn("Private identity-evidence applications remain unavailable", self.deployer)
        self.assertIn("does not gate the", self.deployer)
        self.assertIn("independent native /api/auth/signup + email verification account flow.", self.deployer)
        self.assertNotIn("identity registration remains fail-closed", self.deployer)
        self.assertIn("Identity registration requires TESTAGRAM_TERMS_VERSION", self.deployer)
        self.assertIn("Identity registration requires TESTAGRAM_PRIVACY_VERSION", self.deployer)
        self.assertIn('write_env_value CLOUDFLARE_R2_METADATA_BUCKET "$DC_R2_METADATA_BUCKET"', self.deployer)
        self.assertIn('write_dc_env_value CLOUDFLARE_R2_METADATA_BUCKET "$DC_R2_METADATA_BUCKET"', self.deployer)
        self.assertIn('--arg r2_metadata_bucket "${CLOUDFLARE_R2_METADATA_BUCKET:-}"', self.deployer)
        self.assertIn("CLOUDFLARE_R2_METADATA_BUCKET:$r2_metadata_bucket", self.deployer)
        self.assertIn("TESTAGRAM_TERMS_VERSION: ${{ vars.TESTAGRAM_TERMS_VERSION || '2026-11-11' }}", self.workflow)
        self.assertIn("TESTAGRAM_PRIVACY_VERSION: ${{ vars.TESTAGRAM_PRIVACY_VERSION || '2026-11-11' }}", self.workflow)
        self.assertIn('ensure_random_secret IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN', self.deployer)
        self.assertIn('write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"', self.deployer)
        self.assertIn('write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"', self.deployer)
        self.assertIn('write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""', self.deployer)
        self.assertIn('write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""', self.deployer)

    def test_native_jwt_and_session_database_are_shared_with_data_centre_consumers(self):
        self.assertIn('write_dc_env_value TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('write_dc_env_value DATABASE_URL "$shared_database_url"', self.deployer)
        self.assertIn('verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" video TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" notification TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" video DATABASE_URL "$shared_database_url"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" notification DATABASE_URL "$shared_database_url"', self.deployer)

    def test_missing_identity_token_keeps_the_client_interface_nil(self):
        main = (ROOT / "cmd/api/main.go").read_text()
        self.assertIn("var identityEvidenceMedia identityEvidenceMediaClient", main)
        self.assertNotIn("var identityEvidenceMedia *datacentreclient.Client", main)

    def test_identity_evidence_token_is_passed_to_the_private_backend_service(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        self.assertIn("IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN: ${IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN:-}", compose)
    def test_media_profile_reuses_existing_r2_backup_credentials(self):
        self.assertIn("B2_S3_BUCKET: ${{ vars.B2_S3_PRIVATE_BUCKET || secrets.B2_S3_BUCKET }}", self.workflow)
        self.assertIn("B2_S3_PRIVATE_BUCKET: ${{ vars.B2_S3_PRIVATE_BUCKET || secrets.B2_S3_BUCKET }}", self.workflow)
        self.assertIn("CLOUDFLARE_R2_ENDPOINT: ${{ vars.CLOUDFLARE_R2_ENDPOINT || secrets.TESTAGRAM_BACKUP_S3_ENDPOINT }}", self.workflow)
        self.assertIn("CLOUDFLARE_R2_ACCESS_KEY_ID: ${{ secrets.CLOUDFLARE_R2_ACCESS_KEY_ID || secrets.TESTAGRAM_BACKUP_S3_ACCESS_KEY_ID }}", self.workflow)
        self.assertIn("CLOUDFLARE_R2_SECRET_ACCESS_KEY: ${{ secrets.CLOUDFLARE_R2_SECRET_ACCESS_KEY || secrets.TESTAGRAM_BACKUP_S3_SECRET_ACCESS_KEY }}", self.workflow)
        self.assertIn("CLOUDFLARE_R2_REGION: ${{ vars.CLOUDFLARE_R2_REGION || 'auto' }}", self.workflow)
        self.assertIn("CLOUDFLARE_R2_METADATA_BUCKET: ${{ steps.r2-media-buckets.outputs.metadata_bucket", self.workflow)
        self.assertIn("Ensure Data Centre R2 media and metadata buckets exist", self.workflow)
        self.assertIn("testagram-media-production-r2", self.workflow)
        self.assertIn("testagram-media-production-metadata", self.workflow)

    def test_data_centre_media_preserves_provider_credentials_and_initializes_metadata(self):
        for token in (
            'DC_B2_S3_ENDPOINT="${B2_S3_ENDPOINT:-}"',
            'DC_R2_ENDPOINT="${CLOUDFLARE_R2_ENDPOINT:-}"',
            'DC_R2_METADATA_BUCKET="${CLOUDFLARE_R2_METADATA_BUCKET:-}"',
            'export -n B2_S3_ENDPOINT',
            'MINIO_METADATA_ACCESS_KEY=testagram-metadata',
            'ensure_random_secret MINIO_METADATA_SECRET_KEY',
            'run --rm --no-deps object-init',
            'write_dc_env_value VPS_MINIO_METADATA_ACCESS_KEY_ID "$MINIO_METADATA_ACCESS_KEY"',
            'write_dc_env_value VPS_MINIO_METADATA_SECRET_ACCESS_KEY "$MINIO_METADATA_SECRET_KEY"',
            'write_dc_env_value CLOUDFLARE_R2_METADATA_BUCKET "$DC_R2_METADATA_BUCKET"',
            'verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media VPS_MINIO_METADATA_BUCKET',
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.deployer)

    def test_provider_smoke_secret_sources_and_retired_identity_smoke(self):
        for key in ("B2_S3_ENDPOINT", "B2_S3_REGION"):
            with self.subTest(key=key):
                self.assertIn(f"{key}: ${{{{ secrets.{key} }}}}", self.workflow)
                self.assertNotIn(f"{key}: ${{{{ vars.{key} }}}}", self.workflow)

        identity_smoke = (
            ROOT / ".github/workflows/identity-evidence-staging-smoke.yml"
        ).read_text()
        media_smoke = (
            ROOT / ".github/workflows/media-provider-smoke.yml"
        ).read_text()
        self.assertIn("Retired", identity_smoke)
        self.assertIn("Testagram-data-centre-/.github/workflows/media-provider-smoke.yml", identity_smoke)
        self.assertNotIn("IDENTITY_SMOKE_B2_ENDPOINT", identity_smoke)
        self.assertNotIn("IDENTITY_SMOKE_R2_ENDPOINT", identity_smoke)
        self.assertIn("B2_ENDPOINT: ${{ secrets.B2_S3_ENDPOINT }}", media_smoke)
        self.assertIn("B2_REGION: ${{ secrets.B2_S3_REGION }}", media_smoke)

    def test_deployer_normalizes_boolean_flags_before_fail_closed_validation(self):
        self.assertIn("normalize_boolean FRESH_MEDIA_ENABLED false", self.deployer)
        self.assertIn("normalize_boolean DATA_CENTRE_MEDIA_ENABLED false", self.deployer)
        self.assertIn("normalize_boolean MEDIA_REPLICATION_ENABLED false", self.deployer)
        self.assertIn("tr -d", self.deployer)
        self.assertIn("surrounding whitespace is ignored", self.deployer)
        self.assertIn("refusing an ambiguous media profile", self.deployer)

    def test_boolean_normalizer_accepts_whitespace_and_rejects_invalid_values(self):
        match = re.search(r"normalize_boolean\(\) \{.*?\n\}", self.deployer, re.S)
        self.assertIsNotNone(match)
        helper = match.group(0)
        accepted = helper + "\n" + "\n".join([
            "FRESH_MEDIA_ENABLED=$' \\ttrue \\r\\n'",
            "normalize_boolean FRESH_MEDIA_ENABLED false",
            "printf '%s\\n' \"$FRESH_MEDIA_ENABLED\"",
        ]) + "\n"
        result = subprocess.run(["bash", "-c", accepted], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "true")

        rejected = helper + "\n" + "\n".join([
            "FRESH_MEDIA_ENABLED=tru",
            "normalize_boolean FRESH_MEDIA_ENABLED false",
        ]) + "\n"
        result = subprocess.run(["bash", "-c", rejected], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)

    def test_env_writer_strips_crlf_from_single_line_values(self):
        match = re.search(r"write_env_value\(\) \{.*?\n\}", self.deployer, re.S)
        self.assertIsNotNone(match)
        script = match.group(0) + "\n" + "\n".join([
            'BASE_ENV="$(mktemp)"',
            'trap \'rm -f "$BASE_ENV"\' EXIT',
            "write_env_value TESTAGRAM_WEBAUTHN_RPID $'ops.testagram.site\\r\\n'",
            "if grep -q $'\\r' \"$BASE_ENV\"; then exit 1; fi",
            '[[ "$(cat "$BASE_ENV")" == "TESTAGRAM_WEBAUTHN_RPID=ops.testagram.site" ]]',
        ]) + "\n"
        result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_fresh_media_approval_reaches_remote_deployment(self):
        self.assertIn('--arg fresh_media_enabled "$FRESH_MEDIA_ENABLED"', self.deployer)
        self.assertIn("FRESH_MEDIA_ENABLED:$fresh_media_enabled", self.deployer)
        self.assertIn('write_env_value FRESH_MEDIA_ENABLED "$FRESH_MEDIA_ENABLED"', self.deployer)
        self.assertNotIn('write_env_value FRESH_MEDIA_ENABLED "$media_enabled"', self.deployer)


    def test_dedicated_identity_evidence_buckets_are_independent_of_legacy_replicator(self):
        for token in (
            'DC_B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET="${B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private-b2}"',
            'DC_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET="${CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private-r2}"',
            'if [[ "${DATA_CENTRE_MEDIA_ENABLED:-false}" == "true" && "${FRESH_MEDIA_ENABLED:-false}" == "true" ]]; then',
            'B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET',
            'CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET',
            'mc stat "b2/$B2_BUCKET"',
            'mc stat "r2/$R2_BUCKET"',
            'verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET',
            'verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET',
            'internal/v1/identity-evidence/readyz',
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.deployer)

    def test_optional_fingerprint_migration_is_mounted_for_remote_preflight(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        migration = "20261121_native_identity_intake_optional_fingerprint.sql"
        self.assertIn(migration + ":/docker-entrypoint-initdb.d/" + migration + ":ro", compose)
        self.assertIn("github.event_name == 'push'", self.workflow)
        self.assertIn("'APPLY-NATIVE-IDENTITY-MIGRATIONS'", self.workflow)

    def test_b2_s3_credentials_are_trimmed_before_provider_calls(self):
        self.assertIn("B2 S3 access key ID length after whitespace normalization", self.workflow)
        self.assertIn("tr -d '\\r\\n[:space:]'", self.workflow)
        self.assertIn("tr -d '\\r\\n[:space:]'", self.deployer)

    def test_backend_container_is_pinned_to_the_exact_built_image_id(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn("image: testagram-self-hosted-api:latest", compose)
        self.assertIn('docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" build api', self.deployer)
        self.assertIn("config --format json | jq -r '.services.api.image // empty'", self.deployer)
        self.assertIn('docker image inspect --format \'{{.Id}}\' "$api_image_ref"', self.deployer)
        self.assertIn('docker inspect --format \'{{.Image}}\' "$api_container"', self.deployer)
        self.assertIn('"$api_actual_image_id" != "$api_expected_image_id"', self.deployer)
        self.assertIn("Verified running Backend immutable image ID", self.deployer)
        self.assertNotIn('docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" images -q api', self.deployer)

    def test_production_deploy_requires_manual_dispatch_and_pull_requests_do_not(self):
        deploy_gate = self.workflow.split("  deploy:", 1)[1].split("    name:", 1)[0]
        self.assertIn("github.ref == 'refs/heads/main'", deploy_gate)
        self.assertIn("github.event_name == 'workflow_dispatch'", deploy_gate)
        self.assertIn("inputs.confirm_production_deploy == true", deploy_gate)
        self.assertNotIn("github.event_name == 'push'", deploy_gate)
        self.assertNotIn("github.event_name == 'pull_request'", deploy_gate)
        self.assertIn("environment: production", self.workflow)


if __name__ == "__main__":
    unittest.main()
