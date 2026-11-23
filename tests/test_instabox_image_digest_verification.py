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
        self.assertIn("ghcr.io/trendyzima/testagram-edge@sha256:bb7e1b067223cb7140e8cf10ee86562d039b0370d9d5c5f8dc1bef22e5d00b14", self.script)
        self.assertNotIn("ghcr.io/trendyzima/testagram-cdn@", self.script)

    def test_no_container_inspection_reads_repo_digests(self):
        self.assertNotIn('docker inspect --format \'{{range .RepoDigests}}{{println .}}{{end}}\'', self.script)


if __name__ == "__main__":
    unittest.main()
