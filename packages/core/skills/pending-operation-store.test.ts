// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { useSkillPendingOperationStore } from "./pending-operation-store";

describe("skill pending operation store", () => {
  beforeEach(() => {
	setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
    useSkillPendingOperationStore.getState().clear();
  });

  it("persists the exact request and key for response-loss recovery", async () => {
    useSkillPendingOperationStore.getState().setPendingCreate({
      requestKey: "55555555-5555-4555-8555-555555555555",
      request: { name: "Review", description: "Current contract" },
    });

    await useSkillPendingOperationStore.persist.rehydrate();

    expect(useSkillPendingOperationStore.getState().pendingCreate).toEqual({
      requestKey: "55555555-5555-4555-8555-555555555555",
      request: { name: "Review", description: "Current contract" },
    });
    expect(localStorage.getItem("multica_skill_pending_operations:test-workspace")).toContain("Current contract");
  });
});
