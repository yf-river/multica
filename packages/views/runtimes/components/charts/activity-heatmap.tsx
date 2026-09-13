// ============================================================================
// 活动热力图（GitHub 风格的成本热力网格 + 洞察统计条）
//
// 角色定位：把「近 26 周每天的估算成本」画成按周分列的色块日历（像 GitHub
// 的贡献热力图），颜色深浅代表当天花钱多少；下方再跟一条 4 格的统计条，
// 展示「最忙的一天 / 最活跃的周几 / 最安静的周几 / 窗口总成本」。
//
// 数据流：usage（日粒度 RuntimeUsage 行）→ 按天累加 estimateCost 金额 → 把
// 每天金额映射成 0~4 档颜色（0=无活动）→ 生成单元格网格（跨 26 周、按当前
// 周截断不画未来）→ 从同一批单元格推导洞察，保证色阶、最忙格与侧栏数字
// 永远一致。
//
// 关键约定：日期一律用 YYYY-MM-DD 字符串在查看者 tz 下运算，宿主浏览器
// 时区不会把列错位；星期顺序周一开头，与周聚合 (#MUL-2382) 及 ISO 8601 一致。
// ============================================================================
import { useMemo } from "react";
import type { RuntimeUsage } from "@multica/core/types";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { addDaysIso, estimateCost, formatUsd, todayIso, weekStartIso } from "../../utils";
import { useLocale, useT } from "../../../i18n";

// 26 周（约半年）让热力图在较宽的图表卡片里有真正的存在感，「长周期」才
// 能成为一个有意义的页签——13 周的一条细带看起来太局促。格子用 16px
// （GitHub 是 11px），保持日历格密度在这个尺寸下清晰可读。
const HEATMAP_WEEKS = 26;
const CELL_SIZE = 16;
const CELL_GAP = 3;
// 行标签只标第 0/2/4 行（周一/周三/周五），保持密度可读，同时不把图表结构
// 绑死在某一语言上。周一在首行，与 ISO 8601 及周聚合（见 #MUL-2382）一致。
const LABELED_WEEKDAY_INDICES = new Set([0, 2, 4]);
// 2026-01-05 是周一，从这里以 7 天为步长前进，能让格式化出来的星期名与
// 「周一起始」的 dayOfWeek 下标对齐。
const WEEKDAY_ANCHOR = Date.UTC(2026, 0, 5);

// 单元格沿用品牌 chart-1 色相、以递减透明度表达档次，而不是用中性前景色做
// 淡出——这样热力图和「每日成本」（chart-1 堆叠）属于同一个视觉家族，而
// 不是一块孤立去色的平板。0 档保持中性的弱色，明确表达「没有活动」（而
// 不是「有很淡的活动」）。
function getHeatmapColor(level: number): string {
  if (level === 0) return "var(--color-muted)";
  const opacities = ["20%", "45%", "70%", "100%"];
  return `color-mix(in oklch, var(--color-chart-1) ${opacities[level - 1]}, transparent)`;
}

// fmtDate 把 YYYY-MM-DD 格式化成短日期文案（如 "5/12"），用本地化 locale。
function fmtDate(iso: string, locale: string): string {
  return new Date(iso + "T00:00:00").toLocaleString(locale, {
    month: "short",
    day: "numeric",
  });
}

// 星期行标签直接来自 Intl 而非翻译文案：我们支持的每个 locale，其短星期名
// 恰好就是我们要手译的那串字符串（下面的月份标签也是同法解析）。这样新增
// 一种 locale 时，就少一组需要同步维护的字符串。
function fmtWeekdays(locale: string): string[] {
  const fmt = new Intl.DateTimeFormat(locale, {
    weekday: "short",
    timeZone: "UTC",
  });
  return Array.from({ length: 7 }, (_, i) =>
    fmt.format(new Date(WEEKDAY_ANCHOR + i * 86_400_000)),
  );
}

// 从热力图单元格推导出的洞察数据（供下方统计条渲染）。所有数字都出自同一
// 批单元格，色阶、最忙格与统计条不可能互相矛盾。
interface Insights {
  /** 窗口内单日成本最高的那天（无任何花费时为 null）。 */
  busiestDay: { date: string; cost: number } | null;
  /** 平均成本最高的星期几下标（0=周一…6=周日），无花费时为 null。 */
  busyDayIndex: number | null;
  busyDayAvg: number;
  quietDayIndex: number | null;
  quietDayAvg: number;
  /** 窗口内总成本。 */
  totalCost: number;
  /** 实际绘制出的自然日格数（含进行中的本周）。 */
  windowDays: number;
}

/**
 * 活动热力图：26 周成本色块日历 + 底部洞察统计条。
 *
 * @param usage - 日粒度用量行（按天累加估算成本）。
 * @param tz - 查看者 IANA 时区，用于决定「今天」与周起始。
 */
export function ActivityHeatmap({
  usage,
  tz,
}: {
  usage: RuntimeUsage[];
  tz: string;
}) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  const weekdayLabels = useMemo(() => fmtWeekdays(locale), [locale]);
  // Memo 依赖——estimateCost（在下方函数体内被调用）会查用户自定义价格
  // 存储，所以用户保存一个自定义费率时，必须让格子失效重算。
  const pricings = useCustomPricingStore((s) => s.pricings);
  const { cells, monthLabels, insights } = useMemo(() => {
    // 按天累加估算成本。用成本（而非 token）给色阶赋予财务含义，与页面其余
    // 部分口径一致——这里的「热」格与每日成本图里的高柱说的是同一件事。
    const dateCost = new Map<string, number>();
    for (const u of usage) {
      dateCost.set(u.date, (dateCost.get(u.date) ?? 0) + estimateCost(u));
    }

    // 以查看者 tz 下「今天」所在周的周一为网格锚点，再往回推 HEATMAP_WEEKS-1
    // 周。所有日期都是 YYYY-MM-DD 字符串，宿主浏览器的时区无法让某一列错位。
    // 单元格画到 `today` 就停，使进行中的这一周只画已过去的天（「明天及以后」
    // 不渲染）——与周图表对「不完整周」的处理一致。
    const today = todayIso(tz);
    const lastWeekStart = weekStartIso(today);
    const startDate = addDaysIso(lastWeekStart, -(HEATMAP_WEEKS - 1) * 7);
    const todayIndex = (HEATMAP_WEEKS - 1) * 7 + ((() => {
      // 以周一为基准的 `today` 星期几：0=周一 … 6=周日。用字符串相减算出，
      // 宿主时区无法改变这个值。
      const [y, m, d] = today.split("-").map(Number);
      const dt = new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, d ?? 1));
      return (dt.getUTCDay() + 6) % 7;
    })());

    const allCells: {
      date: string;
      dayOfWeek: number; // 0 = 周一 … 6 = 周日
      week: number;
      cost: number;
    }[] = [];
    for (let i = 0; i <= todayIndex; i++) {
      const dateStr = addDaysIso(startDate, i);
      const dayOfWeek = i % 7;
      const week = Math.floor(i / 7);
      allCells.push({
        date: dateStr,
        dayOfWeek,
        week,
        cost: dateCost.get(dateStr) ?? 0,
      });
    }

    const nonZero = allCells.filter((c) => c.cost > 0).map((c) => c.cost);
    nonZero.sort((a, b) => a - b);
    const getLevel = (cost: number) => {
      if (cost === 0) return 0;
      if (nonZero.length <= 1) return 4;
      const p = nonZero.indexOf(cost) / (nonZero.length - 1);
      if (p <= 0.25) return 1;
      if (p <= 0.5) return 2;
      if (p <= 0.75) return 3;
      return 4;
    };

    const cellsWithLevel = allCells.map((c) => ({
      ...c,
      level: getLevel(c.cost),
    }));

    const months: { label: string; week: number }[] = [];
    let lastMonth = -1;
    for (const c of cellsWithLevel) {
      const month = new Date(c.date + "T00:00:00").getMonth();
      if (month !== lastMonth && c.dayOfWeek === 0) {
        months.push({
          label: new Date(c.date + "T00:00:00").toLocaleString(locale, {
            month: "short",
          }),
          week: c.week,
        });
        lastMonth = month;
      }
    }

    // 洞察也取自同一批单元格——色阶、最忙格与侧栏数字因此永远一致。
    let busiestDay: { date: string; cost: number } | null = null;
    let totalCost = 0;
    const weekdaySum = [0, 0, 0, 0, 0, 0, 0];
    const weekdayCount = [0, 0, 0, 0, 0, 0, 0];
    for (const c of allCells) {
      totalCost += c.cost;
      weekdaySum[c.dayOfWeek] = (weekdaySum[c.dayOfWeek] ?? 0) + c.cost;
      weekdayCount[c.dayOfWeek] = (weekdayCount[c.dayOfWeek] ?? 0) + 1;
      if (c.cost > 0 && (!busiestDay || c.cost > busiestDay.cost)) {
        busiestDay = { date: c.date, cost: c.cost };
      }
    }
    const weekdayAvg = weekdaySum.map((s, i) => {
      const count = weekdayCount[i] ?? 0;
      return count > 0 ? s / count : 0;
    });
    let busyDayIndex: number | null = null;
    let busyDayAvg = 0;
    let quietDayIndex: number | null = null;
    let quietDayAvg = Number.POSITIVE_INFINITY;
    weekdayAvg.forEach((avg, i) => {
      if (avg > busyDayAvg) {
        busyDayAvg = avg;
        busyDayIndex = i;
      }
      if (avg < quietDayAvg) {
        quietDayAvg = avg;
        quietDayIndex = i;
      }
    });
    if (quietDayAvg === Number.POSITIVE_INFINITY) quietDayAvg = 0;
    // 窗口内完全没有花费时，「最忙/最安静的星期几」没有意义（每天都平均成
    // 0），把这两个指标压掉。
    if (totalCost === 0) {
      busyDayIndex = null;
      quietDayIndex = null;
    }

    const insights: Insights = {
      busiestDay,
      busyDayIndex,
      busyDayAvg,
      quietDayIndex,
      quietDayAvg,
      totalCost,
      windowDays: allCells.length,
    };

    return { cells: cellsWithLevel, monthLabels: months, insights };
  }, [usage, pricings, tz, locale]);

  const labelWidth = 28;
  const svgWidth = labelWidth + HEATMAP_WEEKS * (CELL_SIZE + CELL_GAP);
  const svgHeight = 14 + 7 * (CELL_SIZE + CELL_GAP);

  // 垂直布局：热力图在上居中，洞察作为一条 4 格统计条在下方（以细线分隔）。
  // 垂直堆叠能保证父卡片的宽度完全由它自己的网格列决定——绝不取决于 SVG 的
  // 固有宽度或洞察标签的长度——切换本页签不会改变卡片的表现宽度。
  return (
    <div className="space-y-4">
      <div className="flex flex-col items-center gap-2">
        <div className="overflow-x-auto">
          <svg width={svgWidth} height={svgHeight} className="block">
            {monthLabels.map((m) => (
              <text
                key={`${m.label}-${m.week}`}
                x={labelWidth + m.week * (CELL_SIZE + CELL_GAP)}
                y={10}
                className="fill-muted-foreground"
                fontSize={9}
              >
                {m.label}
              </text>
            ))}
            {weekdayLabels.map((label, i) =>
              LABELED_WEEKDAY_INDICES.has(i) ? (
                <text
                  key={i}
                  x={0}
                  y={14 + i * (CELL_SIZE + CELL_GAP) + CELL_SIZE - 1}
                  className="fill-muted-foreground"
                  fontSize={9}
                >
                  {label}
                </text>
              ) : null,
            )}
            {cells.map((c) => (
              <rect
                key={c.date}
                x={labelWidth + c.week * (CELL_SIZE + CELL_GAP)}
                y={14 + c.dayOfWeek * (CELL_SIZE + CELL_GAP)}
                width={CELL_SIZE}
                height={CELL_SIZE}
                rx={3}
                fill={getHeatmapColor(c.level)}
                className="transition-colors"
              >
                {/* 悬浮提示的日期刻意保留 ISO 格式：它就是该格背后的精确日期
                    键，读者要拿它与 API / CLI 的用量行（同样是 ISO）对比。
                    只有它周围的说明文字才做翻译。 */}
                <title>
                  {c.date}:{" "}
                  {c.cost > 0
                    ? `$${c.cost.toFixed(2)}`
                    : t(($) => $.charts.heatmap_no_activity)}
                </title>
              </rect>
            ))}
          </svg>
        </div>
        <div className="flex items-center gap-1 text-micro text-muted-foreground">
          <span>{t(($) => $.charts.heatmap_less)}</span>
          {[0, 1, 2, 3, 4].map((level) => (
            <div
              key={level}
              className="h-[10px] w-[10px] rounded-[2px]"
              style={{ backgroundColor: getHeatmapColor(level) }}
            />
          ))}
          <span>{t(($) => $.charts.heatmap_more)}</span>
        </div>
      </div>

      <InsightsRow
        insights={insights}
        locale={locale}
        weekdayLabels={weekdayLabels}
      />
    </div>
  );
}

// 热力图下方的横向统计条。仿照页顶 KPI 首屏的「标签 → 大数值 → 副文案」
// 模式，但缩小到次要地位。桌面 4 列、窄屏 2 列。
function InsightsRow({
  insights,
  locale,
  weekdayLabels,
}: {
  insights: Insights;
  locale: string;
  weekdayLabels: string[];
}) {
  const { t } = useT("runtimes");
  const {
    busiestDay,
    busyDayIndex,
    busyDayAvg,
    quietDayIndex,
    quietDayAvg,
    totalCost,
    windowDays,
  } = insights;
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-3 border-t pt-3 sm:grid-cols-4">
      <Insight
        label={t(($) => $.charts.heatmap_busiest_day)}
        value={busiestDay ? fmtDate(busiestDay.date, locale) : "—"}
        sub={busiestDay ? formatUsd(busiestDay.cost) : null}
      />
      <Insight
        label={t(($) => $.charts.heatmap_most_active_weekday)}
        value={
          busyDayIndex === null ? "—" : (weekdayLabels[busyDayIndex] ?? "—")
        }
        sub={busyDayIndex !== null
          ? t(($) => $.charts.heatmap_average, { value: formatUsd(busyDayAvg) })
          : null}
      />
      <Insight
        label={t(($) => $.charts.heatmap_quietest_weekday)}
        value={
          quietDayIndex === null ? "—" : (weekdayLabels[quietDayIndex] ?? "—")
        }
        sub={quietDayIndex !== null
          ? t(($) => $.charts.heatmap_average, { value: formatUsd(quietDayAvg) })
          : null}
      />
      <Insight
        label={t(($) => $.charts.heatmap_window_total, { count: windowDays })}
        value={formatUsd(totalCost)}
      />
    </dl>
  );
}

function Insight({
  label,
  value,
  sub,
}: {
  label: string;
  value: string;
  sub?: string | null;
}) {
  return (
    <div className="min-w-0">
      <dt className="truncate text-micro uppercase tracking-wider text-muted-foreground">
        {label}
      </dt>
      <dd className="mt-0.5 truncate text-body font-medium tabular-nums">
        {value}
        {sub != null && (
          <span className="ml-1.5 text-caption font-normal text-muted-foreground">
            {sub}
          </span>
        )}
      </dd>
    </div>
  );
}
