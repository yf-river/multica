#!/usr/bin/env python3
"""Apply the cooking migrations to an existing integration schema using the standard runner."""

import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import urllib.request

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--release", type=pathlib.Path, required=True)
p.add_argument("--config", type=pathlib.Path, required=True)
a = p.parse_args()
release = a.release.resolve()
cfg = json.loads(a.config.read_text())["server"]
if cfg.get("MULTICA_ENV_ID") != "int":
    raise SystemExit("Only integration configuration is accepted")
manifest = json.loads((release / "release.json").read_text())
for name in (
    "server/bin/migrate",
    "server/migrations/497_cooking_favorite.up.sql",
    "server/migrations/498_cooking_favorite_unique.up.sql",
):
    with (release / name).open("rb") as file:
        if hashlib.file_digest(file, "sha256").hexdigest() != manifest["files"].get(
            name
        ):
            raise SystemExit("Release checksum mismatch: " + name)

url = "http://127.0.0.1:" + cfg["PORT"] + "/api/config"
op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with op.open(url, timeout=5) as response:
    if json.load(response).get("environment_id") != "int":
        raise SystemExit("Running API is not integration")
# Existing integration was initialized from a schema snapshot, not the complete historical ledger.
# Do not replay or fabricate historical migrations. The normal runner still owns locking and receipts.
with tempfile.TemporaryDirectory(prefix="multica-cooking-migrations-") as directory:
    target = pathlib.Path(directory) / "migrations"
    target.mkdir()
    for name in ("497_cooking_favorite.up.sql", "498_cooking_favorite_unique.up.sql"):
        shutil.copy2(release / "server/migrations" / name, target / name)
    subprocess.run(
        [str(release / "server/bin/migrate"), "up"],
        cwd=directory,
        env={**os.environ, **cfg},
        check=True,
    )
print("Cooking integration migrations applied by the standard migration runner")
