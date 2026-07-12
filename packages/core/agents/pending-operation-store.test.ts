// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { useAgentPendingOperationStore } from "./pending-operation-store";

describe("agent pending operation store", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
    useAgentPendingOperationStore.getState().clear();
  });

  it("persists the exact request and key per workspace", async () => {
    useAgentPendingOperationStore.getState().setPendingCreate({
      requestKey: "66666666-6666-4666-8666-666666666666",
      request: { name: "Reviewer", runtime_id: "runtime-1" },
    });
    await useAgentPendingOperationStore.persist.rehydrate();

    expect(useAgentPendingOperationStore.getState().pendingCreate).toEqual({
      requestKey: "66666666-6666-4666-8666-666666666666",
      request: { name: "Reviewer", runtime_id: "runtime-1" },
    });
    expect(localStorage.getItem("multica_agent_pending_operations:test-workspace"))
      .toContain("runtime-1");
  });
});
