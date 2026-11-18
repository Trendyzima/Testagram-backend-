#!/usr/bin/env python3
"""Validate object-store inventories and media checksums for full backups."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path, PurePosixPath


def read_inventory(path: Path, bucket: str | None = None) -> dict[str, int]:
    entries: dict[str, int] = {}
    if not path.exists():
        raise ValueError(f"inventory is missing: {path}")
    for line_number, raw in enumerate(path.read_text().splitlines(), 1):
        if not raw.strip():
            continue
        try:
            item = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ValueError(f"invalid object-store inventory JSON at {path}:{line_number}") from exc
        if item.get("status") == "error":
            raise ValueError(f"object-store listing reported an error at {path}:{line_number}")
        if item.get("type") not in ("file", "object") and not ("key" in item and "size" in item):
            continue
        key = item.get("key") or item.get("name")
        size = item.get("size")
        if not isinstance(key, str) or not key or not isinstance(size, int) or size < 0:
            raise ValueError(f"object-store inventory entry is incomplete at {path}:{line_number}")
        key = key.lstrip("/")
        if bucket and key.startswith(bucket + "/"):
            key = key[len(bucket) + 1 :]
        pure = PurePosixPath(key)
        if pure.is_absolute() or not pure.parts or any(part in ("", ".", "..") for part in pure.parts) or "\\" in key:
            raise ValueError(f"unsafe object key in inventory at {path}:{line_number}")
        if key in entries:
            raise ValueError(f"duplicate object key in inventory: {key}")
        entries[key] = size
    return entries


def local_inventory(root: Path) -> dict[str, int]:
    if not root.is_dir():
        raise ValueError(f"local mirror directory is missing: {root}")
    entries: dict[str, int] = {}
    for path in root.rglob("*"):
        if path.is_symlink():
            raise ValueError(f"symlink found in object backup: {path}")
        if path.is_file():
            key = path.relative_to(root).as_posix()
            entries[key] = path.stat().st_size
    return entries


def verify_mirror(inventory: Path, root: Path, bucket: str | None = None) -> tuple[int, int]:
    expected = read_inventory(inventory, bucket)
    actual = local_inventory(root)
    missing = sorted(set(expected) - set(actual))
    extra = sorted(set(actual) - set(expected))
    changed = sorted(key for key in set(expected) & set(actual) if expected[key] != actual[key])
    if missing or extra or changed:
        details = []
        if missing:
            details.append(f"missing={len(missing)}")
        if extra:
            details.append(f"extra={len(extra)}")
        if changed:
            details.append(f"size_mismatch={len(changed)}")
        raise ValueError("object-store mirror does not match source inventory: " + ", ".join(details))
    return len(expected), sum(expected.values())


def verify_media_objects(checksums: Path, b2_root: Path, r2_root: Path) -> int:
    verified = 0
    for line_number, raw in enumerate(checksums.read_text().splitlines(), 1):
        if not raw:
            continue
        fields = raw.split("\t")
        if len(fields) != 5:
            raise ValueError(f"invalid media object checksum row at line {line_number}")
        namespace, key, size_raw, sha256, status = fields
        if status == "deleted":
            continue
        if status != "protected":
            raise ValueError(f"media object {key!r} is not in the protected state ({status})")
        if not re.fullmatch(r"[0-9a-fA-F]{64}", sha256):
            raise ValueError(f"media object {key!r} has no valid SHA-256")
        if not size_raw.isdigit() or int(size_raw) <= 0:
            raise ValueError(f"media object {key!r} has an invalid size")
        pure = PurePosixPath(key)
        if pure.is_absolute() or not pure.parts or any(part in ("", ".", "..") for part in pure.parts) or "\\" in key:
            raise ValueError(f"media object {key!r} has an unsafe key")
        for provider, root in (("B2", b2_root), ("R2", r2_root)):
            target = root.joinpath(*pure.parts)
            if not target.is_file() or target.is_symlink():
                raise ValueError(f"{provider} backup is missing media object {key!r}")
            if target.stat().st_size != int(size_raw):
                raise ValueError(f"{provider} media object {key!r} has a size mismatch")
            digest = hashlib.sha256()
            with target.open("rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
            if digest.hexdigest().lower() != sha256.lower():
                raise ValueError(f"{provider} media object {key!r} failed SHA-256 verification")
        verified += 1
    return verified


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    stats = sub.add_parser("stats")
    stats.add_argument("--inventory", type=Path, required=True)
    stats.add_argument("--bucket")
    mirror = sub.add_parser("verify-mirror")
    mirror.add_argument("--inventory", type=Path, required=True)
    mirror.add_argument("--root", type=Path, required=True)
    mirror.add_argument("--bucket")
    media = sub.add_parser("verify-media")
    media.add_argument("--checksums", type=Path, required=True)
    media.add_argument("--b2", type=Path, required=True)
    media.add_argument("--r2", type=Path, required=True)
    compare = sub.add_parser("compare-inventories")
    compare.add_argument("--expected", type=Path, required=True)
    compare.add_argument("--actual", type=Path, required=True)
    compare.add_argument("--bucket")
    args = parser.parse_args()
    try:
        if args.command == "stats":
            entries = read_inventory(args.inventory, args.bucket)
            print(json.dumps({"count": len(entries), "bytes": sum(entries.values())}))
        elif args.command == "verify-mirror":
            count, size = verify_mirror(args.inventory, args.root, args.bucket)
            print(json.dumps({"count": count, "bytes": size, "verified": True}))
        elif args.command == "verify-media":
            print(json.dumps({"verified_objects": verify_media_objects(args.checksums, args.b2, args.r2)}))
        else:
            expected = read_inventory(args.expected, args.bucket)
            actual = read_inventory(args.actual, args.bucket)
            if expected != actual:
                raise ValueError("restored isolated object store does not match the backup inventory")
            print(json.dumps({"count": len(expected), "bytes": sum(expected.values()), "verified": True}))
    except (OSError, ValueError) as exc:
        print(f"media backup validation error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
