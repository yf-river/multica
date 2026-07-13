/**
 * Shared link handling utilities for the editor system.
 *
 * Used by content-editor (ProseMirror click handler), readonly-content
 * (react-markdown link component), and link-hover-card (Open button).
 */

import {
  dispatchInternalNavigation,
  isInternalAppPath,
} from "../../navigation/internal-navigation";

/**
 * Open a link — internal paths dispatch multica:navigate, external open new tab.
 */
export function openLink(href: string): void {
  if (isInternalAppPath(href)) {
    dispatchInternalNavigation(href);
    return;
  }

  if (isSafeExternalHref(href)) {
    window.open(href, "_blank", "noopener,noreferrer");
  }
}

function isSafeExternalHref(href: string): boolean {
  if (href.startsWith("//")) return true;

  try {
    const protocol = new URL(href).protocol;
    return ["http:", "https:", "mailto:", "tel:"].includes(protocol);
  } catch {
    return false;
  }
}

/** Check if a href is a mention protocol link (should not be opened as a regular link). */
export function isMentionHref(href: string | null | undefined): href is string {
  return !!href && href.startsWith("mention://");
}
