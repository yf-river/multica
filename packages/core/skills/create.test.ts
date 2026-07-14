// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Skill } from "../types";
import { createSkillWithRecovery } from "./create";
import { useSkillPendingOperationStore } from "./pending-operation-store";

const skill = { id: "skill-1" } as Skill;

describe("createSkillWithRecovery", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
    useSkillPendingOperationStore.getState().clear();
  });

  it("replays the original intent and key after an unknown outcome", async () => {
    const createSkill = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST /api/skills", true, new Error("reset")))
      .mockResolvedValueOnce(skill);
    const client = { createSkill };

    await expect(createSkillWithRecovery({ name: "Original" }, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    const pending = useSkillPendingOperationStore.getState().pendingCreate;
    expect(pending?.request).toEqual({ name: "Original" });

    await expect(createSkillWithRecovery({ name: "Changed after reload" }, client))
      .resolves.toBe(skill);
    expect(createSkill.mock.calls[1]).toEqual([
      { name: "Original" },
      pending?.requestKey,
    ]);
    expect(useSkillPendingOperationStore.getState().pendingCreate).toBeUndefined();
  });

  it("releases the key after a definitive rejection", async () => {
    const createSkill = vi.fn()
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce(skill);
    const client = { createSkill };

    await expect(createSkillWithRecovery({ name: "First" }, client))
      .rejects.toBeInstanceOf(ApiError);
    await createSkillWithRecovery({ name: "Second" }, client);

    expect(createSkill.mock.calls[0]?.[1]).not.toBe(createSkill.mock.calls[1]?.[1]);
    expect(createSkill.mock.calls[1]?.[0]).toEqual({ name: "Second" });
  });
});
