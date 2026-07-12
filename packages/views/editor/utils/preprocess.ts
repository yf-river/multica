import { preprocessLinks, preprocessFileCards } from "@multica/ui/markdown";
import { configStore } from "@multica/core/config";

/**
 * Preprocess a markdown string before loading into Tiptap via contentType: 'markdown'.
 *
 * This is the ONLY transform applied before @tiptap/markdown parses the content.
 * It does NOT convert to HTML — that was the old markdownToHtml.ts pipeline which
 * was deleted in the April 2026 refactor.
 *
 * Two string→string transforms on raw Markdown:
 * 1. Raw URLs → markdown links via linkify-it (so they render as clickable Link nodes)
 * 2. File card syntax (new !file[name](url) + legacy [name](cdnUrl)) → HTML div for
 *    fileCard node parsing
 */
export function preprocessMarkdown(markdown: string): string {
  if (!markdown) return "";
  const cdnDomain = configStore.getState().cdnDomain;
  const withLinks = preprocessLinks(markdown);
  return preprocessFileCards(withLinks, cdnDomain);
}
