// Keep a small link normalizer for MDX-rendered anchors. The docs are
// Chinese-only, so root-relative internal links can remain prefix-free.
//
// We deliberately do NOT touch:
//   - external links (`https:`, `mailto:`, `tel:`, etc.)
//   - in-page anchors (`#section`)
//   - relative paths (`./foo`, `../bar`)
export function prefixLocale(href: string, _lang: string): string {
  if (!href) return href;
  return href;
}
