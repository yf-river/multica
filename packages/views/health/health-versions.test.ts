// @vitest-environment node
import { describe, expect, it } from "vitest";
import { HealthObservationSchema } from "@multica/core/health";
import { versionChanges } from "./health-versions";

const observation = (id: string, code: string, value: string) => HealthObservationSchema.parse({ id, code, name: code, value, source: "manual", status: "accepted" });

describe("versionChanges", () => {
  it("pairs replaced values of the same metric as one change", () => {
    const changes = versionChanges([observation("new", "weight", "70")], [observation("old", "weight", "71")]);
    expect(changes).toHaveLength(1);
    expect(changes[0]).toMatchObject({ kind: "changed", before: { id: "new" }, after: { id: "old" } });
  });

  it("classifies changes in the direction of restoring the target", () => {
    const same = observation("same", "height", "170");
    const changes = versionChanges([same, observation("remove", "weight", "70")], [same, observation("add", "bmi", "24")]);
    expect(changes.map((item) => item.kind).sort()).toEqual(["added", "removed"]);
  });
});
