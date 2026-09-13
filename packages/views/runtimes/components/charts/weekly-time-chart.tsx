// ============================================================================
// 每周运行时长柱状图（runtime 用量页「周视图」运行时间）
//
// 角色定位：DailyTimeChart 的周粒度版本——同样单序列柱状图，但每根柱代表
// 周一到周日累计的运行秒数。进行中的周半透明显示，并在悬浮提示标注
// 「(进行中 · N / 7 天)」，避免把未满的一周误读成突然下降。
// ============================================================================
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Cell,
} from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { useT } from "../../../i18n";

// 单序列配色：周运行时长用品牌 chart-1 色（与日视图一致）。
const weeklyTimeChartConfig = {
  totalSeconds: { label: "Run time", color: "var(--chart-1)" },
} satisfies ChartConfig;

// 每周一条：周起止/展示标签/范围文案 + 是否进行中（partial）+ 已过天数 +
// 该周累计运行秒数。
export interface WeeklyTimeData {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
  totalSeconds: number;
}

/**
 * 每周运行时长柱状图。进行中的周（partial=true）半透明显示、提示标注天数。
 */
export function WeeklyTimeChart({
  data,
  formatY,
  formatTooltip,
}: {
  data: WeeklyTimeData[];
  formatY: (seconds: number) => string;
  formatTooltip: (seconds: number) => string;
}) {
  const { t } = useT("usage");
  return (
    <ChartContainer
      config={weeklyTimeChartConfig}
      className="aspect-[3/1] w-full"
    >
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
          tickFormatter={(v: number) => formatY(v)}
          width="auto"
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelKey="rangeLabel"
              labelFormatter={(_label, payload) => {
                const row = payload[0]?.payload as WeeklyTimeData | undefined;
                if (!row) return "";
                return row.partial
                  ? t(($) => $.weekly.partial_label, {
                      range: row.rangeLabel,
                      covered: row.daysCovered,
                    })
                  : row.rangeLabel;
              }}
              formatter={(value, name) =>
                typeof value === "number"
                  ? `${formatTooltip(value)} ${name}`
                  : `${value} ${name}`
              }
            />
          }
        />
        <Bar
          dataKey="totalSeconds"
          fill="var(--color-totalSeconds)"
          radius={[3, 3, 0, 0]}
        >
          {data.map((d) => (
            <Cell key={d.weekStart} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
      </BarChart>
    </ChartContainer>
  );
}
