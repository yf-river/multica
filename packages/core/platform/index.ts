export { CoreProvider } from "./core-provider";
export type { CoreProviderProps, ClientIdentity } from "./types";
export { AuthInitializer } from "./auth-initializer";
export { defaultStorage, defaultSessionStorage } from "./storage";
export { createPersistStorage } from "./persist-storage";
export {
  createWorkspaceAwareStorage,
  setCurrentWorkspace,
  getCurrentSlug,
  getCurrentWsId,
  subscribeToCurrentSlug,
  registerWorkspaceStoreLifecycle,
  registerWorkspacePersistStore,
  registerAccountStateReset,
  registerAccountPersistStore,
  resetAccountState,
} from "./workspace-storage";
export { clearAccountStorage, clearWorkspaceStorage } from "./storage-cleanup";
export { isMac, modKey, enterKey, formatShortcut } from "./keyboard";
export {
  registerSystemNotificationClickHandler,
  isWebNotificationSupported,
  getWebNotificationPermission,
  requestWebNotificationPermission,
  showWebNotification,
  type SystemNotificationPayload,
  type WebNotificationPermission,
} from "./system-notification";
