// @vitest-environment node
import { describe, expect, it } from "vitest";
import { EMPTY_REVIEW_SETTINGS, reviewSettingsSchema } from "./schema";

describe("review settings response", () => {
  it("preserves valid settings and unknown server fields", () => {
    const parsed = reviewSettingsSchema.parse({ ...EMPTY_REVIEW_SETTINGS, enabled: true, instructions: "审查规则", future: 1 });
    expect(parsed.enabled).toBe(true);
    expect(parsed.instructions).toBe("审查规则");
    expect(parsed.future).toBe(1);
  });
  it("rejects missing permissions and malformed editable values", () => {
    expect(reviewSettingsSchema.safeParse({}).success).toBe(false);
    expect(reviewSettingsSchema.safeParse({ ...EMPTY_REVIEW_SETTINGS, enabled: "true" }).success).toBe(false);
    expect(reviewSettingsSchema.safeParse({ ...EMPTY_REVIEW_SETTINGS, instructions: null }).success).toBe(false);
    expect(reviewSettingsSchema.safeParse({ ...EMPTY_REVIEW_SETTINGS, max_rounds: -1 }).success).toBe(false);
  });
});
