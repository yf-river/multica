import { preprocessLinks, preprocessFileCards } from "@multica/ui/markdown";

/**
 * Preprocess a markdown string before loading into Tiptap via contentType: 'markdown'.
 *
 * This is the ONLY transform applied before @tiptap/markdown parses the content.
 * Two string→string transforms on raw Markdown:
 * 1. Raw URLs → markdown links via linkify-it (so they render as clickable Link nodes)
 * 2. File card syntax (`!file[name](url)`) → HTML div for
 *    fileCard node parsing
 */
export function preprocessMarkdown(markdown: string): string {
  if (!markdown) return "";
  const withLinks = preprocessLinks(markdown);
  return preprocessFileCards(withLinks);
}
