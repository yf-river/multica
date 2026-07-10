export const promptLibraryKeys = {
  list: (workspaceId: string) => ["prompt-library", workspaceId, "list"] as const,
  versions: (workspaceId: string, promptId: string | null) =>
    ["prompt-library", workspaceId, "versions", promptId ?? ""] as const,
  trials: (workspaceId: string, promptId: string | null) =>
    ["prompt-library", workspaceId, "trials", promptId ?? ""] as const,
  agents: (workspaceId: string) => ["prompt-library", workspaceId, "agents"] as const,
  assets: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-assets"] as const,
  datasetVersions: (workspaceId: string, assetId: string) =>
    ["prompt-library", workspaceId, "evaluation-dataset-versions", assetId] as const,
  cases: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-cases"] as const,
  runs: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-runs"] as const,
  runEvidence: (workspaceId: string, runId: string | null) =>
    ["prompt-library", workspaceId, "run-evidence", runId ?? ""] as const,
  runEvidenceSnapshots: (workspaceId: string, runId: string | null) =>
    ["prompt-library", workspaceId, "run-evidence-snapshots", runId ?? ""] as const,
  candidates: (workspaceId: string) => ["prompt-library", workspaceId, "optimization-candidates"] as const,
  runCandidates: (workspaceId: string, runId: string) =>
    ["prompt-library", workspaceId, "optimization-candidates", "run", runId] as const,
};
