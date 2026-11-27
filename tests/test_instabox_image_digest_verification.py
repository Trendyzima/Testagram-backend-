from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "start-instabox-deployment.sh"


class TestInstaboxImageDigestVerification(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = SCRIPT.read_text(encoding="utf-8")

    def test_flutter_container_resolves_its_image_before_reading_repo_digests(self):
        self.assertIn("web_actual_image_id=", self.script)
        self.assertIn('docker image inspect --format \'{{range .RepoDigests}}{{println .}}{{end}}\' "$web_actual_image_id"', self.script)

    def test_data_centre_and_cdn_containers_use_image_metadata(self):
        self.assertIn("actual_image_id=", self.script)
        self.assertIn('docker image inspect --format \'{{range .RepoDigests}}{{println .}}{{end}}\' "$actual_image_id"', self.script)
        self.assertIn("cdn_image_id=", self.script)
        self.assertIn('docker image inspect --format \'{{range .RepoDigests}}{{println .}}{{end}}\' "$cdn_image_id"', self.script)

    def test_cdn_uses_the_canonical_go_edge_package(self):
        self.assertIn('sync_repo https://github.com/Trendyzima/cdn-.git "$REPOS/cdn"', self.script)
        self.assertIn("CDN_RUNTIME_SHA=", self.script)
        self.assertIn('TESTAGRAM_CDN_IMAGE="$(resolve_cdn_image ghcr.io/trendyzima/testagram-edge)"', self.script)
        self.assertIn('org.opencontainers.image.revision', self.script)
        self.assertNotIn("ghcr.io/trendyzima/testagram-cdn@", self.script)

    def test_data_centre_media_and_video_images_are_resolved_from_source_revision(self):
        self.assertIn("DATA_CENTRE_RUNTIME_SHA=", self.script)
        self.assertIn('org.opencontainers.image.revision', self.script)
        self.assertIn("TESTAGRAM_MEDIA_IMAGE=\"$(resolve_data_centre_image ghcr.io/trendyzima/testagram-media)\"", self.script)
        self.assertIn("TESTAGRAM_VIDEO_IMAGE=\"$(resolve_data_centre_image ghcr.io/trendyzima/testagram-video)\"", self.script)
        self.assertIn("TESTAGRAM_VIDEO_WORKER_IMAGE=\"$(resolve_data_centre_image ghcr.io/trendyzima/testagram-video-worker)\"", self.script)

    def test_data_centre_runtime_images_are_verified_after_start(self):
        self.assertIn('verify_running_image video "$TESTAGRAM_VIDEO_IMAGE"', self.script)
        self.assertIn('verify_running_image video-worker "$TESTAGRAM_VIDEO_WORKER_IMAGE"', self.script)
        self.assertIn('verify_running_image media "$TESTAGRAM_MEDIA_IMAGE"', self.script)
        self.assertIn('pull algorithm federation-api video video-worker', self.script)

    def test_private_media_image_is_only_started_when_media_profile_is_enabled(self):
        self.assertIn('if [[ "$media_enabled" == true ]]; then', self.script)
        self.assertIn('pull media', self.script)
        self.assertIn('up -d --force-recreate media', self.script)

    def test_no_container_inspection_reads_repo_digests(self):
        self.assertNotIn('docker inspect --format \'{{range .RepoDigests}}{{println .}}{{end}}\'', self.script)


if __name__ == "__main__":
    unittest.main()
