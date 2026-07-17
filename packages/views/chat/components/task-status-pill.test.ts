import { describe, it, expect } from "vitest";
import { pickStageKeys } from "./task-status-pill";

describe("pickStageKeys", () => {
  it.each([
    ["queued", "online", { stageKey: "queued" }],
    ["queued", "offline", { stageKey: "offline", static: true }],
    ["running", "online", { stageKey: "thinking" }],
  ] as const)("maps %s/%s to the current stage", (status, presence, expected) => {
    expect(pickStageKeys(status, [], presence)).toEqual(expected);
  });
});
