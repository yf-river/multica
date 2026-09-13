"use client";

/**
 * 官网「更新日志（Changelog）」页（客户端组件）：把 i18n 字典里的发布条目
 * 按版本号分组渲染为时间线，左侧提供按月份分组的粘性目录，滚动时自动高亮当前版本，
 * 点击目录则平滑滚到对应版本段。
 */
import {
  type MouseEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { LandingHeader } from "./landing-header";
import { LandingFooter } from "./landing-footer";
import { useLocale } from "../i18n";
import type { Locale } from "../i18n/types";

/** 拆解后的日期（年/月/日，来自 `YYYY-MM-DD`）。 */
type ParsedDate = { year: number; month: number; day: number };

/** 把 `YYYY-MM-DD` 字符串拆成数字年月日。 */
function parseDate(dateStr: string): ParsedDate {
  const parts = dateStr.split("-");
  return {
    year: Number(parts[0]),
    month: Number(parts[1]),
    day: Number(parts[2]),
  };
}

/** 用 UTC 构造日期并回验三段数值，防止非法日期（如 2 月 30 日）被静默进位成合法日期。 */
function utcDate(year: number, month: number, day: number) {
  const date = new Date(Date.UTC(year, month - 1, day));
  if (
    date.getUTCFullYear() !== year ||
    date.getUTCMonth() !== month - 1 ||
    date.getUTCDate() !== day
  ) {
    return null;
  }
  return date;
}

/**
 * 把「年 + 月」格式化为本地化月份标签（如 2026 年 6 月），用于目录分组标题。
 *
 * @param year - 年份。
 * @param month - 月份（1~12）。
 * @param locale - 目标语言。
 * @returns 本地化后的「月份 + 年份」文本。
 */
export function monthYearLabel(year: number, month: number, locale: Locale) {
  const date = utcDate(year, month, 1);
  if (!date) return "";
  return new Intl.DateTimeFormat(locale, {
    month: "long",
    timeZone: "UTC",
    year: "numeric",
  }).format(date);
}

/**
 * 把 `YYYY-MM-DD` 格式化为本地化完整日期；非法日期则原样返回字符串兜底。
 *
 * @param dateStr - 日期字符串。
 * @param locale - 目标语言。
 * @returns 本地化日期文本。
 */
export function fullDateLabel(dateStr: string, locale: Locale) {
  const { year, month, day } = parseDate(dateStr);
  const date = utcDate(year, month, day);
  if (!date) return dateStr;
  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "long",
    timeZone: "UTC",
    year: "numeric",
  }).format(date);
}

/** 单个发布版本：版本号、日期、标题与各类变更条目。 */
type Release = {
  version: string;
  date: string;
  title: string;
  changes: string[];
  features?: string[];
  improvements?: string[];
  fixes?: string[];
};

/** 同一自然月内的版本分组（目录按它聚合）。 */
type MonthGroup = {
  key: string;
  year: number;
  month: number;
  entries: Release[];
};

/** 按月份顺序把发布条目聚成组（输入需已按日期升序）。 */
function groupByMonth(entries: readonly Release[]): MonthGroup[] {
  const groups: MonthGroup[] = [];
  for (const entry of entries) {
    const { year, month } = parseDate(entry.date);
    const key = `${year}-${month}`;
    const last = groups[groups.length - 1];
    if (last && last.key === key) {
      last.entries.push(entry);
    } else {
      groups.push({ key, year, month, entries: [entry] });
    }
  }
  return groups;
}

/** 版本号生成 DOM 锚点 id：`1.2.3` → `release-1-2-3`。 */
function anchorId(version: string) {
  return `release-${version.replace(/\./g, "-")}`;
}

/** 带项目符号的变更条目列表。 */
function ChangeList({ items }: { items: string[] }) {
  return (
    <ul className="mt-2 space-y-2">
      {items.map((change) => (
        <li
          key={change}
          className="flex items-start gap-2.5 text-body leading-[1.7] text-[#0a0d12]/60 sm:text-body-lg"
        >
          <span className="mt-2.5 h-1 w-1 shrink-0 rounded-full bg-[#0a0d12]/30" />
          {change}
        </li>
      ))}
    </ul>
  );
}

/**
 * 更新日志页内容主体（导出给 page.tsx 使用）。
 *
 * 用 IntersectionObserver 监测各版本段在视口的位置，据此驱动左侧目录的「当前版本」高亮；
 * 点击目录跳转期间会短暂锁住该联动，避免经过中间条目时高亮闪烁。
 *
 * @returns 带官网导航与页脚的整页布局。
 */
export function ChangelogPageClient() {
  const { t, locale } = useLocale();
  const categoryLabels = t.changelog.categories;
  const entries = t.changelog.entries;
  const groups = useMemo(() => groupByMonth(entries), [entries]);

  const [activeVersion, setActiveVersion] = useState<string>(
    entries[0]?.version ?? ""
  );
  const navLockRef = useRef<number | null>(null);

  useEffect(() => {
    if (entries.length === 0) return;
    const visible = new Set<string>();

    const observer = new IntersectionObserver(
      (observed) => {
        observed.forEach((e) => {
          const v = (e.target as HTMLElement).dataset.version;
          if (!v) return;
          if (e.isIntersecting) visible.add(v);
          else visible.delete(v);
        });
        // 程序化滚动到点击目标期间忽略观察器回调，否则高亮会随着滚过
        // 的每个条目来回闪烁。
        if (navLockRef.current !== null) return;

        const firstVisible = entries.find((r) => visible.has(r.version));
        if (firstVisible) {
          setActiveVersion(firstVisible.version);
          return;
        }
        const scrollY = window.scrollY;
        let best = entries[0]?.version ?? "";
        for (const r of entries) {
          const el = document.getElementById(anchorId(r.version));
          if (!el) continue;
          if (el.getBoundingClientRect().top + scrollY <= scrollY + 160) {
            best = r.version;
          }
        }
        setActiveVersion(best);
      },
      { rootMargin: "-20% 0px -70% 0px", threshold: 0 }
    );

    entries.forEach((r) => {
      const el = document.getElementById(anchorId(r.version));
      if (el) observer.observe(el);
    });
    return () => observer.disconnect();
  }, [entries]);

  const jumpTo =
    (version: string) => (e: MouseEvent<HTMLAnchorElement>) => {
      const el = document.getElementById(anchorId(version));
      if (!el) return;
      e.preventDefault();
      el.scrollIntoView({ behavior: "smooth", block: "start" });
      window.history.replaceState(null, "", `#${anchorId(version)}`);
      setActiveVersion(version);
      if (navLockRef.current !== null) {
        window.clearTimeout(navLockRef.current);
      }
      navLockRef.current = window.setTimeout(() => {
        navLockRef.current = null;
      }, 800);
    };

  return (
    <>
      <LandingHeader variant="light" />
      <main className="bg-white text-[#0a0d12]">
        <div className="mx-auto max-w-[1080px] px-4 py-16 sm:px-6 sm:py-20 lg:py-24">
          <div className="lg:grid lg:grid-cols-[200px_minmax(0,1fr)] lg:gap-16">
            <aside className="hidden lg:block">
              <nav
                aria-label={t.changelog.toc}
                className="sticky top-28 max-h-[calc(100vh-8rem)] overflow-y-auto pb-8 pr-2"
              >
                <h3 className="text-micro font-semibold uppercase tracking-[0.14em] text-[#0a0d12]/50">
                  {t.changelog.toc}
                </h3>

                <div className="relative mt-5">
                  <span
                    aria-hidden="true"
                    className="pointer-events-none absolute left-[4px] top-7 bottom-2 w-px bg-[#0a0d12]/10"
                  />

                  <ol className="space-y-5">
                    {groups.map((group) => (
                      <li key={group.key}>
                        <p className="ml-6 text-micro font-semibold uppercase tracking-[0.12em] text-[#0a0d12]/45">
                          {monthYearLabel(group.year, group.month, locale)}
                        </p>

                        <ol className="mt-1.5">
                          {group.entries.map((release) => {
                            const isActive =
                              release.version === activeVersion;
                            const { day } = parseDate(release.date);
                            return (
                              <li key={release.version}>
                                <a
                                  href={`#${anchorId(release.version)}`}
                                  onClick={jumpTo(release.version)}
                                  aria-current={isActive ? "true" : undefined}
                                  className={[
                                    "group relative flex items-center gap-3 rounded-md py-1 pr-2 text-label transition-colors",
                                    isActive
                                      ? "text-[#0a0d12]"
                                      : "text-[#0a0d12]/55 hover:text-[#0a0d12]/80",
                                  ].join(" ")}
                                >
                                  <span
                                    aria-hidden="true"
                                    className={[
                                      "relative z-10 block size-[9px] shrink-0 rounded-full border transition-all duration-200",
                                      isActive
                                        ? "border-[#0a0d12] bg-[#0a0d12] ring-4 ring-[#0a0d12]/8"
                                        : "border-[#0a0d12]/25 bg-white group-hover:border-[#0a0d12]/60",
                                    ].join(" ")}
                                  />
                                  <span
                                    className={[
                                      "w-[1.25rem] shrink-0 text-right tabular-nums",
                                      isActive
                                        ? "font-semibold"
                                        : "font-medium",
                                    ].join(" ")}
                                  >
                                    {day}
                                  </span>
                                  <span className="tabular-nums text-micro text-[#0a0d12]/35">
                                    v{release.version}
                                  </span>
                                </a>
                              </li>
                            );
                          })}
                        </ol>
                      </li>
                    ))}
                  </ol>
                </div>
              </nav>
            </aside>

            <div className="mx-auto min-w-0 max-w-[720px] lg:mx-0">
              <h1 className="landing-serif text-[2.6rem] leading-[1.05] tracking-[-0.03em] sm:text-[3.4rem]">
                {t.changelog.title}
              </h1>
              <p className="mt-4 text-body-lg leading-7 text-[#0a0d12]/60 sm:text-title-sm">
                {t.changelog.subtitle}
              </p>

              <div className="mt-16 space-y-16">
                {entries.map((release) => {
                  const hasCategorized =
                    release.features || release.improvements || release.fixes;
                  return (
                    <section
                      key={release.version}
                      id={anchorId(release.version)}
                      data-version={release.version}
                      className="relative scroll-mt-28"
                    >
                      <div className="flex items-baseline gap-3">
                        <span className="text-label font-semibold tabular-nums">
                          v{release.version}
                        </span>
                        <span className="text-label text-[#0a0d12]/40">
                          {fullDateLabel(release.date, locale)}
                        </span>
                      </div>
                      <h2 className="mt-2 text-title-lg font-semibold leading-snug sm:text-display-sm">
                        {release.title}
                      </h2>

                      {hasCategorized ? (
                        <div className="mt-4 space-y-5">
                          {release.features && release.features.length > 0 && (
                            <div>
                              <h3 className="text-label font-semibold uppercase tracking-wide text-[#0a0d12]/50">
                                {categoryLabels.features}
                              </h3>
                              <ChangeList items={release.features} />
                            </div>
                          )}
                          {release.improvements &&
                            release.improvements.length > 0 && (
                              <div>
                                <h3 className="text-label font-semibold uppercase tracking-wide text-[#0a0d12]/50">
                                  {categoryLabels.improvements}
                                </h3>
                                <ChangeList items={release.improvements} />
                              </div>
                            )}
                          {release.fixes && release.fixes.length > 0 && (
                            <div>
                              <h3 className="text-label font-semibold uppercase tracking-wide text-[#0a0d12]/50">
                                {categoryLabels.fixes}
                              </h3>
                              <ChangeList items={release.fixes} />
                            </div>
                          )}
                        </div>
                      ) : (
                        <ChangeList items={release.changes} />
                      )}
                    </section>
                  );
                })}
              </div>
            </div>
          </div>
        </div>
      </main>
      <LandingFooter />
    </>
  );
}
