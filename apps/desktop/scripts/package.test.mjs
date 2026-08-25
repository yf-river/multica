import { delimiter, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const childProcess = vi.hoisted(() => ({
  execFileSync: vi.fn(),
  execSync: vi.fn(),
  spawnSync: vi.fn(),
}));

vi.mock("node:child_process", () => ({ ...childProcess, default: childProcess }));

import { main } from "./package-runner.mjs";

const desktopRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

function runtime(overrides = {}) {
  return {
    argv: [],
    platform: "darwin",
    arch: "arm64",
    env: {
      PATH: ["/usr/local/bin", "/usr/bin"].join(delimiter),
      APPLE_TEAM_ID: "team-id",
    },
    ...overrides,
  };
}

function builderCalls() {
  return childProcess.spawnSync.mock.calls
    .filter(([command]) => command === "electron-builder")
    .map(([, args]) => args);
}

beforeEach(() => {
  childProcess.execFileSync.mockReset();
  childProcess.execSync.mockReset().mockReturnValue("v1.2.3\n");
  childProcess.spawnSync.mockReset().mockReturnValue({ status: 0 });
  vi.spyOn(console, "log").mockImplementation(() => {});
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Desktop package runner", () => {
  it("builds one explicit target with canonical version and forwarded arguments", () => {
    const result = main(runtime({
      argv: ["--", "--win", "nsis", "--arm64", "--publish", "always"],
    }));

    expect(result).toBe(0);
    expect(childProcess.spawnSync).toHaveBeenNthCalledWith(
      1,
      "electron-vite",
      ["build"],
      expect.objectContaining({ cwd: desktopRoot, shell: true }),
    );
    expect(childProcess.execFileSync).toHaveBeenCalledWith(
      "node",
      [
        resolve(desktopRoot, "scripts/bundle-cli.mjs"),
        "--target-platform",
        "win32",
        "--target-arch",
        "arm64",
      ],
      { cwd: desktopRoot, stdio: "inherit" },
    );
    expect(builderCalls()).toEqual([[
      "-c.extraMetadata.version=1.2.3",
      "--win",
      "nsis",
      "--arm64",
      "--publish",
      "always",
      "-c.publish.channel=latest-arm64",
    ]]);
  });

  it("builds the complete release matrix with isolated output and portable Linux targets", () => {
    expect(main(runtime({ argv: ["--all-platforms", "--publish", "never"] }))).toBe(0);

    expect(builderCalls()).toEqual([
      ["-c.extraMetadata.version=1.2.3", "--mac", "--arm64", "--publish", "never", "-c.directories.output=dist/mac-arm64"],
      ["-c.extraMetadata.version=1.2.3", "--win", "--x64", "--publish", "never", "-c.directories.output=dist/win-x64"],
      ["-c.extraMetadata.version=1.2.3", "--win", "--arm64", "--publish", "never", "-c.directories.output=dist/win-arm64", "-c.publish.channel=latest-arm64"],
      ["-c.extraMetadata.version=1.2.3", "--linux", "AppImage", "--x64", "--publish", "never", "-c.directories.output=dist/linux-x64"],
      ["-c.extraMetadata.version=1.2.3", "--linux", "AppImage", "--arm64", "--publish", "never", "-c.directories.output=dist/linux-arm64"],
    ]);
  });

  it("normalizes an untagged numeric commit and supplies local binaries", () => {
    childProcess.execSync.mockReturnValue("0123456\n");
    const env = { Path: ["runner-bin", resolve(desktopRoot, "node_modules/.bin")].join(delimiter) };

    expect(main(runtime({ platform: "linux", arch: "x64", env }))).toBe(0);

    const viteEnv = childProcess.spawnSync.mock.calls[0][2].env;
    expect(viteEnv).not.toHaveProperty("PATH");
    expect(viteEnv.Path.split(delimiter)).toEqual([
      resolve(desktopRoot, "node_modules/.bin"),
      resolve(desktopRoot, "../../node_modules/.bin"),
      "runner-bin",
    ]);
    expect(builderCalls()).toEqual([[
      "-c.extraMetadata.version=0.0.0-g0123456",
      "-c.mac.notarize=false",
      "--linux",
      "--x64",
    ]]);
  });

  it("rejects conflicting or unsupported target requests before spawning tools", () => {
    expect(() => main(runtime({ argv: ["--all-platforms", "--win"] }))).toThrow(
      /cannot be combined/,
    );
    expect(() => main(runtime({ argv: ["--win", "--universal"] }))).toThrow(
      /unsupported Desktop CLI architecture/,
    );
    expect(childProcess.spawnSync).not.toHaveBeenCalled();
  });

  it.each([
    [{ error: new Error("missing vite") }, 1],
    [{ status: 7 }, 7],
  ])("returns the electron-vite failure status %#", (viteResult, expected) => {
    childProcess.spawnSync.mockReturnValueOnce(viteResult);
    expect(main(runtime())).toBe(expected);
    expect(childProcess.execFileSync).not.toHaveBeenCalled();
  });

  it("returns the electron-builder failure status", () => {
    childProcess.spawnSync
      .mockReturnValueOnce({ status: 0 })
      .mockReturnValueOnce({ status: 9 });

    expect(main(runtime())).toBe(9);
  });
});
