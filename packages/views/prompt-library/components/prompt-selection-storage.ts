export function trainingSelectedPromptStorageKey(workspaceId: string | null | undefined) {
  return workspaceId ? `multica:training:selected-prompt:${workspaceId}` : null;
}

export function trainingPlaygroundSelectedPromptStorageKey(
  surface: "prompt-playground" | "agent-playground",
  workspaceId: string | null | undefined,
) {
  return workspaceId ? `multica:training:${surface}:selected-prompt:${workspaceId}` : null;
}

export function legacyTrainingSelectedPromptStorageKeys(workspaceId: string | null | undefined) {
  if (!workspaceId) return [];
  return [
    `multica:training:prompt-library:selected-prompt:${workspaceId}`,
    `multica:training:prompt-playground:selected-prompt:${workspaceId}`,
    `multica:training:agent-playground:selected-prompt:${workspaceId}`,
  ];
}
