import { mkdir, rename, rm, writeFile } from "fs/promises";
import { dirname } from "path";
import type { DaemonPrefs } from "../shared/daemon-types";
import { readOptionalJsonObject } from "./json-config-file";

const DEFAULT_DAEMON_PREFS: DaemonPrefs = {
  autoStart: true,
  autoStop: false,
};

function parseDaemonPrefs(value: unknown): DaemonPrefs {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("daemon preferences must be a JSON object");
  }

  const candidate = value as Record<string, unknown>;
  return {
    autoStart: readBooleanPref(candidate, "autoStart"),
    autoStop: readBooleanPref(candidate, "autoStop"),
  };
}

function readBooleanPref(
  candidate: Record<string, unknown>,
  key: keyof DaemonPrefs,
): boolean {
  const value = candidate[key];
  if (value === undefined) return DEFAULT_DAEMON_PREFS[key];
  if (typeof value !== "boolean") {
    throw new Error(`daemon preference ${key} must be a boolean`);
  }
  return value;
}

export async function loadDaemonPrefs(path: string): Promise<DaemonPrefs> {
  try {
    return parseDaemonPrefs(await readOptionalJsonObject(path));
  } catch (error) {
    throw new Error(`invalid daemon preferences at ${path}`, { cause: error });
  }
}

export async function saveDaemonPrefs(
  path: string,
  value: unknown,
): Promise<DaemonPrefs> {
  const prefs = parseDaemonPrefs(value);
  await mkdir(dirname(path), { recursive: true });

  const tempPath = `${path}.${process.pid}.${Date.now()}.tmp`;
  try {
    await writeFile(tempPath, JSON.stringify(prefs, null, 2), "utf-8");
    await rename(tempPath, path);
  } catch (error) {
    try {
      await rm(tempPath, { force: true });
    } catch {
      // Preserve the original write/rename error. The uniquely named temp file
      // is harmless and can be removed on the next maintenance pass.
    }
    throw new Error(`failed to write daemon preferences at ${path}`, { cause: error });
  }

  return prefs;
}
