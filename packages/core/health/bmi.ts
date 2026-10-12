import type { ComputedBMI, HealthObservation } from "./schema";

export function calculateBMI(height?: HealthObservation, weight?: HealthObservation): ComputedBMI | null {
  if (!height || !weight || !height.value.trim() || !weight.value.trim()) return null;
  const number = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;
  if (!number.test(height.value.trim()) || !number.test(weight.value.trim())) return null;
  let h = Number(height.value);
  let w = Number(weight.value);
  if (!Number.isFinite(h) || !Number.isFinite(w) || h <= 0 || w <= 0) return null;
  if (["cm", "厘米"].includes(height.unit)) h /= 100;
  else if (!["m", "米"].includes(height.unit)) return null;
  if (["g", "克"].includes(weight.unit)) w /= 1000;
  else if (!["kg", "千克", "公斤"].includes(weight.unit)) return null;
  const value = Math.round((w / (h * h)) * 10) / 10;
  return Number.isFinite(value) && value > 0 ? { value, unit: "kg/m²", height, weight } : null;
}

export function currentDisplayMetrics(current: HealthObservation[], bmi: ComputedBMI | null) {
  const items = current.filter((item) => item.code !== "bmi" && item.code !== "ideal_weight" && !item.name.replace(/\s/g, "").includes("理想体重"));
  if (!bmi) return items;
  const computed: HealthObservation = {
    ...bmi.weight, id: "computed-bmi", code: "bmi", name: "BMI", value: bmi.value.toFixed(1), unit: bmi.unit,
    source: "calculated", observedAt: "", originalName: "", originalValue: "", originalUnit: "",
    reportId: "", page: 0, evidence: "", uncertainty: "", referenceRange: "", abnormal: "", supersedes: "", measurementGroupId: "",
  };
  const weightIndex = items.findIndex((item) => item.id === bmi.weight.id);
  items.splice(weightIndex < 0 ? 0 : weightIndex + 1, 0, computed);
  return items;
}
