// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  HealthOverviewSchema,
  HealthReportSchema,
  EMPTY_HEALTH,
  observationWire,
  isHealthAbnormal,
} from "./schema";
import { parseWithFallback, setSchemaLogger } from "../api/schema";

describe("health API boundary", () => {
  it("does not highlight absent abnormal markers", () => {
    expect(isHealthAbnormal({ abnormal: "-" })).toBe(false);
    expect(isHealthAbnormal({ abnormal: "正常" })).toBe(false);
    expect(isHealthAbnormal({ abnormal: "↑" })).toBe(true);
  });
  it("does not turn malformed data into a usable empty patient record", () => {
    expect(
      parseWithFallback(
        { revision: "bad", profile: {} },
        HealthOverviewSchema,
        EMPTY_HEALTH,
        { endpoint: "/api/health/overview", sensitive: true },
      ).available,
    ).toBe(false);
    expect(HealthReportSchema.safeParse({ id: "partial" }).success).toBe(false);
  });
  it("converts profile fields and keeps absent birthday unknown", () => {
    const result = HealthOverviewSchema.parse({
      profile: {},
      revision: 1,
      epoch: 0,
      age: null,
      observations: [],
      current: [],
    });
    expect(result.profile.birthDate).toBe("");
    expect(result.age).toBeNull();
    expect(
      observationWire({
        name: "体重",
        value: "70",
        unit: "kg",
        observedAt: "2025-07-26",
      }).observed_at,
    ).toBe("2025-07-26");
    expect(
      observationWire({ measurementGroupId: "blood-pressure-group" })
        .measurement_group_id,
    ).toBe("blood-pressure-group");
  });
  it("redacts medical values from schema failure logs", () => {
    const warn = vi.fn();
    setSchemaLogger({ warn, debug: vi.fn(), info: vi.fn(), error: vi.fn() });
    try {
      parseWithFallback(
        { private_patient: "CONFIDENTIAL-HEALTH-RESULT" },
        HealthOverviewSchema,
        EMPTY_HEALTH,
        { endpoint: "/api/health/overview", sensitive: true },
      );
      expect(JSON.stringify(warn.mock.calls)).not.toContain(
        "CONFIDENTIAL-HEALTH-RESULT",
      );
    } finally {
      setSchemaLogger({
        warn: vi.fn(),
        debug: vi.fn(),
        info: vi.fn(),
        error: vi.fn(),
      });
    }
  });
});
