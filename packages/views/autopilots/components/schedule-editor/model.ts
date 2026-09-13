// 定时器（调度计划）的结构化模型与基础工具：把"几点跑、多久跑一次、哪天跑"表示成
// 可编辑的类型，并提供默认配置、星期常量与几个纯函数工具。cron 文本与结构体之间的
// 互相转换在 cron-mapping.ts，本文件只定义模型本身。
//
// 模型与 cron 是两种真相视图：能落进结构体的一切都优先用结构体编辑；超出表达能力
// 的复杂表达式才整段放进 `raw`（高级模式）原样往返。

export type TimePattern =
  | { kind: "at"; time: string } // "HH:MM"——在固定时刻跑
  | {
      kind: "every";
      interval: number; // 步长：按小时 1-23，按分钟 1-59
      unit: "minutes" | "hours";
      // null = 全天。按"小时"时窗口起点时刻的分就是触发分钟，`to` 携带同一分钟；
      // 按"分钟"时窗口以小时为粒度（从 :00 到 :59）。
      window: { from: string; to: string } | null;
      // 按"小时"模式的触发分钟（"每 N 小时的第 :M 分"）；设了窗口时与
      // window.from 的分钟保持同步。按"分钟"时携带但不用（toCron 忽略它），
      // 这样来回切换单位不会丢掉用户设的分钟。
      minute: number;
    };

export type DayPattern =
  | { kind: "every" } // 每天
  | { kind: "weekly"; daysOfWeek: number[] } // 0=周日 … 6=周六，非空、去重、升序
  | { kind: "monthly"; dayOfMonth: number }; // 每月第 1-31 日

export interface ScheduleConfig {
  time: TimePattern;
  days: DayPattern;
  timezone: string; // IANA 时区名
  // 表达式超出结构化模型时为非 null：编辑器进入仅高级模式，这段字符串一字不改地
  // 往返保存。
  raw: string | null;
}

// 时区列表在 views/common/timezone-select（browserTimezone / timezoneOptions）——
// 调用方把已解析的时区传进来，让本模块不碰平台探测。
export function getDefaultScheduleConfig(timezone: string): ScheduleConfig {
  return {
    time: { kind: "at", time: "09:00" },
    days: { kind: "every" },
    timezone,
    raw: null,
  };
}

// 位置就是 cron 的星期编号（周日为 0），所以这个顺序是有承载意义的：它既索引
// locale 的星期名，`daysOfWeek` 里存的也是这个编号。
export const DAY_KEYS = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"] as const;

/** 升序星期集合里"连续天"的最大分段：[1,2,3,4,5] → 一段 1–5；[0,1,2,4] →
 *  0–2 与 4–4。什么长度的段值得折叠由调用方决定——cron 连两个都折叠（"0-1"），
 *  自然语言回读要三个才说"周一至周三"——但"分段在哪"只有一个问题一个答案，
 *  两个调用方过去各自扫描一遍，现在统一在这里算。 */
export function consecutiveRuns(days: number[]): Array<[number, number]> {
  const sorted = Array.from(new Set(days)).toSorted((a, b) => a - b);
  const runs: Array<[number, number]> = [];
  let i = 0;
  while (i < sorted.length) {
    let j = i;
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j]! + 1) j++;
    runs.push([sorted[i]!, sorted[j]!]);
    i = j + 1;
  }
  return runs;
}

/** 补零成两位数，如 9 → "09"。 */
export function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/** 把 "HH:MM" 拆成小时与分钟数字。 */
export function timeParts(time: string): { hour: number; minute: number } {
  const [h, m] = time.split(":");
  return { hour: parseInt(h ?? "0", 10), minute: parseInt(m ?? "0", 10) };
}
