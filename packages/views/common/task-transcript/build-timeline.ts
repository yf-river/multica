// 任务时间线构建：把服务器推来的原始任务消息（TaskMessagePayload 事件）整理成按
// 时间顺序排列的统一时间线条目（工具调用/调用结果/思考/文本/错误），并做两件事：
// 合并相邻的同类型文本片段、给内容里的敏感信息脱敏。转录弹窗显示的就是这里产出的
// 时间线。

import type { TaskMessagePayload } from "@multica/core/types/events";
import { redactSecrets } from "./redact";

/**
 * 一条统一的时间线条目：把工具调用、思考、文本和错误按时间先后排成一列。
 * 各字段对应原始消息里的同名属性，只做聚合不做裁剪。
 */
export interface TimelineItem {
  id?: string;
  taskId?: string;
  callId?: string;
  outputChars?: number;
  outputTruncated?: boolean;
  /** 全局递增序号，用于排序（不同来源的消息共用同一把序号）。 */
  seq: number;
  /** 条目类型：工具调用 / 工具结果 / 思考 / 文本 / 错误。 */
  type: "tool_use" | "tool_result" | "thinking" | "text" | "error";
  /** 工具名（仅 tool_use/tool_result 类条目有）。 */
  tool?: string;
  /** 内容文本（思考、文本、错误信息等）。 */
  content?: string;
  /** 工具调用的入参对象（仅 tool_use 有）。 */
  input?: Record<string, unknown>;
  /** 工具调用的输出文本（仅 tool_result 有）。 */
  output?: string;
  /** 产生时间（ISO 字符串）。 */
  created_at?: string;
}

// 相邻的 thinking/text 是否能合并：只要两条类型相同且都属于"流式文本"类
// （thinking 或 text）就可以拼成一条。tool_use/tool_result/error 永远不合并。
function canMergeStreamingText(prev: TimelineItem, next: TimelineItem): boolean {
  return (prev.type === "thinking" || prev.type === "text") && prev.type === next.type;
}

/**
 * 合并相邻的文本/思考片段。
 *
 * 为什么需要：智能体的输出是流式产生的，daemon 在刷盘时可能把一段连续的话按
 * 时间片切成多条消息，界面上一行一句地渲染会让读者看到断断续续的碎片。这里把
 * 类型相同且相邻的片段拼成一条完整文本；合并时保留首条的时间，避免时间点跳来跳去。
 * @param items 原始条目列表（乱序也没关系）。
 * @returns 按 seq 升序、相邻同类文本已合并的列表。
 */
export function coalesceTimelineItems(items: TimelineItem[]): TimelineItem[] {
  const sorted = [...items].sort((a, b) => a.seq - b.seq);
  const out: TimelineItem[] = [];

  for (const item of sorted) {
    const prev = out[out.length - 1];
    if (prev && canMergeStreamingText(prev, item)) {
      out[out.length - 1] = {
        ...prev,
        content: `${prev.content ?? ""}${item.content ?? ""}`,
        created_at: item.created_at ?? prev.created_at,
      };
      continue;
    }
    out.push(item);
  }

  return out;
}

/**
 * 往时间线追加一条新条目（先追加再整体合并/排序）。适合把新到达的消息增量地
 * 并进已有时间线的场景。
 * @param items 现有时间线。
 * @param item 新到达的条目。
 * @returns 追加并合并后的新时间线。
 */
export function appendTimelineItem(items: TimelineItem[], item: TimelineItem): TimelineItem[] {
  return coalesceTimelineItems([...items, item]);
}

// 对一批条目的 content/output 逐条做敏感信息脱敏，返回新数组（不改原数组）。
function redactTimelineItems(items: TimelineItem[]): TimelineItem[] {
  return items.map((item) => ({
    ...item,
    content: item.content ? redactSecrets(item.content) : item.content,
    output: item.output ? redactSecrets(item.output) : item.output,
  }));
}

/**
 * 从原始任务消息构建一条按时间排序、同类文本已合并、敏感信息已脱敏的时间线。
 * @param msgs 服务器下发的原始任务消息列表。
 * @returns 可直接用于渲染的时间线条目数组。
 */
export function buildTimeline(msgs: TaskMessagePayload[]): TimelineItem[] {
  const items: TimelineItem[] = [];
  for (const msg of msgs) {
    // 遥测由运行上下文界面消费，不作为对话正文或过程步骤显示。
    if (msg.type === "runtime") continue;
    items.push({
      id: msg.id,
      taskId: msg.task_id,
      outputChars: msg.output_chars,
      outputTruncated: msg.output_truncated,
      seq: msg.seq,
      type: msg.type,
      tool: msg.tool,
      callId: msg.call_id,
      content: msg.content,
      input: msg.input,
      output: msg.output,
      created_at: msg.created_at,
    });
  }
  return redactTimelineItems(coalesceTimelineItems(items));
}
