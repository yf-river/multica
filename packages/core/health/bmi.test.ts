// @vitest-environment node
import { describe, expect, it } from "vitest";
import { calculateBMI, currentDisplayMetrics } from "./bmi";
import { HealthObservationSchema, HealthOverviewSchema } from "./schema";

const item = (id: string, code: string, value: string, unit: string) => HealthObservationSchema.parse({ id, code, name: code, value, unit, source: "manual", status: "accepted" });

describe("calculated BMI", () => {
  it("previews unit conversion and rejects unknown or invalid inputs", () => {
    const height = item("h", "height", "1.8", "m");
    expect(calculateBMI(height, item("w", "weight", "81000", "g"))?.value).toBe(25);
    for (const [value, unit] of [["", "kg"], ["0", "kg"], ["-1", "kg"], ["NaN", "kg"], ["0x51", "kg"], ["81", "lb"]]) expect(calculateBMI(height, item("w", "weight", value!, unit!))).toBeNull();
    expect(calculateBMI(undefined, item("w", "weight", "81", "kg"))).toBeNull();
  });
  it("replaces report BMI only in the display and preserves edited metric priority", () => {
    const height = item("h", "height", "180", "cm");
    const weight = item("w", "weight", "81", "kg");
    const raw = [weight, item("old-bmi", "bmi", "27", "kg/m²"), height, { ...item("ideal", "ideal_weight", "70", "kg"), name: "理想体重" }];
    const bmi = calculateBMI(height, weight);
    expect(currentDisplayMetrics(raw, bmi).map((record) => record.id)).toEqual(["w", "computed-bmi", "h"]);
    expect(raw).toHaveLength(4);
    expect(currentDisplayMetrics(raw, bmi)[1]).toMatchObject({ source: "calculated", observedAt: "", referenceRange: "", abnormal: "" });
  });
  it("accepts older responses and isolates a malformed calculation", () => {
    const overview = { profile: {}, revision: 1, epoch: 1, age: null, observations: [], current: [] };
    expect(HealthOverviewSchema.parse(overview).computedBMI).toBeNull();
    expect(HealthOverviewSchema.parse({ ...overview, computed_bmi: { value: "wrong" } }).computedBMI).toBeNull();
  });
});
