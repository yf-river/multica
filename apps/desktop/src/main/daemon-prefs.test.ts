import { mkdtemp, readFile, readdir, rm, writeFile } from "fs/promises";
import { tmpdir } from "os";
import { join } from "path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_DAEMON_PREFS,
  loadDaemonPrefs,
  saveDaemonPrefs,
} from "./daemon-prefs";

describe("daemon preferences", () => {
  let dir: string;
  let path: string;

  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), "multica-daemon-prefs-"));
    path = join(dir, "desktop_prefs.json");
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("uses defaults only when the file does not exist", async () => {
    await expect(loadDaemonPrefs(path)).resolves.toEqual(DEFAULT_DAEMON_PREFS);
  });

  it("rejects malformed JSON instead of silently replacing user data", async () => {
    await writeFile(path, "{broken", "utf-8");

    await expect(loadDaemonPrefs(path)).rejects.toThrow("invalid daemon preferences");
    await expect(readFile(path, "utf-8")).resolves.toBe("{broken");
  });

  it("rejects invalid IPC field types", async () => {
    await expect(
      saveDaemonPrefs(path, { autoStart: "yes", autoStop: false }),
    ).rejects.toThrow("autoStart must be a boolean");
  });

  it("fills missing fields and atomically writes the canonical shape", async () => {
    await expect(saveDaemonPrefs(path, { autoStop: true })).resolves.toEqual({
      autoStart: true,
      autoStop: true,
    });
    await expect(loadDaemonPrefs(path)).resolves.toEqual({
      autoStart: true,
      autoStop: true,
    });
    await expect(readdir(dir)).resolves.toEqual(["desktop_prefs.json"]);
  });
});
