import { describe, expect, it } from "vitest";
import { buildProjectRecentContext } from "./project-detail";

describe("buildProjectRecentContext", () => {
  it("keeps the current project identity and presentation fields", () => {
    expect(buildProjectRecentContext({
      id: "project-1",
      title: "专业重构",
      description: null,
      icon: null,
      status: "in_progress",
    })).toEqual({
      type: "project",
      id: "project-1",
      label: "专业重构",
      subtitle: undefined,
      icon: null,
      projectStatus: "in_progress",
    });
  });
});
