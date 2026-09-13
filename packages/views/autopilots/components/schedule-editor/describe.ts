// 定时器计划的白话回读：把结构化的调度配置翻译成一句人话（如"每天 09:00"、
// "每 3 小时（9:00–21:00 窗口），工作日"），显示在编辑器底部的说明行。仅高级
// 模式（raw 非空）没有结构可描述，返回 null。

import { useCallback } from "react";
import { useT } from "../../../i18n";
import type { ScheduleConfig } from "./model";
import { consecutiveRuns, DAY_KEYS, pad2 } from "./model";

type AutopilotsT = ReturnType<typeof useT<"autopilots">>["t"];

/** 把 3 天及以上连续星期折叠成区间（"周一至周五"），其余逐个列出。两天的连续
 *  保持列表：读 "周一至周二" 不比读 "周一、周二" 短。 */
function formatDayList(t: AutopilotsT, days: number[]): string {
  const name = (d: number) => t(($) => $.schedule_editor.describe.days_long[DAY_KEYS[d]!]);
  const parts: string[] = [];
  for (const [lo, hi] of consecutiveRuns(days)) {
    if (hi - lo >= 2) {
      parts.push(
        t(($) => $.schedule_editor.describe.days_range, { from: name(lo), to: name(hi) }),
      );
    } else {
      for (let d = lo; d <= hi; d++) parts.push(name(d));
    }
  }
  return parts.join(t(($) => $.schedule_editor.describe.days_join));
}

/** 结构化调度计划的白话回读。仅高级模式（raw 非空）返回 null——那里没有结构化
 *  模型可描述。 */
export function describeSchedule(t: AutopilotsT, config: ScheduleConfig): string | null {
  if (config.raw !== null) return null;

  // 子句顺序跟着编辑器自己的字段自上而下：时间（步长与它的窗口是同一维度，
  // 保持相邻），然后是日期。
  const clauses: string[] = [];
  const { time } = config;
  if (time.kind === "at") {
    clauses.push(t(($) => $.schedule_editor.describe.time_at, { time: time.time }));
  } else if (time.unit === "hours") {
    if (time.window === null) {
      const minute = pad2(time.minute);
      clauses.push(
        time.interval === 1
          ? t(($) => $.schedule_editor.describe.time_every_hour, { minute })
          : t(($) => $.schedule_editor.describe.time_every_hours, {
              interval: time.interval,
              minute,
            }),
      );
    } else {
      clauses.push(
        time.interval === 1
          ? t(($) => $.schedule_editor.describe.time_every_hour_window)
          : t(($) => $.schedule_editor.describe.time_every_hours_window, {
              interval: time.interval,
            }),
      );
      clauses.push(
        t(($) => $.schedule_editor.describe.window, {
          from: time.window.from,
          to: time.window.to,
        }),
      );
    }
  } else {
    clauses.push(
      time.interval === 1
        ? t(($) => $.schedule_editor.describe.time_every_minute)
        : t(($) => $.schedule_editor.describe.time_every_minutes, { interval: time.interval }),
    );
    if (time.window !== null) {
      clauses.push(
        t(($) => $.schedule_editor.describe.window, {
          from: time.window.from,
          to: time.window.to,
        }),
      );
    }
  }

  switch (config.days.kind) {
    case "every":
      clauses.push(t(($) => $.schedule_editor.describe.days_every));
      break;
    case "weekly":
      clauses.push(formatDayList(t, config.days.daysOfWeek));
      break;
    case "monthly":
      clauses.push(
        t(($) => $.schedule_editor.describe.days_monthly, { day: config.days.dayOfMonth }),
      );
      break;
  }

  return clauses.join(" · ");
}

/**
 * Hook 包装：返回一个绑定当前 locale 的描述函数（config → 白话文本），供组件
 * 直接调用，避免每次渲染都要重新拿 t。
 * @returns 描述函数；高级模式返回 null。
 */
export function useDescribeSchedule(): (config: ScheduleConfig) => string | null {
  const { t } = useT("autopilots");
  return useCallback((config: ScheduleConfig) => describeSchedule(t, config), [t]);
}
