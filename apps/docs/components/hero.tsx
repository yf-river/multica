import type { ReactNode } from "react";

/**
 * DocsHero — editorial showpiece header for landing-style pages.
 *
 * Escapes prose scope to run its own type scale. Title accepts ReactNode so
 * callers can pass <em> spans for brand-color emphasis (italic is avoided —
 * Chinese italic is a synthetic slant and reads as broken).
 */
export function DocsHero({
  eyebrow,
  title,
  subtitle,
}: {
  eyebrow?: string;
  title: ReactNode;
  subtitle?: ReactNode;
}) {
  return (
    <section className="not-prose mb-7 pt-2">
      {eyebrow ? (
        <p className="mb-5 text-[0.6875rem] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
          {eyebrow}
        </p>
      ) : null}
      <h1 className="mb-5 font-[family-name:var(--font-serif)] text-[2.25rem] font-normal leading-[1.05] tracking-[-0.025em] text-foreground sm:text-[2.75rem]">
        {title}
      </h1>
      {subtitle ? (
        <p className="max-w-[36rem] font-[family-name:var(--font-serif)] text-[1.25rem] leading-[1.5] tracking-[-0.005em] text-[oklch(from_var(--foreground)_calc(l+0.06)_c_h)]">
          {subtitle}
        </p>
      ) : null}
    </section>
  );
}
