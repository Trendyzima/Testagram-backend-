import importlib.util
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "video-e2e-staging-acceptance.py"
SPEC = importlib.util.spec_from_file_location("video_staging_acceptance", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class GitHubOutputContractTests(unittest.TestCase):
    def test_outputs_are_three_distinct_newline_delimited_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "github-output"
            MODULE.write_github_outputs(str(output), "video-123", "Staging title", "short-lived-token")
            self.assertEqual(
                output.read_text(encoding="utf-8").splitlines(),
                [
                    "video_id=video-123",
                    "video_title=Staging title",
                    "access_token=short-lived-token",
                ],
            )

    def test_multiline_values_are_rejected_to_prevent_output_injection(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "github-output"
            with self.assertRaises(RuntimeError):
                MODULE.write_github_outputs(str(output), "video-123", "safe\nmalicious=value", "token")
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
