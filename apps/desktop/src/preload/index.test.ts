import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  exposeInMainWorld: vi.fn(),
  sendSync: vi.fn(),
}));

vi.mock("electron", () => ({
  contextBridge: { exposeInMainWorld: mocks.exposeInMainWorld },
  ipcRenderer: {
    invoke: vi.fn(),
    on: vi.fn(),
    removeListener: vi.fn(),
    send: vi.fn(),
    sendSync: mocks.sendSync,
  },
}));

vi.mock("@electron-toolkit/preload", () => ({ electronAPI: {} }));

describe("desktop preload diagnostics", () => {
  beforeEach(() => {
    vi.resetModules();
    mocks.exposeInMainWorld.mockReset();
    mocks.sendSync.mockReset();
  });

  it("reports a freeze breadcrumb IPC failure instead of silently treating it as absent", async () => {
    mocks.sendSync.mockImplementation((channel: string) => {
      if (channel === "app:get-info") return { version: "1.0.0", os: "linux" };
      if (channel === "runtime-config:get") return { ok: true, value: {} };
      if (channel === "freeze:get-last") throw new Error("ipc unavailable");
      return undefined;
    });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);

    await import("./index");
    const desktopAPI = mocks.exposeInMainWorld.mock.calls.find(
      ([name]) => name === "desktopAPI",
    )?.[1] as { getLastFreeze: () => unknown };

    expect(desktopAPI.getLastFreeze()).toBeNull();
    expect(consoleError).toHaveBeenCalledWith(
      "[diagnostics] failed to read last freeze breadcrumb",
      expect.any(Error),
    );

    consoleError.mockRestore();
  });
});
