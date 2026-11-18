from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
WORKFLOW = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()


class ImmutableSevenRepositoryDeploymentTests(unittest.TestCase):
    def test_repo_sync_requires_full_commit_sha(self):
        self.assertIn('[[ ! "$ref" =~ ^[0-9a-f]{40}$ ]]', SCRIPT)
        self.assertIn('git -C "$path" fetch --depth 1 origin "$ref"', SCRIPT)
        self.assertIn('actual="$(git -C "$path" rev-parse HEAD)"', SCRIPT)
        self.assertIn('if [[ "$actual" != "$ref" ]]', SCRIPT)
        self.assertNotIn("fetch --depth 1 origin main", SCRIPT)
        self.assertNotIn("--branch main", SCRIPT)

    def test_all_six_runtime_sources_and_control_plane_are_pinned(self):
        for name in (
            "TESTAGRAM_BACKEND_REF",
            "TESTAGRAM_API_CONTRACT_REF",
            "TESTAGRAM_DATA_CENTRE_REF",
            "TESTAGRAM_ALGORITHM_REF",
            "TESTAGRAM_FEDERATION_REF",
            "TESTAGRAM_FLUTTER_REF",
            "TESTAGRAM_CDN_REF",
        ):
            self.assertIn(name, SCRIPT)
        self.assertIn('sync_repo https://github.com/Trendyzima/testagram-api.git "$REPOS/api-contracts"', SCRIPT)
        self.assertIn("Runtime source refs do not match the immutable Testagram API release lock.", SCRIPT)
        self.assertIn("refs/heads/main", SCRIPT)

    def test_registry_images_are_resolved_by_commit_and_oci_revision(self):
        for image in (
            "ghcr.io/trendyzima/testagram-feed:sha-$TESTAGRAM_ALGORITHM_REF",
            "ghcr.io/trendyzima/testagram-federation:sha-$TESTAGRAM_FEDERATION_REF",
            "ghcr.io/trendyzima/testagram-web:sha-$TESTAGRAM_FLUTTER_REF",
        ):
            self.assertIn(image, SCRIPT)
        self.assertIn('org.opencontainers.image.revision', SCRIPT)
        self.assertNotIn("testagram-web:main", SCRIPT)
        self.assertNotIn("testagram-feed:sha-6ae0532c171383b84731063d590d775524f9da1e", SCRIPT)
        self.assertNotIn("testagram-federation:sha-edf4f59f843514596d35273c888ff1c16ec0f73e", SCRIPT)

    def test_cdn_digest_is_checked_against_locked_source_code(self):
        self.assertIn("TESTAGRAM_CDN_IMAGE_SOURCE_REF", SCRIPT)
        self.assertIn('git -C "$REPOS/cdn" diff --quiet "$TESTAGRAM_CDN_IMAGE_SOURCE_REF" "$TESTAGRAM_CDN_REF"', SCRIPT)
        self.assertIn("TESTAGRAM_CDN_IMAGE", SCRIPT)

    def test_release_manifest_records_all_components_without_credentials(self):
        self.assertIn('> "$ROOT/release-manifest.json"', SCRIPT)
        for field in (
            "apiContractRef",
            "backendRef",
            "dataCentreRef",
            "algorithmRef",
            "federationRef",
            "flutterRef",
            "cdnRef",
            "cdnImageSourceRef",
        ):
            self.assertIn(field, SCRIPT)
        self.assertIn('environment:"production"', SCRIPT)

    def test_dispatch_requires_explicit_contract_and_source_revisions(self):
        for input_name in (
            "testagram_api_contract_ref",
            "data_centre_ref",
            "algorithm_ref",
            "federation_ref",
            "flutter_ref",
            "cdn_ref",
        ):
            self.assertIn(f"      {input_name}:", WORKFLOW)
        self.assertIn("TESTAGRAM_API_CONTRACT_REF: ${{ inputs.testagram_api_contract_ref }}", WORKFLOW)


if __name__ == "__main__":
    unittest.main()
