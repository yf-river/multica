export function trainingSelectedPromptStorageKey(workspaceId: string | null | undefined) {
  return workspaceId ? `multica:training:selected-prompt:${workspaceId}` : null;
}
