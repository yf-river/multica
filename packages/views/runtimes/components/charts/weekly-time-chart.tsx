import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { useT } from "../../../i18n";
import { PartialWeekCells, RuntimeBarChart } from "./runtime-bar-chart";

// Weekly counterpart of DailyTimeChart — same single-series bar, but each
// bar represents Mon–Sun run-time totals. Partial weeks render at half
// opacity and tag their tooltip with "(partial · N / 7 days)" so the user
// can't misread an in-progress week as a sudden drop.
const weeklyTimeChartConfig = {
  totalSeconds: { label: "Run time", color: "var(--chart-1)" },
} satisfies ChartConfig;

export interface WeeklyTimeData {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
  totalSeconds: number;
}

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
    <RuntimeBarChart
      config={weeklyTimeChartConfig}
      data={data}
      yAxisWidth={56}
      tickFormatter={(v: number) => formatY(v)}
      tooltip={
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
      }
    >
        <Bar
          dataKey="totalSeconds"
          fill="var(--color-totalSeconds)"
          radius={[3, 3, 0, 0]}
        >
          <PartialWeekCells data={data} />
        </Bar>
    </RuntimeBarChart>
  );
}
