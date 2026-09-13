// 定时器模式的切换/钳制转换：用户在"固定时刻 ↔ 每 N 小时/分钟 ↔ 窗口 ↔ 全天"
// 之间切来切去时，旧值往往无法直接装入新形态（比如按小时跑的窗口在固定时刻里没有
// 位置）。这里的锚点（anchor）记忆与重基准逻辑保证：切走再切回来，用户真正表达过
// 的时刻/步长/窗口不至于丢。

import type { TimePattern } from "./model";
import { pad2, timeParts } from "./model";

export type EveryPattern = Extract<TimePattern, { kind: "every" }>;
export type ScheduleWindow = { from: string; to: string };

// 窗口是计划的一个维度，不是编辑器的一种模式：全天就是"横跨一天的窗口"，模型只
// 用唯一一种写法表达它——`window: null`。编辑器提交的每个窗口都经过这里，两种
// 对同一计划的表达永远不会漂移成两套。
//
// 边界按小时读。按分钟的窗口以小时为粒度（两端读作 :00 与 :59），按小时的窗口在
// 两端携带触发分钟——任一分针都不决定窗口是否横跨全天。
export function isFullDay(window: ScheduleWindow): boolean {
  return timeParts(window.from).hour === 0 && timeParts(window.to).hour === 23;
}

/** 控件所显示的窗口。全天没有自己的窗口，但两个输入框仍要渲染点东西：就是它所
 *  代表的边界（00:00–23:59 或 带触发分钟的首尾）。 */
export function displayWindow(time: EveryPattern): ScheduleWindow {
  if (time.window !== null) return time.window;
  return time.unit === "hours"
    ? { from: `00:${pad2(time.minute)}`, to: `23:${pad2(time.minute)}` }
    : { from: "00:00", to: "23:59" };
}

// "每 N 小时、全天"的模式没地方放小时值，所以用户真正表达过的最后那个 时:分 要
// 在模型外携带。没有它，14:30 → 切到间隔 → 切回固定时刻会悄悄变回 09:30。
//
// 小时来自窗口、分钟来自 `minute`——绝不取自窗口自身的文本。按分钟的窗口是小时
// 粒度的，其边界读作 :00；从那里取分钟会丢掉用户在切换前输入、模型仍持有的 14:30
// 的 30。
//
// 全天没有小时可给：它的输入框显示 00:00 是"没有边界"，不是用户挑的时间，把它带
// 回去会在回到固定时刻时把用户的 14:30 改写成 00:30。
export function timeAnchorOf(time: TimePattern): string | null {
  if (time.kind === "at") return time.time;
  if (time.window === null) return null;
  return `${pad2(timeParts(time.window.from).hour)}:${pad2(time.minute)}`;
}

/** 切到"固定时刻"时的默认时间：优先用切换前的时间锚点；否则用切换参数里的锚 +
 *  旧时刻的分（若旧的也是 every 模式，则是它携带的触发分）。
 */
export function defaultAtTime(prev: TimePattern, anchor: string): string {
  return (
    timeAnchorOf(prev) ??
    `${pad2(timeParts(anchor).hour)}:${pad2(prev.kind === "every" ? prev.minute : 0)}`
  );
}

// 切到固定时刻只能带走一个 HH:MM，所以步长/单位/窗口切走就没了——"每 3 小时、
// 9:00–21:00"会以"每小时、全天"回来。它们在模型外被记住，并在用户此刻正切离的
// 时间基础上重建恢复。
export function defaultEveryTime(
  prev: TimePattern,
  anchor: EveryPattern | null,
): EveryPattern {
  if (prev.kind === "every") return prev;
  const { hour, minute } = timeParts(prev.time);
  const base: EveryPattern = anchor ?? {
    kind: "every",
    interval: 1,
    unit: "hours",
    window: null,
    minute,
  };
  const time: EveryPattern = { ...base, minute };
  if (time.window === null) return time;
  // 窗口在用户正切离的那个时间点重新开始——但仅当那仍留得下窗口。"09:00–15:00"
  // 重基准到 22:00 会把结束拖到 22、还给用户孤零零的一小时 22:00–22:00，恰恰毁掉
  // 这个锚要保住的窗口。
  // 必须严格更早：起点重基准到结束小时本身也留下单小时 15:00–15:00，是同一种坍缩
  // 的另一个名字。
  const rebased =
    hour < timeParts(time.window.to).hour
      ? { from: `${pad2(hour)}:${pad2(minute)}`, to: time.window.to }
      : time.window;
  // 把 23 点结束的窗口重基准到午夜固定时刻会横跨全天，而横跨全天的窗口不是窗口——
  // 模型把那种状态记成全天，别的表达不得再吐出一个第二形态。
  const window = clampWindow(rebased, time.unit, minute);
  return { ...time, window: isFullDay(window) ? null : window };
}

// 让窗口保持规范形态：`minute`——不是窗口自身的文本——是触发分钟的唯一真源，
// 所以 小时→分钟→小时 的单位往返会还原它，而不是留下被清零的值。按分钟时窗口是
// 小时粒度的，且 `to` 永远不早于 `from`。
export function clampWindow(
  window: ScheduleWindow,
  unit: EveryPattern["unit"],
  minute: number,
): ScheduleWindow {
  const from = timeParts(window.from);
  const to = timeParts(window.to);
  const edgeMinute = unit === "hours" ? minute : 0;
  const endMinute = unit === "hours" ? minute : 59;
  const endHour = Math.max(to.hour, from.hour);
  return {
    from: `${pad2(from.hour)}:${pad2(edgeMinute)}`,
    to: `${pad2(endHour)}:${pad2(endMinute)}`,
  };
}

/** 勾选/取消某个星期；结果保持非空且升序（不允许把最后一天也取消掉）。 */
export function toggleDay(days: number[], day: number): number[] {
  if (days.includes(day)) {
    if (days.length === 1) return days;
    return days.filter((d) => d !== day);
  }
  return [...days, day].toSorted((a, b) => a - b);
}
