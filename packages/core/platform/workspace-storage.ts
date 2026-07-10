import type { PersistOptions, PersistStorage } from "zustand/middleware";
import type { StoreApi } from "zustand/vanilla";
import type { StorageAdapter } from "../types/storage";

// Paired module vars — always set/cleared together by the workspace layout.
// _currentSlug is the primary identifier (matches the URL segment).
// _currentWsId is derived (from the React Query workspace list) and used for
// query keys and path-embedded API calls where UUID is required.
let _currentSlug: string | null = null;
let _currentWsId: string | null = null;

interface WorkspaceStoreLifecycle {
  rehydrate: () => void;
  reset: () => void;
}

interface PersistedClientStore<State, PersistedState> extends StoreApi<State> {
  persist: {
    rehydrate: () => Promise<void> | void;
    getOptions: () => Partial<PersistOptions<State, PersistedState>>;
    setOptions: (
      options: Partial<PersistOptions<State, PersistedState>>,
    ) => void;
  };
}

const _workspaceStores = new Set<WorkspaceStoreLifecycle>();
const _accountStateResetters = new Set<() => void>();
const _slugSubscribers = new Set<(slug: string | null) => void>();
let _pendingNotify = false;
let _pendingRehydrate = false;

/**
 * Update the current workspace identity. This is the single source of truth
 * for "which workspace is active"; everything downstream (WS connection,
 * persist namespace, cache-key derivation) follows from here.
 *
 * If the slug actually changed, two side effects fire:
 *   1. Subscribers are notified (e.g. WSProvider reconnects).
 *   2. All registered persist stores rehydrate from the new slug's namespace.
 *
 * Both side effects are idempotent on slug-equality: repeat calls with the
 * same slug are a pure no-op. This matters on desktop, where N tabs each
 * mount their own WorkspaceRouteLayout and each one naively tries to sync;
 * only the first call for a given slug does real work.
 *
 * Both side effects are deferred to a microtask because zustand persist
 * rehydrate + subscriber notifications both end up calling setState(), and
 * React 19 forbids "cross-component updates during render".
 */
export function setCurrentWorkspace(slug: string | null, wsId: string | null) {
  if (_currentSlug === slug) {
    // Slug unchanged: nothing to rehydrate, nothing to notify. Accept a
    // (possibly) updated wsId for consumers that read the UUID mirror.
    _currentWsId = wsId;
    return;
  }
  _currentSlug = slug;
  _currentWsId = wsId;

  if (!_pendingNotify) {
    _pendingNotify = true;
    queueMicrotask(() => {
      _pendingNotify = false;
      const current = _currentSlug;
      for (const fn of _slugSubscribers) {
        fn(current);
      }
    });
  }

  if (!_pendingRehydrate) {
    _pendingRehydrate = true;
    queueMicrotask(() => {
      _pendingRehydrate = false;
      for (const store of _workspaceStores) {
        store.reset();
        store.rehydrate();
      }
    });
  }
}

/** Current workspace slug (from URL). */
export function getCurrentSlug(): string | null {
  return _currentSlug;
}

/** Current workspace UUID (derived from slug + workspace list cache). */
export function getCurrentWsId(): string | null {
  return _currentWsId;
}

/**
 * Subscribe to changes of the current workspace slug. Returns an unsubscribe
 * function. Designed for React's `useSyncExternalStore` (WSProvider reconnect).
 */
export function subscribeToCurrentSlug(
  fn: (slug: string | null) => void,
): () => void {
  _slugSubscribers.add(fn);
  return () => {
    _slugSubscribers.delete(fn);
  };
}

/** Register state owned by the active workspace. */
export function registerWorkspaceStoreLifecycle(
  lifecycle: WorkspaceStoreLifecycle,
): () => void {
  _workspaceStores.add(lifecycle);
  _accountStateResetters.add(lifecycle.reset);
  return () => {
    _workspaceStores.delete(lifecycle);
    _accountStateResetters.delete(lifecycle.reset);
  };
}

/** Register a Zustand persist store without letting lifecycle resets write. */
export function registerWorkspacePersistStore<State, PersistedState>(
  store: PersistedClientStore<State, PersistedState>,
): () => void {
  return registerWorkspaceStoreLifecycle({
    rehydrate: () => store.persist.rehydrate(),
    reset: () => resetPersistedStore(store),
  });
}

/** Register non-workspace state that still belongs to the signed-in account. */
export function registerAccountStateReset(reset: () => void): () => void {
  _accountStateResetters.add(reset);
  return () => _accountStateResetters.delete(reset);
}

/** Register a global persist store whose contents belong to the account. */
export function registerAccountPersistStore<State, PersistedState>(
  store: PersistedClientStore<State, PersistedState>,
): () => void {
  return registerAccountStateReset(() => resetPersistedStore(store));
}

/** Reset every loaded client store that can contain account-owned data. */
export function resetAccountState(): void {
  for (const reset of _accountStateResetters) reset();
}

function resetPersistedStore<State, PersistedState>(
  store: PersistedClientStore<State, PersistedState>,
): void {
  const storage = store.persist.getOptions().storage;
  if (!storage) {
    store.setState(store.getInitialState(), true);
    return;
  }

  // Zustand setState normally writes through persist. Use a discard storage
  // for the synchronous reset so switching to workspace B cannot overwrite
  // B's saved state before rehydration reads it.
  const discardStorage: PersistStorage<PersistedState> = {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
  };
  store.persist.setOptions({ storage: discardStorage });
  try {
    store.setState(store.getInitialState(), true);
  } finally {
    store.persist.setOptions({ storage });
  }
}

/**
 * Storage that automatically namespaces keys with the current workspace slug.
 * Reads _currentSlug at call time, so it follows workspace switches dynamically.
 *
 * When no workspace is active (e.g. zustand persist's initial hydration before
 * WorkspaceRouteLayout has called setCurrentWorkspace, or a setter firing from
 * a child component's mount-effect before the parent layout's effect has run),
 * reads return null and writes are dropped — explicitly NOT a fallback to the
 * un-namespaced bare key, which used to leak workspace-scoped data across
 * workspaces. Persisted stores get a real read once setCurrentWorkspace
 * triggers their registered rehydrate fn.
 */
export function createWorkspaceAwareStorage(adapter: StorageAdapter): StorageAdapter {
  return {
    getItem: (key) =>
      _currentSlug ? adapter.getItem(`${key}:${_currentSlug}`) : null,
    setItem: (key, value) => {
      if (_currentSlug) adapter.setItem(`${key}:${_currentSlug}`, value);
    },
    removeItem: (key) => {
      if (_currentSlug) adapter.removeItem(`${key}:${_currentSlug}`);
    },
  };
}
