import { z } from "zod";

const FoodSchema = z.object({ name: z.string(), quantity: z.number().finite().positive().nullable().default(null), unit: z.string().default(""), basis: z.string().default(""), origin: z.string().default("manual"), original: z.string().default("") });
export type MealFood = z.infer<typeof FoodSchema>;
export const MealEntrySchema = z.object({ id: z.string().default(""), date: z.string(), slot: z.string(), name: z.string(), portions: z.number().int().positive(), mode: z.string().default("home"), recipe_id: z.string().default(""), batch_id: z.string().default(""), foods: z.array(FoodSchema).default([]), note: z.string().default("") }).transform(({ recipe_id, batch_id, ...e }) => ({ ...e, recipeId: recipe_id, batchId: batch_id }));
export type MealEntry = z.output<typeof MealEntrySchema>;
const IngredientSchema = FoodSchema.extend({ group_key: z.string().default(""), id: z.string(), week: z.string(), status: z.string(), manual: z.boolean().default(false), sources: z.array(z.object({ key: z.string(), name: z.string(), original: z.string().default("") })).default([]), stale: z.boolean().default(false) }).transform(({ group_key, ...f }) => ({ ...f, groupKey: group_key }));
export type MealIngredient = z.infer<typeof IngredientSchema>;
const UseSchema = z.object({ id: z.string(), date: z.string(), portions: z.number().int().positive() });
const BatchSchema = z.object({ id: z.string().default(""), name: z.string(), portions: z.number().int().positive(), made_at: z.string(), storage: z.string(), planned_dates: z.array(z.string()).default([]), recipe_id: z.string().default(""), plan_id: z.string().default(""), uses: z.array(UseSchema).default([]) }).transform(({ made_at, planned_dates, recipe_id, plan_id, ...b }) => ({ ...b, madeAt: made_at, plannedDates: planned_dates, recipeId: recipe_id, planId: plan_id }));
export type MealBatch = z.output<typeof BatchSchema>;
const PreferencesSchema = z.object({ goal: z.string().default(""), activity: z.string().default(""), restrictions: z.string().default(""), people: z.number().int().positive().default(1), prep_time: z.string().default(""), notes: z.string().default("") }).transform(({ prep_time, ...p }) => ({ ...p, prepTime: prep_time }));
export type MealPreferences = z.output<typeof PreferencesSchema>;
const DraftSchema = z.object({ preferences: PreferencesSchema.optional(), version: z.number().int().default(0), id: z.string(), week: z.string(), health_revision: z.number().int().default(0), rationale: z.string().default(""), entries: z.array(MealEntrySchema).default([]), prep_steps: z.string().default(""), status: z.string() }).transform(({ health_revision, prep_steps, ...d }) => ({ ...d, healthRevision: health_revision, prepSteps: prep_steps }));
export type MealDraft = z.output<typeof DraftSchema>;
const PlanSchema = z.object({ stale: z.boolean().default(false), id: z.string(), week: z.string(), name: z.string(), portions: z.number().int().positive(), recipe_id: z.string().default(""), steps: z.string().default(""), entry_ids: z.array(z.string()).default([]) }).transform(({ recipe_id, entry_ids, ...p }) => ({ ...p, recipeId: recipe_id, entryIds: entry_ids }));
export type MealPrep = z.output<typeof PlanSchema>;
export const MealsSchema = z.object({ revision: z.number().int().nonnegative(), health_revision: z.number().int().nonnegative().default(0), task_statuses: z.record(z.string(), z.string()).default({}), state: z.object({ preferences: PreferencesSchema, entries: z.array(MealEntrySchema), shopping: z.array(IngredientSchema), plans: z.array(PlanSchema), batches: z.array(BatchSchema), drafts: z.array(DraftSchema), tasks: z.array(z.object({ key: z.string(), issue_id: z.string() }).transform(({ issue_id, key }) => ({ key, issueId: issue_id }))) }) }).transform(({ health_revision, task_statuses, ...s }) => ({ ...s, healthRevision: health_revision, taskStatuses: task_statuses }));
export type MealsData = z.output<typeof MealsSchema>;
export interface MealCommand {
 action: "preferences" | "entry_save" | "entry_remove" | "shopping_generate" | "ingredient_save" | "ingredient_status" | "ingredient_remove" | "batch_save" | "batch_use" | "draft_start" | "draft_save" | "draft_confirm" | "task_link";
 draftVersion?: number; revision?: number; requestId?: string; id?: string; week?: string; entry?: MealEntry; ingredient?: Partial<MealIngredient>; batch?: MealBatch; use?: { date: string; portions: number }; draft?: MealDraft; preferences?: MealPreferences; entryIds?: string[]; issueId?: string;
}
// 仅转换本模块已定义的网络字段；业务对象在包内使用 camelCase。
const fields: Record<string, string> = { groupKey: "group_key", draftVersion: "draft_version", requestId: "request_id", recipeId: "recipe_id", batchId: "batch_id", healthRevision: "health_revision", prepSteps: "prep_steps", madeAt: "made_at", plannedDates: "planned_dates", planId: "plan_id", entryIds: "entry_ids", issueId: "issue_id", prepTime: "prep_time" };
export function mealWire(value: unknown): unknown { if (Array.isArray(value)) return value.map(mealWire); if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([k, v]) => [fields[k] || k, mealWire(v)])); return value; }
export function remaining(batch: MealBatch) { return batch.portions - batch.uses.reduce((sum, use) => sum + use.portions, 0); }
export function localDate(date = new Date()) { return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; }
export function weekOf(value: string) { const d = new Date(`${value}T12:00:00`); d.setDate(d.getDate() - (d.getDay() + 6) % 7); return localDate(d); }
export function dateOffset(value: string, offset: number) { const d = new Date(`${value}T12:00:00`); d.setDate(d.getDate() + offset); return localDate(d); }
export function emptyEntry(date = localDate(), slot = "lunch"): MealEntry { return { id: "", date, slot, name: "", portions: 1, mode: "home", recipeId: "", batchId: "", foods: [], note: "" }; }
export function emptyBatch(): MealBatch { return { id: "", name: "", portions: 1, madeAt: localDate(), storage: "frozen", plannedDates: [], recipeId: "", planId: "", uses: [] }; }
