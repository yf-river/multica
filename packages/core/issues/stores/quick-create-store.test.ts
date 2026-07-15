import { beforeEach, describe, expect, it } from "vitest";
import { useQuickCreateStore } from "./quick-create-store";

const RESET_STATE = {
  lastActorType: null,
  lastActorId: null,
  lastProjectId: null,
  prompt: "",
  keepOpen: false,
  pendingOperation: null,
};

describe("quick create store", () => {
  beforeEach(() => {
    useQuickCreateStore.setState(RESET_STATE);
  });

  it("clears only the pending operation owned by the completed request", () => {
    const { setPendingOperation, clearPendingOperation } = useQuickCreateStore.getState();
    setPendingOperation({
      request: { prompt: "Create one issue", agent_id: "agent-1" },
      idempotencyKey: "key-1",
    });

    clearPendingOperation("another-key");
    expect(useQuickCreateStore.getState().pendingOperation?.idempotencyKey).toBe("key-1");

    clearPendingOperation("key-1");
    expect(useQuickCreateStore.getState().pendingOperation).toBeNull();
  });
});
