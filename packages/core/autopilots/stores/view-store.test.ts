import { describe, expect, it } from "vitest";
import { migrateAutopilotsViewState } from "./view-store";

describe("migrateAutopilotsViewState", () => {
  it("normalizes the removed archived scope", () => {
    expect(
      migrateAutopilotsViewState({
        scope: "archived",
        sortField: "created",
      }),
    ).toEqual({
      scope: "all",
      sortField: "created",
    });
  });

  it("preserves current scopes", () => {
    expect(migrateAutopilotsViewState({ scope: "paused" })).toEqual({
      scope: "paused",
    });
  });

  it("uses current defaults for an invalid persisted payload", () => {
    expect(migrateAutopilotsViewState(null)).toMatchObject({
      scope: "all",
      sortField: "lastRun",
      sortDirection: "desc",
    });
  });
});
