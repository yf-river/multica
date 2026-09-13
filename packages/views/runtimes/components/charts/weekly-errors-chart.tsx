// ============================================================================
// 每周失败数堆叠图（runtime 用量页「周视图」错误）
//
// 角色定位：DailyErrorsChart 的周粒度版本——同样按失败类别堆叠，每根柱代表
// 一个自然周（周一到周日）。进行中的周半透明渲染，与其它周图表一致，让
// 未满的一周在视觉上处于从属地位，不会被误读为「本周失败激增/暴跌」。
// ============================================================================
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Cell } from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@multica/ui/components/ui/chart";
import { useT } from "../../../i18n";
import {
  activeFailureClasses,
  formatRate,
  labelOf,
  useFailureClassConfig,
  type FailureBucketTotals,
  type FailureClassCounts,
} from "./failure-class-visuals";

// 一周的数据：周起止/标签/范围 + partial/daysCovered + 各类失败计数与
// failed/total（用于提示里的失败率）。
export interface WeeklyErrorsData extends FailureClassCounts, FailureBucketTotals {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
}

/**
 * 每周按失败类别堆叠的失败任务柱状图——DailyErrorsChart 的周粒度版本。
 * 每根柱是一个自然周；进行中的周半透明渲染（与其它周图表一致），读作从属
 * 地位的「进行中」。
 */
export function WeeklyErrorsChart({ data }: { data: WeeklyErrorsData[] }) {
  const { t } = useT("usage");
  const config = useFailureClassConfig();
  const classes = activeFailureClasses(data);

  return (
    <ChartContainer config={config} className="aspect-[3/1] w-full">
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
          allowDecimals={false}
          width="auto"
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelKey="rangeLabel"
              labelFormatter={(_label, payload) => {
                const row = payload[0]?.payload as WeeklyErrorsData | undefined;
                if (!row) return "";
                return row.partial
                  ? t(($) => $.weekly.partial_label, {
                      range: row.rangeLabel,
                      covered: row.daysCovered,
                    })
                  : row.rangeLabel;
              }}
              // `name` 是 Recharts 的 dataKey，即裸类别 id（"rate_limit"）。
              // 经图表 config 解析成已翻译的标签，让提示与图例措辞一致。
              formatter={(value, name) => `${value} ${labelOf(config, name)}`}
              footer={(payload) => {
                const row = payload[0]?.payload as WeeklyErrorsData | undefined;
                if (!row) return null;
                return (
                  <div className="flex items-center justify-between gap-2 font-medium">
                    <span>{t(($) => $.errors.tooltip_rate)}</span>
                    <span className="font-mono tabular-nums">
                      {formatRate(row.failed, row.total)}
                    </span>
                  </div>
                );
              }}
            />
          }
        />
        {classes.map((c, i) => (
          <Bar
            key={c}
            dataKey={c}
            stackId="errors"
            fill={`var(--color-${c})`}
            radius={i === classes.length - 1 ? [3, 3, 0, 0] : [0, 0, 0, 0]}
          >
            {data.map((d) => (
              <Cell key={`${d.weekStart}-${c}`} fillOpacity={d.partial ? 0.5 : 1} />
            ))}
          </Bar>
        ))}
      </BarChart>
    </ChartContainer>
  );
}
