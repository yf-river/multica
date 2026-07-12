// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import type { Workspace } from "../types";
import {
  createWorkspaceWithRecovery,
  useWorkspaceCreateOperationStore,
  type WorkspaceCreateClient,
} from "./create-operation";

const workspace = (id: string) => ({ id }) as Workspace;

describe("createWorkspaceWithRecovery", () => {
  beforeEach(() => {
    localStorage.clear();
    useWorkspaceCreateOperationStore.setState({ pending: undefined });
  });

  it("persists the exact workspace intent after an unknown outcome", async () => {
    const createWorkspace = vi.fn().mockRejectedValue(
      new ApiTransportError("POST workspace", true, new Error("lost")),
    );
    await expect(createWorkspaceWithRecovery(
      { name: "Current", slug: "current" },
      { createWorkspace },
    )).rejects.toBeInstanceOf(ApiTransportError);
    const pending = useWorkspaceCreateOperationStore.getState().pending;
    expect(pending?.request).toEqual({ name: "Current", slug: "current" });
    expect(pending?.requestKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(localStorage.getItem("multica_workspace_create_operation")).toContain("current");
  });

  it("recovers an older workspace before creating a changed request", async () => {
    useWorkspaceCreateOperationStore.getState().setPending({
      request: { name: "Old", slug: "old" },
      requestKey: "10000000-0000-4000-8000-000000000006",
      createdAt: Date.now(),
    });
    const createWorkspace = vi.fn()
      .mockResolvedValueOnce(workspace("workspace-1"))
      .mockResolvedValueOnce(workspace("workspace-2"));
    const client: WorkspaceCreateClient = { createWorkspace };
    await expect(createWorkspaceWithRecovery({ name: "New", slug: "new" }, client))
      .resolves.toMatchObject({ id: "workspace-2" });
    expect(createWorkspace).toHaveBeenCalledTimes(2);
    expect(useWorkspaceCreateOperationStore.getState().pending).toBeUndefined();
  });
});
