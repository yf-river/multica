// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { readBuildInfo } from "./build-info";
import { GET } from "../app/api/version/route";

let directory = "";
afterEach(async () => {
  vi.unstubAllEnvs();
  if (directory) await rm(directory, { recursive: true, force: true });
});
it("reads only public identity from the running artifact and disables caching", async () => {
  directory = await mkdtemp(join(tmpdir(), "build-info-"));
  const manifest = join(directory, "release.json");
  vi.stubEnv("MULTICA_RELEASE_MANIFEST", manifest);
  const artifact = { built_at: "2026-10-19T06:22:00Z", source_commit: "a".repeat(40), source_digest: "b".repeat(64), source_directory: "/private/source" };
  await writeFile(manifest, JSON.stringify(artifact));
  const response = await GET();
  expect(response.headers.get("cache-control")).toBe("no-store");
  expect(await response.json()).toEqual({ builtAt: artifact.built_at, commit: artifact.source_commit, digest: artifact.source_digest });
  await writeFile(manifest, JSON.stringify({ ...artifact, built_at: "invalid" }));
  await expect(readBuildInfo()).rejects.toThrow("有效构建信息");
});
it("does not invent a build time for development", async () => {
  vi.stubEnv("MULTICA_RELEASE_MANIFEST", "");
  expect(await readBuildInfo()).toBeNull();
});
