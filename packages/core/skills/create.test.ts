// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Skill } from "../types";
import { createSkillWithRecovery } from "./create";

const skill = { id: "skill-1" } as Skill;

describe("createSkillWithRecovery", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    setCurrentWorkspace("test-workspace", "workspace-1");
    localStorage.clear();
  });

  it("replays the original intent and key after an unknown outcome", async () => {
    const createSkill = vi.spyOn(getApi(), "createSkill")
      .mockRejectedValueOnce(new ApiTransportError("POST /api/skills", true, new Error("reset")))
      .mockResolvedValueOnce(skill);
    await expect(createSkillWithRecovery({ name: "Original" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    const requestKey = createSkill.mock.calls[0]?.[1];

    await expect(createSkillWithRecovery({ name: "Changed after reload" }))
      .resolves.toBe(skill);
    expect(createSkill.mock.calls[1]).toEqual([
      { name: "Original" },
      requestKey,
    ]);
  });

  it("releases the key after a definitive rejection", async () => {
    const createSkill = vi.spyOn(getApi(), "createSkill")
      .mockRejectedValueOnce(new ApiError("invalid", 400, "Bad Request"))
      .mockResolvedValueOnce(skill);
    await expect(createSkillWithRecovery({ name: "First" }))
      .rejects.toBeInstanceOf(ApiError);
    await createSkillWithRecovery({ name: "Second" });

    expect(createSkill.mock.calls[0]?.[1]).not.toBe(createSkill.mock.calls[1]?.[1]);
    expect(createSkill.mock.calls[1]?.[0]).toEqual({ name: "Second" });
  });
});
