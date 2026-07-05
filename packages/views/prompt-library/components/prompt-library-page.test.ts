import { describe, expect, it } from "vitest";
import { promptLibraryListParamsForFilters } from "./prompt-library-page";

describe("promptLibraryListParamsForFilters", () => {
  it("does not send prompt filters by default", () => {
    expect(promptLibraryListParamsForFilters("全部", "全部", true)).toBeUndefined();
  });

  it("maps prompt type and status filters to server query params", () => {
    expect(promptLibraryListParamsForFilters("需求澄清", "归档", true)).toEqual({
      prompt_type: "需求澄清",
      status: "归档",
    });
  });

  it("does not send prompt filters outside the prompt library editor", () => {
    expect(promptLibraryListParamsForFilters("需求澄清", "启用", false)).toBeUndefined();
  });

  it("keeps project binding out of the prompt library UI query", () => {
    expect(promptLibraryListParamsForFilters("通用", "启用", true)).not.toHaveProperty("project_id");
  });
});
