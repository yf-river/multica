#!/usr/bin/env python3
"""Validate the complete committed catalog without contacting upstream sites."""

import hashlib
import json
import pathlib
import re

from PIL import Image

root = pathlib.Path(__file__).resolve().parents[2]
catalog = json.loads((root / "server/internal/cooking/catalog.json").read_text())
report = json.loads((root / "scripts/cooking/inventory.json").read_text())
lock = json.loads((root / "scripts/cooking/sources.json").read_text())
assert report["sources"] == lock, "source manifest mismatch"
version = hashlib.sha256(json.dumps(report, sort_keys=True).encode()).hexdigest()[:16]
assert catalog["version"] == version, "catalog version mismatch"
assert json.loads((root / "apps/web/public/cooking/version.json").read_text()) == {
    "version": version,
    "sources": lock,
}, "published content version mismatch"
for replacement in lock.get("image_replacements", {}).values():
    data = (root / replacement["path"]).read_bytes()
    assert hashlib.sha256(data).hexdigest() == replacement["sha256"], (
        "replacement checksum mismatch"
    )
entries = catalog["entries"]
for source, replacement in lock.get("cover_replacements", {}).items():
    data = (root / replacement["path"]).read_bytes()
    assert replacement["source"] == source
    assert hashlib.sha256(data).hexdigest() == replacement["sha256"], (
        "cover replacement checksum mismatch"
    )
    entry = next(e for e in entries if e["source_path"] == source)
    assert entry["cover"] and "![" in entry["markdown"], source
ids = {e["id"] for e in entries}
assert len(ids) == len(entries), "duplicate IDs"
assert len(report["mapping"]) == report["illustrations"], "unaccounted illustrations"
assert sum(len(e["illustrations"]) for e in entries) == report["illustrations"]
for e in entries:
    assert e["markdown"] or e["illustrations"], e["id"]
    assert not re.search(r"!\[[^\]]*\]\(https?://", e["markdown"]), e["source_path"]
    for path in [
        e["cover"],
        *e["illustrations"],
        *re.findall(r"\]\((/cooking/[^)]+)\)", e["markdown"]),
    ]:
        if path:
            assert (root / "apps/web/public" / path.lstrip("/")).is_file(), path
for key, sha in report["assets"].items():
    p = root / "apps/web/public/cooking" / key
    data = p.read_bytes()
    assert hashlib.sha256(data).hexdigest() == sha, key
    assert not data.startswith(b"version https://git-lfs"), key
    if p.suffix == ".webp":
        with Image.open(p) as image:
            image.verify()
for link in report["links"]:
    assert link["from"] in ids and link["to"] in ids, link
assert report["text_recipes"] == sum(
    e["kind"] == "recipe" and bool(e["markdown"]) for e in entries
)
assert report["text_tips"] == sum(
    e["kind"] == "tip" and bool(e["markdown"]) for e in entries
)
print(
    json.dumps(
        {
            k: report[k]
            for k in [
                "text_recipes",
                "text_tips",
                "illustrations",
                "matched",
                "unavailable_images",
            ]
        },
        ensure_ascii=False,
    )
)
print("PASS: all catalog entries, local links, image payloads and hashes validated")
