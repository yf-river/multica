import type { RuntimeModel } from "@multica/core/types";

const preferredDeepSeekModelIds = [
  "deepseek-v4-pro-ioa",
  "deepseek-v4-pro",
];

export function preferredPMModel(models: RuntimeModel[]) {
  const exactPreferred = preferredDeepSeekModelIds
    .map((id) => models.find((model) => model.id.toLowerCase() === id))
    .find(Boolean);
  const namedV4Pro =
    exactPreferred ??
    models.find((model) => {
      const haystack = `${model.id} ${model.label}`.toLowerCase();
      return (
        haystack.includes("deepseek") &&
        haystack.includes("v4") &&
        haystack.includes("pro")
      );
    });
  const deepseek =
    namedV4Pro ??
    models.find((model) => model.provider?.toLowerCase() === "deepseek") ??
    models.find((model) =>
      `${model.id} ${model.label}`.toLowerCase().includes("deepseek"),
    );
  return (deepseek ?? models[0])?.id ?? "";
}
