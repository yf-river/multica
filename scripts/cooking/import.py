#!/usr/bin/env python3
"""Import pinned HowToCook content and same-origin assets. Requires Pillow."""

import argparse
import concurrent.futures
import hashlib
import io
import json
import pathlib
import re
import subprocess
import time
import urllib.parse
import urllib.request

from PIL import Image, ImageOps

ROOT = pathlib.Path(__file__).resolve().parents[2]
LOCK = pathlib.Path(__file__).with_name("sources.json")
CATEGORIES = {
    "vegetable_dish": "素菜",
    "meat_dish": "荤菜",
    "aquatic": "水产",
    "breakfast": "早餐",
    "staple": "主食",
    "soup": "汤粥",
    "condiment": "酱料和其他材料",
    "dessert": "甜品",
    "drink": "饮料",
    "semi-finished": "半成品加工",
    "tips": "基础技巧",
}


def digest(b):
    return hashlib.sha256(b).hexdigest()


def read_json(p):
    return json.loads(p.read_text())


def save_json(p, v):
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(v, ensure_ascii=False, indent=2) + "\n")


def git_sha(p):
    return subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=p, text=True
    ).strip()


def write_asset(data, out, assets, cover=False):
    if data.startswith(b"version https://git-lfs"):
        raise ValueError("Git LFS pointer is not an image")
    sha = digest(data)
    key = sha + ("-cover" if cover else "") + ".webp"
    target = out / key
    if target.exists():
        try:
            with Image.open(target) as existing:
                existing.verify()
        except Exception:
            target.unlink()
    if not target.exists():
        im = ImageOps.exif_transpose(Image.open(io.BytesIO(data))).convert("RGB")
        if cover:
            # Crop the upper photographic area; never shrink an entire infographic onto a card.
            im = ImageOps.fit(im, (640, 400), centering=(0.5, 0))
        else:
            im.thumbnail((2400, 10000))
        im.save(target, "WEBP", quality=88, method=6)
    assets[key] = digest(target.read_bytes())
    return "/cooking/" + key


def replacement_data(lock, url, source):
    replacement = lock.get("image_replacements", {}).get(url)
    if replacement is None:
        return None
    if replacement["source"] != source:
        raise ValueError(f"Image replacement source mismatch: {source}")
    path = (ROOT / replacement["path"]).resolve()
    if not path.is_relative_to(ROOT.resolve()):
        raise ValueError("Image replacement outside repository")
    data = path.read_bytes()
    if digest(data) != replacement["sha256"]:
        raise ValueError(f"Image replacement checksum mismatch: {source}")
    return data


def cover_replacement_data(lock, source):
    replacement = lock.get("cover_replacements", {}).get(source)
    if replacement is None:
        return None
    path = (ROOT / replacement["path"]).resolve()
    if not path.is_relative_to(ROOT.resolve()):
        raise ValueError("Cover replacement outside repository")
    data = path.read_bytes()
    if digest(data) != replacement["sha256"]:
        raise ValueError(f"Cover replacement checksum mismatch: {source}")
    return data


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--original", type=pathlib.Path, required=True)
    p.add_argument("--illustrated", type=pathlib.Path, required=True)
    p.add_argument("--check", action="store_true")
    args = p.parse_args()
    lock = read_json(LOCK)
    for key, path in [("original", args.original), ("illustrated", args.illustrated)]:
        if git_sha(path) != lock[key]["commit"]:
            raise ValueError(f"{key}: source commit does not match lock")
    subprocess.run(
        [
            "git",
            "diff",
            "--exit-code",
            "--quiet",
            "HEAD",
            "--",
            "dishes",
            "tips",
            "LICENSE",
        ],
        cwd=args.original,
        check=True,
    )
    out = ROOT / (
        ".run/cooking-preview-assets" if args.check else "apps/web/public/cooking"
    )
    out.mkdir(parents=True, exist_ok=True)
    cache = ROOT / ".run/cooking-downloads"
    cache.mkdir(parents=True, exist_ok=True)
    assets = {}
    missing = []

    def download(url):
        f = cache / digest(url.encode())
        if not f.exists():
            req = urllib.request.Request(
                url, headers={"User-Agent": "Multica-content-import"}
            )
            for attempt in range(3):
                try:
                    with urllib.request.urlopen(req, timeout=120) as r:
                        data = r.read()
                    break
                except urllib.error.HTTPError as ex:
                    if ex.code < 500 or attempt == 2:
                        raise
                    time.sleep(2**attempt)
                except (TimeoutError, urllib.error.URLError):
                    if attempt == 2:
                        raise
                    time.sleep(2**attempt)
            f.write_bytes(data)
        return f.read_bytes()

    def asset(data, cover=False):
        return write_asset(data, out, assets, cover)

    files = sorted(
        [
            *args.original.joinpath("dishes").rglob("*.md"),
            *args.original.joinpath("tips").rglob("*.md"),
        ]
    )
    entries = []
    by_path = {}
    excluded = []
    by_name = {}
    for f in files:
        rel = f.relative_to(args.original).as_posix()
        if "template" in f.relative_to(args.original).parts or f.stem.lower() in (
            "template",
            "模板",
        ):
            excluded.append({"path": rel, "reason": "菜谱模板"})
            continue
        kind = "tip" if rel.startswith("tips/") else "recipe"
        cat = "tips" if kind == "tip" else f.relative_to(args.original).parts[1]
        stable = lock.get("stable_ids", {}).get(rel, digest(rel.encode())[:20])
        text = f.read_text()
        difficulty = re.search(r"预估烹饪难度[：:]\s*([^\n]+)", text)
        ingredients = re.search(
            r"^## .*?(?:原料|材料).*?\n(.*?)(?=^## |\Z)", text, re.M | re.S
        )
        e = {
            "id": stable,
            "name": f.stem,
            "kind": kind,
            "category": cat,
            "category_name": CATEGORIES.get(cat, cat),
            "markdown": text,
            "difficulty": difficulty.group(1).strip() if difficulty else "",
            "difficulty_rank": difficulty.group(1).count("★") if difficulty else 0,
            "ingredients": ingredients.group(1).strip() if ingredients else "",
            "cover": "",
            "illustrations": [],
            "source_path": rel,
            "source_sha256": digest(f.read_bytes()),
            "source_url": f"{lock['original']['url']}/blob/{lock['original']['commit']}/{urllib.parse.quote(rel)}",
            "image_source_url": "",
            "available": True,
        }
        if kind == "tip":
            e["category_name"] = (
                "厨具使用"
                if f.stem in ("微波炉", "高压力锅", "空气炸锅")
                else "厨房准备"
                if f.parent == args.original / "tips"
                else "食材处理"
                if f.stem in ("去腥", "辅料技巧", "食品安全")
                else "烹饪方法"
            )
        entries.append(e)
        by_path[rel] = e
        by_name.setdefault(f.stem, []).append(e)
    # Resolve markdown assets and document links after all stable IDs are known.
    links = []
    for e in entries:
        f = args.original / e["source_path"]
        photos = []

        def replace(m):
            prefix, label, url = m.group(1), m.group(2), m.group(3)
            url = url.strip("<>")
            parsed = urllib.parse.urlsplit(url)
            if url.startswith("#") or parsed.scheme in ("mailto", "tel"):
                return m.group(0)
            local = (
                (f.parent / urllib.parse.unquote(parsed.path)).resolve()
                if not parsed.scheme
                else None
            )
            if prefix == "!":
                try:
                    data = replacement_data(lock, url, e["source_path"])
                    if data is None:
                        data = (
                            download(url)
                            if parsed.scheme in ("http", "https")
                            else local.read_bytes()
                        )
                    dest = asset(data)
                    photos.append(data)
                except urllib.error.HTTPError as ex:
                    if ex.code != 404:
                        raise
                    missing.append(
                        {
                            "source": e["source_path"],
                            "url": url,
                            "reason": "upstream HTTP 404",
                        }
                    )
                    return (
                        f"\n> 原文图片暂不可用：{label or '配图'}（来源图片已失效）\n"
                    )
                return f"![{label}]({dest})"
            if local and local.suffix.lower() == ".md":
                try:
                    rel = local.relative_to(args.original.resolve()).as_posix()
                except ValueError:
                    raise ValueError(f"Link outside source: {url}")
                target = by_path.get(rel)
                if target:
                    dest = f"/__cooking/{'tips' if target['kind'] == 'tip' else 'recipes'}/{target['id']}"
                    links.append({"from": e["id"], "to": target["id"]})
                    return f"[{label}]({dest})"
                # Root/project documentation remains an explicit source link, not a broken local route.
                return f"[{label}]({lock['original']['url']}/blob/{lock['original']['commit']}/{urllib.parse.quote(rel)})"
            if local:
                if not local.is_file():
                    raise ValueError(f"Missing linked file {e['source_path']}: {url}")
                data = local.read_bytes()
                key = digest(data) + local.suffix
                target = out / key
                target.write_bytes(data)
                assets[key] = digest(data)
                return f"[{label}](/cooking/{key})"
            return m.group(0)

        e["markdown"] = re.sub(
            r"(!?)\[([^\]]*)\]\(((?:[^()\n]|\([^()\n]*\))+)\)", replace, e["markdown"]
        )
        if photos:
            e["cover"] = asset(photos[0], True)
        else:
            replacement = cover_replacement_data(lock, e["source_path"])
            if replacement is not None:
                photo = asset(replacement)
                e["cover"] = asset(replacement, True)
                heading = re.search(r"^# .*?(?:\n|$)", e["markdown"], re.M)
                if heading:
                    e["markdown"] = (
                        e["markdown"][: heading.end()]
                        + f"\n![{e['name']}]({photo})\n"
                        + e["markdown"][heading.end() :]
                    )
                else:
                    e["markdown"] = f"![{e['name']}]({photo})\n\n" + e["markdown"]
    index_path = args.illustrated / "src/data/recipes.json"
    if index_path.read_bytes() != subprocess.check_output(
        ["git", "show", lock["illustrated"]["commit"] + ":src/data/recipes.json"],
        cwd=args.illustrated,
    ):
        raise ValueError("Index differs from pinned commit")
    index = read_json(index_path)
    graphs = [x for c in index for x in c["recipes"]]
    mapping = []

    def graphic(x):
        local = args.illustrated / "public" / x["imagePath"]
        data = local.read_bytes()
        pinned = subprocess.check_output(
            [
                "git",
                "show",
                lock["illustrated"]["commit"] + ":public/" + x["imagePath"],
            ],
            cwd=args.illustrated,
        )
        if data != pinned:
            raise ValueError(
                "Illustration source differs from pinned commit: " + x["id"]
            )
        if data.startswith(b"version https://git-lfs"):
            oid = re.search(rb"oid sha256:([a-f0-9]+)", data).group(1).decode()
            url = f"https://media.githubusercontent.com/media/king-jingxiang/HowToCook/{lock['illustrated']['commit']}/public/{urllib.parse.quote(x['imagePath'])}"
            data = download(url)
            if digest(data) != oid:
                raise ValueError("LFS SHA mismatch: " + x["id"])
        return x, asset(data), asset(data, True)

    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
        for i, (x, url, cover) in enumerate(executor.map(graphic, graphs)):
            candidates = [
                e for e in by_name.get(x["name"], []) if e["category"] == x["category"]
            ]
            override = lock.get("mapping", {}).get(x["id"])
            if override:
                candidates = [by_path[override]]
            if len(candidates) > 1:
                raise ValueError("Ambiguous mapping: " + x["id"])
            if candidates:
                e = candidates[0]
                state = "matched"
            else:
                e = {
                    "id": digest(("illustrated/" + x["id"]).encode())[:20],
                    "name": x["name"],
                    "kind": "tip" if x["category"] == "tips" else "recipe",
                    "category": x["category"],
                    "category_name": CATEGORIES[x["category"]],
                    "markdown": "",
                    "difficulty": "",
                    "difficulty_rank": 0,
                    "ingredients": "",
                    "cover": "",
                    "illustrations": [],
                    "source_path": "",
                    "source_url": "",
                    "image_source_url": "",
                    "available": True,
                }
                entries.append(e)
                state = "illustration_only"
            e["illustrations"].append(url)
            e["image_source_url"] = (
                f"{lock['illustrated']['url']}/blob/{lock['illustrated']['commit']}/public/{urllib.parse.quote(x['imagePath'])}"
            )
            if not e["cover"]:
                e["cover"] = cover
            mapping.append(
                {"image": x["imagePath"], "entry_id": e["id"], "state": state}
            )
            if i % 40 == 0:
                print(f"Illustrations {i + 1}/{len(graphs)}", flush=True)
    entries.sort(
        key=lambda e: (e["kind"], e["category"], e["source_path"] or e["name"])
    )
    report = {
        "sources": lock,
        "text_recipes": sum(
            bool(e["markdown"]) and e["kind"] == "recipe" for e in entries
        ),
        "text_tips": sum(bool(e["markdown"]) and e["kind"] == "tip" for e in entries),
        "illustrations": len(graphs),
        "matched": sum(x["state"] == "matched" for x in mapping),
        "illustration_only": [e["id"] for e in entries if not e["markdown"]],
        "text_only": [e["id"] for e in entries if not e["illustrations"]],
        "excluded": excluded,
        "mapping": mapping,
        "links": links,
        "assets": dict(sorted(assets.items())),
        "unavailable_images": missing,
    }
    payload = {
        "version": digest(json.dumps(report, sort_keys=True).encode())[:16],
        "entries": entries,
    }
    catalog = ROOT / "server/internal/cooking/catalog.json"
    old = read_json(catalog) if catalog.exists() else {"entries": []}
    old_ids = {e["id"] for e in old["entries"]}
    new_ids = {e["id"] for e in entries}
    print(
        json.dumps(
            {
                "entries": len(entries),
                "added": len(new_ids - old_ids),
                "removed": sorted(old_ids - new_ids),
                "version": payload["version"],
            },
            ensure_ascii=False,
        )
    )
    if not args.check:
        save_json(catalog, payload)
        save_json(ROOT / "scripts/cooking/inventory.json", report)
        for key, source in [
            ("original", args.original),
            ("illustrated", args.illustrated),
        ]:
            (out / (key + "-LICENSE.txt")).write_bytes(
                (source / "LICENSE").read_bytes()
            )
        save_json(
            out / "version.json", {"version": payload["version"], "sources": lock}
        )
        for stale in out.iterdir():
            if stale.name not in assets and stale.name not in (
                "original-LICENSE.txt",
                "illustrated-LICENSE.txt",
                "version.json",
            ):
                stale.unlink()


if __name__ == "__main__":
    main()
