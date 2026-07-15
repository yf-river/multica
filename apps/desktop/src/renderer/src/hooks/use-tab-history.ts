import { useCallback, useEffect, useRef } from "react";
import type { DataRouter } from "react-router-dom";
import {
  resolveRouteIcon,
  useActiveTabHistory,
  useActiveTabRouter,
  useTabStore,
} from "@/stores/tab-store";

/**
 * Shared hint map so useTabRouterSync can distinguish back vs forward POP.
 * Set before calling router.navigate(-1 | 1), read in the synchronous subscription.
 */
const popDirectionHints = new Map<DataRouter, "back" | "forward">();

/**
 * Per-tab back/forward navigation derived from the active workspace's
 * active tab.
 *
 * Subscribed via primitive selectors so this hook only re-renders when
 * the numeric history state actually changes — path ticks on the active
 * tab (which don't shift historyIndex) don't churn the back/forward
 * buttons.
 */
export function useTabHistory() {
  const router = useActiveTabRouter();
  const { historyIndex, historyLength } = useActiveTabHistory();

  const canGoBack = historyIndex > 0;
  const canGoForward = historyIndex < historyLength - 1;

  const goBack = useCallback(() => {
    if (!router || historyIndex <= 0) return;
    popDirectionHints.set(router, "back");
    router.navigate(-1);
  }, [router, historyIndex]);

  const goForward = useCallback(() => {
    if (!router || historyIndex >= historyLength - 1) return;
    popDirectionHints.set(router, "forward");
    router.navigate(1);
  }, [router, historyIndex, historyLength]);

  return { canGoBack, canGoForward, goBack, goForward };
}

export function useTabRouterSync(tabId: string, router: DataRouter) {
  const indexRef = useRef(0);
  const lengthRef = useRef(1);

  useEffect(() => {
    const initialPath = tabPathFromLocation(router.state.location);
    useTabStore.getState().updateTab(tabId, {
      path: initialPath,
      icon: resolveRouteIcon(router.state.location.pathname),
    });

    return router.subscribe((state) => {
      const path = tabPathFromLocation(state.location);
      if (state.historyAction === "PUSH") {
        indexRef.current += 1;
        lengthRef.current = indexRef.current + 1;
      } else if (state.historyAction === "POP") {
        const hint = popDirectionHints.get(router);
        popDirectionHints.delete(router);
        indexRef.current =
          hint === "forward"
            ? Math.min(indexRef.current + 1, lengthRef.current - 1)
            : Math.max(0, indexRef.current - 1);
      }

      const store = useTabStore.getState();
      store.updateTab(tabId, {
        path,
        icon: resolveRouteIcon(state.location.pathname),
      });
      store.updateTabHistory(tabId, indexRef.current, lengthRef.current);
    });
  }, [tabId, router]);
}

function tabPathFromLocation(location: {
  pathname: string;
  search?: string;
  hash?: string;
}) {
  return `${location.pathname}${location.search ?? ""}${location.hash ?? ""}`;
}
