export { paths, isGlobalPath, LIFE_TABS } from "./paths";
export type { LifeTab, WorkspacePaths } from "./paths";
export { RESERVED_SLUGS, isReservedSlug } from "./reserved-slugs";
export { resolvePostAuthDestination } from "./resolve";
export {
  WorkspaceSlugProvider,
  useWorkspaceSlug,
  useRequiredWorkspaceSlug,
  useCurrentWorkspace,
  useWorkspaceId,
  useWorkspacePaths,
} from "./hooks";
