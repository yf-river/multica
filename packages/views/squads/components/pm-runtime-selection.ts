import type { AgentRuntime, RuntimeModel, SquadScope } from "@multica/core/types";

export const PM_DEFAULT_PROVIDER = "codebuddy";
const PREFERRED_DEEPSEEK_MODEL_IDS = ["deepseek-v4-pro-ioa", "deepseek-v4-pro"];

function isRuntimeCompatibleWithPMScope(
  runtime: AgentRuntime,
  targetScope: SquadScope,
  currentUserId: string | null,
) {
  if (targetScope === "workspace") {
    return runtime.scope === "workspace";
  }
  return (
    runtime.scope === "personal" &&
    !!currentUserId &&
    runtime.owner_id === currentUserId
  );
}

export function preferredPMModel(models: RuntimeModel[]) {
  const exactPreferred = PREFERRED_DEEPSEEK_MODEL_IDS
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

function providerSortRank(provider: string) {
  const p = provider.toLowerCase();
  if (p === PM_DEFAULT_PROVIDER) return 0;
  if (p === "codex") return 1;
  return 2;
}

export function pmProviderChoices(
  runtimes: AgentRuntime[],
  targetScope: SquadScope,
  currentUserId: string | null,
) {
  const providers = Array.from(
    new Set(
      runtimes
        .filter((runtime) =>
          isRuntimeCompatibleWithPMScope(runtime, targetScope, currentUserId),
        )
        .map((runtime) => runtime.provider.trim().toLowerCase())
        .filter(Boolean),
    ),
  );
  providers.sort((a, b) => {
    const rank = providerSortRank(a) - providerSortRank(b);
    return rank || a.localeCompare(b);
  });
  return providers;
}

export function bestRuntimeForPMProvider(
  runtimes: AgentRuntime[],
  provider: string,
  targetScope: SquadScope,
  currentUserId: string | null,
) {
  const candidates = runtimes.filter(
    (runtime) =>
      runtime.status === "online" &&
      runtime.provider.toLowerCase() === provider.toLowerCase() &&
      isRuntimeCompatibleWithPMScope(runtime, targetScope, currentUserId),
  );
  candidates.sort(
    (a, b) =>
      Date.parse(b.last_seen_at ?? "") - Date.parse(a.last_seen_at ?? ""),
  );
  return candidates[0] ?? null;
}
