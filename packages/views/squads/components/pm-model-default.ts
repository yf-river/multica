import type { RuntimeModel } from "@multica/core/types";

export function preferredPMModel(models: RuntimeModel[]) {
  const deepseek =
    models.find((model) => model.provider?.toLowerCase() === "deepseek") ??
    models.find((model) =>
      `${model.id} ${model.label}`.toLowerCase().includes("deepseek"),
    );
  return (deepseek ?? models[0])?.id ?? "";
}
