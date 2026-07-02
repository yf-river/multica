import type { RuntimeDevice, SquadScope } from "@multica/core/types";

const DEFAULT_PM_PROVIDER = "codebuddy";

export function isRuntimeCompatibleWithPMScope(
  runtime: RuntimeDevice,
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

export function providerSortRank(provider: string) {
  const p = provider.toLowerCase();
  if (p === DEFAULT_PM_PROVIDER) return 0;
  if (p === "codex") return 1;
  return 2;
}

export function pmProviderChoices(
  runtimes: RuntimeDevice[],
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
  runtimes: RuntimeDevice[],
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
