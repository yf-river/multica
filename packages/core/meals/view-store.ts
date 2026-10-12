import { create } from "zustand";
import { emptyEntry, emptyBatch, type MealEntry, type MealBatch, type MealIngredient, type MealPreferences, type MealDraft } from "./schema";
export interface MealView {
 takeRecorded: boolean; baseRevision: number; editingDraftId: string;
 panel: "entry" | "ingredient" | "batch" | "use" | "preferences" | "draft" | "shopping" | null;
 entry: MealEntry; batch: MealBatch; ingredient: MealIngredient;
 preferences: MealPreferences; draft: MealDraft | null;
 recipeSearch: string; favorites: boolean; useDate: string; usePortions: number;
 requestId: string; error: string; feedback: string; selectedDate: string;
 selection: string[]; scrollTop: number; linkingId: string;
}
export const EMPTY_MEAL_VIEW: MealView = { takeRecorded: false, baseRevision: 0, editingDraftId: "", panel: null, entry: emptyEntry(), batch: emptyBatch(), ingredient: { groupKey: "", id: "", week: "", name: "", quantity: null, unit: "", basis: "", origin: "manual", original: "", status: "needed", manual: true, sources: [], stale: false }, preferences: { goal: "", activity: "", restrictions: "", people: 1, prepTime: "", notes: "" }, draft: null, recipeSearch: "", favorites: false, useDate: "", usePortions: 1, requestId: "", error: "", feedback: "", selectedDate: "", selection: [], scrollTop: 0, linkingId: "" };
export const useMealViewStore = create<{ views: Record<string, MealView>; update: (key: string, patch: Partial<MealView>) => void }>((set) => ({ views: {}, update: (key, patch) => set((s) => ({ views: { ...s.views, [key]: { ...(s.views[key] || EMPTY_MEAL_VIEW), ...patch } } })) }));
