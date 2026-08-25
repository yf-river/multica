const INTERNAL_NAVIGATION_EVENT = "multica:navigate";

interface InternalNavigationDetail {
  path: string;
}

export function isInternalAppPath(path: string): boolean {
  return path.startsWith("/") && !path.startsWith("//");
}

export function dispatchInternalNavigation(path: string): boolean {
  if (!isInternalAppPath(path)) return false;

  window.dispatchEvent(
    new CustomEvent<InternalNavigationDetail>(INTERNAL_NAVIGATION_EVENT, {
      detail: { path },
    }),
  );
  return true;
}

export function subscribeInternalNavigation(
  navigate: (path: string) => void,
): () => void {
  const listener = (event: Event) => {
    const path = (event as CustomEvent<unknown>).detail;
    if (!isInternalNavigationDetail(path)) return;
    navigate(path.path);
  };

  window.addEventListener(INTERNAL_NAVIGATION_EVENT, listener);
  return () => window.removeEventListener(INTERNAL_NAVIGATION_EVENT, listener);
}

function isInternalNavigationDetail(
  value: unknown,
): value is InternalNavigationDetail {
  if (!value || typeof value !== "object") return false;
  const path = (value as { path?: unknown }).path;
  return typeof path === "string" && isInternalAppPath(path);
}

export { INTERNAL_NAVIGATION_EVENT };
