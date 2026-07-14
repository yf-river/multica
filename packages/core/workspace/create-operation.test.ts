// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { resetAccountState } from "../platform/workspace-storage";
import type { Workspace } from "../types";
import { createWorkspaceWithRecovery } from "./create-operation";

const workspace = (id: string) => ({ id }) as Workspace;

describe("createWorkspaceWithRecovery", () => {
  beforeEach(() => {
    localStorage.clear();
    resetAccountState();
  });

  it("persists the exact workspace intent after an unknown outcome", async () => {
    const createWorkspace = vi.fn().mockRejectedValue(
      new ApiTransportError("POST workspace", true, new Error("lost")),
    );
    await expect(createWorkspaceWithRecovery(
      { name: "Current", slug: "current" },
      { createWorkspace },
    )).rejects.toBeInstanceOf(ApiTransportError);
    const stored = localStorage.getItem("multica_workspace_create_operation") ?? "";
    expect(stored).toContain("current");
    expect(stored).toMatch(/[0-9a-f-]{36}/);
  });

  it("recovers an older workspace before creating a changed request", async () => {
    const createWorkspace = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old workspace", true, new Error("lost")))
      .mockResolvedValueOnce(workspace("workspace-1"))
      .mockResolvedValueOnce(workspace("workspace-2"));
    const client = { createWorkspace };
    await expect(createWorkspaceWithRecovery({ name: "Old", slug: "old" }, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createWorkspaceWithRecovery({ name: "New", slug: "new" }, client))
      .resolves.toMatchObject({ id: "workspace-2" });
    expect(createWorkspace).toHaveBeenCalledTimes(3);
    expect(createWorkspace.mock.calls[1]).toEqual([
      { name: "Old", slug: "old" }, createWorkspace.mock.calls[0]?.[1],
    ]);
    expect(createWorkspace.mock.calls[2]?.[0]).toEqual({ name: "New", slug: "new" });
  });
});
