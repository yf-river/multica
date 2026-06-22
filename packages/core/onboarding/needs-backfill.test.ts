import { describe, expect, it } from "vitest";
import type { User } from "../types";
import { needsSourceBackfill } from "./needs-backfill";

const BASE_USER: User = {
  id: "u1",
  name: "User",
  account: "user",
  avatar_url: null,
  onboarded_at: "2025-01-01T00:00:00Z",
  onboarding_questionnaire: {},
  starter_content_state: "imported",
  profile_description: "",
  timezone: null,
  created_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
};

function makeUser(partial: Partial<User> = {}): User {
  return { ...BASE_USER, ...partial };
}

describe("needsSourceBackfill", () => {
  it("never prompts in the internal-team build", () => {
    expect(needsSourceBackfill(null, 0)).toBe(false);
    expect(needsSourceBackfill(undefined, 0)).toBe(false);
    expect(needsSourceBackfill(makeUser({ onboarded_at: null }), 0)).toBe(false);
    expect(needsSourceBackfill(makeUser({ onboarding_questionnaire: {} }), 0)).toBe(false);
    expect(needsSourceBackfill(makeUser({ onboarding_questionnaire: { role: "engineer" } }), 0)).toBe(false);
    expect(needsSourceBackfill(makeUser({ onboarding_questionnaire: { source: [] } }), 0)).toBe(false);
    expect(needsSourceBackfill(makeUser({ onboarding_questionnaire: { source: ["search"] } }), 0)).toBe(false);
  });
});
