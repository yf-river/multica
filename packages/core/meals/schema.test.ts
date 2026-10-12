// @vitest-environment node
import { describe, expect, it } from "vitest";
import { MealsSchema, mealWire, weekOf } from "./schema";
import { mealCookingParams } from "./cooking-context";
describe("meals boundary", () => {
 it("rejects malformed saved data instead of claiming a successful empty save", () => { expect(MealsSchema.safeParse({ revision: 1, state: { entries: "broken" } }).success).toBe(false); expect(MealsSchema.safeParse({ revision: 1, state: null }).success).toBe(false); });
 it("translates scoped wire fields and preserves unknown quantities", () => { const result = MealsSchema.parse({ revision: 2, health_revision: 3, state: { preferences: { people: 1 }, entries: [{ date: "2026-10-12", name: "菜", slot: "lunch", portions: 1, recipe_id: "recipe" }], shopping: [], batches: [], plans: [], drafts: [], tasks: [] } }); expect(result.healthRevision).toBe(3); expect(result.state.entries[0]?.recipeId).toBe("recipe"); expect(mealWire({ requestId: "id", entry: result.state.entries[0] })).toMatchObject({ request_id: "id", entry: { recipe_id: "recipe", foods: [] } }); });
 it("restricts cooking return to the same workspace meals page", () => { expect(mealCookingParams(new URLSearchParams({ mealReturn: "https://evil.test/yf/meals" }), "/yf/meals").size).toBe(0); expect(mealCookingParams(new URLSearchParams({ mealReturn: "/other/meals" }), "/yf/meals").size).toBe(0); expect(mealCookingParams(new URLSearchParams({ mealReturn: "/yf/meals?week=2026-10-12&tab=plan" }), "/yf/meals").get("mealReturn")).toContain("week=2026-10-12"); });
 it("uses Monday week boundaries across months", () => { expect(weekOf("2026-11-01")).toBe("2026-10-26"); });
});
