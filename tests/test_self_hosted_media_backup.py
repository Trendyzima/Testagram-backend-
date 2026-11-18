from pathlib import Path
import json
import sys
import tempfile
import unittest
import hashlib

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import self_hosted_media_backup as media_backup


class SelfHostedMediaBackupTests(unittest.TestCase):
    def test_inventory_and_local_mirror_match_exact_keys_and_sizes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            inventory = root / "inventory.jsonl"
            inventory.write_text(
                json.dumps({"type": "file", "key": "users/a/one.jpg", "size": 3}) + "\n"
                + json.dumps({"type": "file", "key": "users/b/two.jpg", "size": 5}) + "\n"
            )
            mirror = root / "mirror"
            (mirror / "users/a").mkdir(parents=True)
            (mirror / "users/b").mkdir(parents=True)
            (mirror / "users/a/one.jpg").write_bytes(b"one")
            (mirror / "users/b/two.jpg").write_bytes(b"12345")
            self.assertEqual(media_backup.verify_mirror(inventory, mirror), (2, 8))

    def test_inventory_rejects_traversal_and_duplicate_keys(self):
        with tempfile.TemporaryDirectory() as temp:
            inventory = Path(temp) / "inventory.jsonl"
            inventory.write_text(json.dumps({"type": "file", "key": "../escape", "size": 1}))
            with self.assertRaises(ValueError):
                media_backup.read_inventory(inventory)
            inventory.write_text(
                json.dumps({"type": "file", "key": "same", "size": 1}) + "\n"
                + json.dumps({"type": "file", "key": "same", "size": 1})
            )
            with self.assertRaises(ValueError):
                media_backup.read_inventory(inventory)

    def test_media_object_hashes_must_match_both_provider_copies(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            b2 = root / "b2"
            r2 = root / "r2"
            (b2 / "users/a").mkdir(parents=True)
            (r2 / "users/a").mkdir(parents=True)
            payload = b"testagram-media-object"
            (b2 / "users/a/object.bin").write_bytes(payload)
            (r2 / "users/a/object.bin").write_bytes(payload)
            digest = hashlib.sha256(payload).hexdigest()
            inventory = root / "media-objects.tsv"
            inventory.write_text(f"media-private\tusers/a/object.bin\t{len(payload)}\t{digest}\tprotected\n")
            self.assertEqual(media_backup.verify_media_objects(inventory, b2, r2), 1)
            (r2 / "users/a/object.bin").write_bytes(b"corrupt")
            with self.assertRaises(ValueError):
                media_backup.verify_media_objects(inventory, b2, r2)

    def test_deleted_objects_are_not_required_in_provider_buckets(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            inventory = root / "media-objects.tsv"
            inventory.write_text("media-private\tusers/a/deleted.bin\t12\t\tdeleted\n")
            self.assertEqual(media_backup.verify_media_objects(inventory, root / "b2", root / "r2"), 0)


if __name__ == "__main__":
    unittest.main()
