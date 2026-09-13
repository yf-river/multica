// ============================================================================
// 每日 token 数四段堆叠图（runtime 用量页 token 概览）
//
// 角色定位：把每天四种 token——输入/输出/缓存读取/缓存写入——的计数画成
// 纵向堆叠柱，让用户看到「用量真正长什么样」：Claude 典型的一天里，缓存
// 读取往往远超原始输入 token 数（常为 10× 以上），不把读取画进去就会漏掉
// 大头。数据来自 utils.ts 的 aggregateByDate。
//
// 配色约定：与 DailyCostChart 完全一致（input→chart-1、output→chart-2、
// cacheRead→chart-4、cacheWrite→chart-3），这样同一天的「成本视图」与
// 「token 视图」读起来像同一张图换了刻度。
// ============================================================================
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
} from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { formatTokens, type DailyTokenData } from "../../utils";
import { useLocale, useT } from "../../../i18n";

// 四段堆叠——输入 / 输出 / 缓存读取 / 缓存写入。与成本图不同，这里缓存读取
// 一定要可见：Claude 上典型的一天里缓存读取占原始 token 的大头（常为输入
// 的 10 倍以上），不把它堆叠进来，用户根本看不到用量的真实形状。成本图
// 之所以丢掉它，是相反的原因（缓存读取的美元贡献小两个数量级，画出来
// 在视觉上不可见）。
//
// 序列 → CSS 图表 token：从下往上依次是 chart-1（最深的品牌蓝，「输入」）
// → chart-2（中）→ chart-4（缓存读取）→ chart-3（最浅，「缓存写入」）。
// 缓存读取用 chart-4，让两条缓存序列在视觉上相邻，且与输入/输出的色调区分开。
export const tokenStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheRead: { label: "Cache read", color: "var(--chart-4)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

/**
 * 每日四种 token 数的堆叠柱状图（y 轴与悬浮提示都用 formatTokens 缩写）。
 */
export function DailyTokensChart({ data }: { data: DailyTokenData[] }) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  // 没有内置空状态——与 DailyCostChart 同一约定：没有数据时渲染成什么由父
  // 组件决定。
  return (
    <ChartContainer config={tokenStackConfig} className="aspect-[3/1] w-full">
      <BarChart data={data} margin={{ left: 0, right: 0, top: 4, bottom: 0 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="label"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          interval="preserveStartEnd"
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          tickFormatter={(v: number) => formatTokens(v)}
          width="auto"
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              formatter={(value, name) =>
                typeof value === "number"
                  ? `${formatTokens(value)} ${name}`
                  : `${value} ${name}`
              }
              footer={(payload) => {
                const total = payload.reduce(
                  (sum, item) =>
                    sum +
                    (typeof item.value === "number" ? item.value : 0),
                  0,
                );
                return (
                  <div className="flex items-center justify-between gap-2 font-medium">
                    <span>{t(($) => $.charts.tooltip_total)}</span>
                    <span className="font-mono tabular-nums">
                      {total.toLocaleString(locale)}
                    </span>
                  </div>
                );
              }}
            />
          }
        />
        {/* 图例由父组件渲染在图表卡片头部，这里不放。 */}
        <Bar
          dataKey="input"
          stackId="tokens"
          fill="var(--color-input)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="output"
          stackId="tokens"
          fill="var(--color-output)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cacheRead"
          stackId="tokens"
          fill="var(--color-cacheRead)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cacheWrite"
          stackId="tokens"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        />
      </BarChart>
    </ChartContainer>
  );
}
