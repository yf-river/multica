// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { createWorkspacePendingCreateStore } from "./pending-create-store";
import { setCurrentWorkspace } from "./workspace-storage";

const usePendingCreateStore = createWorkspacePendingCreateStore<{
  name: string;
}>("test_pending_create");

describe("workspace pending create store", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
    usePendingCreateStore.getState().setPendingCreate();
  });

  it("persists and rehydrates the exact request and key in the active workspace", async () => {
    const operation = {
      requestKey: "55555555-5555-4555-8555-555555555555",
      request: { name: "Current request" },
    };
    usePendingCreateStore.getState().setPendingCreate(operation);
    const persisted = localStorage.getItem(
      "test_pending_create:test-workspace",
    );

    usePendingCreateStore.getState().setPendingCreate();
    localStorage.setItem("test_pending_create:test-workspace", persisted!);
    await usePendingCreateStore.persist.rehydrate();

    expect(usePendingCreateStore.getState().pendingCreate).toEqual(operation);
  });
});
