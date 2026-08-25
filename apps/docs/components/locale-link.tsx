import Link from "next/link";
import type { AnchorHTMLAttributes } from "react";

// Keep MDX links on Next's client-side navigation path. Public docs URLs are
// prefix-free because the current docs site has one language.
export function LocaleLink({
  href,
  ...rest
}: AnchorHTMLAttributes<HTMLAnchorElement> & { href?: string }) {
  if (!href) return <a {...rest} />;
  return <Link href={href} {...rest} />;
}
