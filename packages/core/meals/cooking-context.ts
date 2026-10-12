export function mealCookingParams(params: URLSearchParams, mealsPath: string) {
 const result = new URLSearchParams(); const raw = params.get("mealReturn");
 if (!raw) return result;
 try { const url = new URL(raw, "https://multica.invalid"); if (url.origin !== "https://multica.invalid" || url.pathname !== mealsPath) return result; result.set("mealReturn", url.pathname + url.search); for (const k of ["mealWeek", "mealDate", "mealSlot"]) { const value = params.get(k); if (value) result.set(k, value); } } catch { return result; }
 return result;
}
