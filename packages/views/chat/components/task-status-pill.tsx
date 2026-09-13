// 任务状态小胶囊：在消息列表顶部展示当前 pending 任务进行到哪一步
// （排队/启动/思考/输入中/工具调用等），并附带已耗时时长。
"use client";

import { useEffect, useRef, useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { UnicodeSpinner } from "@multica/ui/components/common/unicode-spinner";
import type { AgentAvailability } from "@multica/core/agents";
import type { ChatPendingTask, TaskMessagePayload } from "@multica/core/types";
import { formatElapsedSecs } from "../lib/format";
import { useT } from "../../i18n";

interface Props {
  /** 服务端权威的 pending 任务快照（`created_at` 是计时起点）。 */
  pendingTask: ChatPendingTask;
  /** 实时任务消息流——最新一条非错误条目决定"运行中"阶段文案。 */
  taskMessages: readonly TaskMessagePayload[];
  /** 已解析的在线状态；传 `undefined` 隐藏在线提示。 */
  availability: AgentAvailability | undefined;
}

interface Stage {
  label: string;
  static?: boolean;
}

type StageKey =
  | "offline"
  | "reconnecting"
  | "retrying"
  | "queued"
  | "waiting_local_directory"
  | "starting_up"
  | "thinking"
  | "typing";

type ToolKey =
  | "running_command"
  | "reading_files"
  | "searching_code"
  | "making_edits"
  | "searching_web"
  | "fallback";

// 工具 slug → 翻译 key 映射。未知工具回退到"Working"。
const TOOL_KEY_BY_SLUG: Record<string, Exclude<ToolKey, "fallback">> = {
  bash: "running_command",
  exec: "running_command",
  read: "reading_files",
  glob: "reading_files",
  grep: "searching_code",
  write: "making_edits",
  edit: "making_edits",
  multi_edit: "making_edits",
  multiedit: "making_edits",
  web_search: "searching_web",
  websearch: "searching_web",
};

// 纯函数式的阶段决策，返回的是翻译 key。下面的 hook 再把这些 key 映射成本地化文案
// ——把决策保持纯净，让优先级规则不掺杂翻译噪音，更容易阅读。
/**
 * 根据任务状态 / 任务消息流 / agent 在线状态，决定应显示哪个阶段的 key。
 * 纯函数，不读 hook 状态，便于单测与阅读。
 */
export function pickStageKeys(
  status: string | undefined,
  taskMessages: readonly TaskMessagePayload[],
  availability: AgentAvailability | undefined,
): {
  stageKey: StageKey;
  toolKey?: ToolKey;
  static?: boolean;
  needsWaitReason?: boolean;
} {
  // 被延期的聊天任务是等退避重试的旧轮次，不是活跃的模型工作。把它排在在线提示
  // 前面，让这种明确的"重试中"状态永远不会降级成误导性的 queued/thinking 文案。
  if (status === "deferred") return { stageKey: "retrying" };
  if (
    (status === "queued" || status === "dispatched") &&
    availability === "offline"
  ) {
    return { stageKey: "offline", static: true };
  }
  if (
    (status === "queued" || status === "dispatched") &&
    availability === "unstable"
  ) {
    return { stageKey: "reconnecting" };
  }
  // daemon 为 local_directory 流程发出的阻塞状态：项目被钉在一条当前由别的任务
  // 占用的路径上。daemon 出队任务却拿不到路径锁时发布这个状态串；渲染层给出专门
  // 的文案，让用户理解为什么排队的任务一直不动。
  if (status === "waiting_local_directory") {
    return {
      stageKey: "waiting_local_directory",
      static: true,
      needsWaitReason: true,
    };
  }
  if (status === "queued") return { stageKey: "queued" };
  if (status === "dispatched") return { stageKey: "starting_up" };

  // running：由最新一条有意义的消息决定文案。
  let latest: TaskMessagePayload | null = null;
  for (let i = taskMessages.length - 1; i >= 0; i--) {
    const m = taskMessages[i];
    if (m && m.type !== "error" && m.type !== "tool_result") {
      latest = m;
      break;
    }
  }

  if (!latest) return { stageKey: "thinking" };
  if (latest.type === "thinking") return { stageKey: "thinking" };
  if (latest.type === "text") return { stageKey: "typing" };
  if (latest.type === "tool_use") {
    const tool = (latest.tool ?? "").toLowerCase();
    const toolKey = TOOL_KEY_BY_SLUG[tool] ?? "fallback";
    // tool_use 严格说仍是"thinking + tool"——工具名走 toolKey 通道输出，
    // 阶段文案主体也采用工具文案。
    return { stageKey: "thinking", toolKey };
  }
  return { stageKey: "thinking" };
}

/**
 * 计算"有效状态"：有任务消息流就认为已开始运行，除非服务端随后把同一任务移进
 * 重试退避（deferred）。
 */
export function effectiveTaskStatus(
  status: string | undefined,
  taskMessages: readonly TaskMessagePayload[],
): string | undefined {
  // 重试会保留任务早先的流历史。deferred 是更新的、服务端权威的状态，
  // 必须压过那段过期的 running 证据。
  if (status === "deferred") return status;
  return taskMessages.length > 0 ? "running" : status;
}

function useResolveStage(): (
  status: string | undefined,
  taskMessages: readonly TaskMessagePayload[],
  availability: AgentAvailability | undefined,
  waitReason?: string,
) => Stage {
  const { t } = useT("chat");
  return (status, taskMessages, availability, waitReason) => {
    const decision = pickStageKeys(status, taskMessages, availability);
    if (decision.toolKey) {
      return {
        label: t(($) => $.status_pill.tools[decision.toolKey!]),
      };
    }
    // 停住的任务如果说明了卡在什么上，就能把一段无法解释的等待变成可行动的：
    // 用户能看到那是另一个兄弟任务，而不是挂死的 agent，从而决定取消是否值得。
    // 旧版服务端不传原因，所以裸文案必须仍然可用。
    const reason = waitReason?.trim();
    if (decision.needsWaitReason && reason) {
      return {
        label: t(($) => $.status_pill.stages.waiting_local_directory_reason, {
          reason,
        }),
        static: decision.static,
      };
    }
    return {
      label: t(($) => $.status_pill.stages[decision.stageKey]),
      static: decision.static,
    };
  };
}

/**
 * 任务状态小胶囊：显示当前任务进行到哪个阶段 + 已耗时。
 * @param pendingTask - 服务端权威的 pending 任务快照。
 * @param taskMessages - 实时任务消息流。
 * @param availability - agent 在线状态。
 */
export function TaskStatusPill({
  pendingTask,
  taskMessages,
  availability,
}: Props) {
  const resolveStage = useResolveStage();
  // 计时锚点：在首次渲染时锁死，之后绝不再赋值——否则当乐观种下的 `Date.now()`
  // 锚点被早几百毫秒的服务端 created_at 替换时，计时器会明显往回跳。
  // 单调递增的耗时比精确到毫秒更重要。
  const anchorRef = useRef<number | null>(null);
  if (anchorRef.current === null) {
    if (pendingTask.created_at) {
      const t = Date.parse(pendingTask.created_at);
      anchorRef.current = Number.isFinite(t) ? t : Date.now();
    } else {
      anchorRef.current = Date.now();
    }
  }
  const anchor = anchorRef.current;

  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  // 有效状态——有流式消息就证明任务已开始，除非服务端此后把同一任务移进重试退避。
  const status = effectiveTaskStatus(pendingTask.status, taskMessages);
  const elapsedSecs = Math.max(0, Math.floor((now - anchor) / 1000));
  // 只在有效状态仍是"等待中"时才读原因：流式消息可能在下一个服务端载荷清掉已存
  // 文案之前，先在本地把缓存里的等待任务提升成 "running"。
  const stage = resolveStage(
    status,
    taskMessages,
    availability,
    status === "waiting_local_directory" ? pendingTask.wait_reason : undefined,
  );

  return (
    <div
      className="flex items-center gap-1.5 px-1 text-caption text-muted-foreground"
      aria-live="polite"
    >
      {!stage.static && (
        <UnicodeSpinner name="breathe" className="opacity-70" />
      )}
      <span className="truncate">
        <span className={cn(!stage.static && "animate-chat-text-shimmer")}>
          {stage.label}
        </span>
        <span className="opacity-70 tabular-nums"> · {formatElapsedSecs(elapsedSecs)}</span>
      </span>
    </div>
  );
}
