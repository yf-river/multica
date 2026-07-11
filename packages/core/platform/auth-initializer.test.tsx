// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { configStore } from "../config";
import type { StorageAdapter } from "../types/storage";
import { AuthInitializer } from "./auth-initializer";

const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  getMe: vi.fn(),
  listWorkspaces: vi.fn(),
  setToken: vi.fn(),
  warn: vi.fn(),
}));

vi.mock("../api", () => ({
  getApi: () => ({
    getConfig: mocks.getConfig,
    getMe: mocks.getMe,
    listWorkspaces: mocks.listWorkspaces,
    setToken: mocks.setToken,
  }),
  ApiError: class ApiError extends Error {
    constructor(public status: number) {
      super(`API ${status}`);
    }
  },
}));

vi.mock("../auth", () => ({
  useAuthStore: { setState: vi.fn() },
}));

vi.mock("../logger", () => ({
  createLogger: () => ({
    debug: vi.fn(),
    info: vi.fn(),
    warn: mocks.warn,
    error: vi.fn(),
  }),
  noopLogger: {
    debug: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  },
}));

const emptyStorage: StorageAdapter = {
  getItem: () => null,
  setItem: () => undefined,
  removeItem: () => undefined,
};

function renderInitializer() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <AuthInitializer storage={emptyStorage}>ready</AuthInitializer>
    </QueryClientProvider>,
  );
}

describe("AuthInitializer app config", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    configStore.setState({
      cdnDomain: "",
      cdnSigned: false,
      daemonServerUrl: "",
      daemonAppUrl: "",
      workspaceCreationDisabled: false,
    });
  });

  afterEach(cleanup);

  it("fails workspace creation closed and logs config fetch failures", async () => {
    const failure = new Error("config unavailable");
    mocks.getConfig.mockRejectedValue(failure);

    renderInitializer();

    await waitFor(() => {
      expect(configStore.getState().workspaceCreationDisabled).toBe(true);
    });
    expect(mocks.warn).toHaveBeenCalledWith("app config init failed", failure);
    expect(mocks.getMe).not.toHaveBeenCalled();
  });

  it("applies the current workspace-creation setting", async () => {
    mocks.getConfig.mockResolvedValue({
      cdn_domain: "",
      cdn_signed: false,
      allow_signup: true,
      workspace_creation_disabled: false,
      daemon_server_url: "",
      daemon_app_url: "",
    });
    configStore.getState().setWorkspaceCreationDisabled(true);

    renderInitializer();

    await waitFor(() => {
      expect(configStore.getState().workspaceCreationDisabled).toBe(false);
    });
    expect(mocks.warn).not.toHaveBeenCalled();
  });
});
