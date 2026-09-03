"use client";

import { useCallback } from "react";
import type { LinkClickIntent } from "./click-intent";
import { useNavigation } from "./context";

/**
 * Imperative sibling of AppLink / useRowLink for surfaces that receive the
 * mouse event through a callback (DataTable rows, command palette items)
 * instead of owning an anchor element: executes a resolved click intent
 * against the navigation adapter.
 *
 * Tab intents use `window.open` on the shareable URL. JavaScript cannot force
 * a background browser tab, so both tab intents use the same browser API.
 */
export function useIntentNavigate() {
  const navigation = useNavigation();

  return useCallback(
    (href: string, intent: LinkClickIntent) => {
      const { push, getShareableUrl } = navigation;
      if (intent === "push") {
        push(href);
        return;
      }
      window.open(getShareableUrl(href), "_blank", "noopener,noreferrer");
    },
    [navigation],
  );
}
